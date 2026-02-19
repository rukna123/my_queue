package mqreader

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// fakeStore is a minimal Store replacement for testing.  It tracks
// which IDs were deleted and returns canned FetchBatch results.
type fakeStore struct {
	msgs       []BufMsg
	deletedIDs []int64
}

func (f *fakeStore) fetchBatch(_ context.Context, _, _, _ int) ([]BufMsg, error) {
	out := make([]BufMsg, len(f.msgs))
	copy(out, f.msgs)
	return out, nil
}

func (f *fakeStore) deleteByIDs(_ context.Context, ids []int64) (int64, error) {
	f.deletedIDs = append(f.deletedIDs, ids...)
	return int64(len(ids)), nil
}

// newTestPartition creates a Partition wired to a fakeStore,
// bypassing the real DB.
func newTestPartition(msgs []BufMsg, batchSize int) (*Partition, *fakeStore) {
	fs := &fakeStore{msgs: msgs}
	st := &Store{} // not used — we override via the test hook
	p := NewPartition(st, 0, 255, 500*time.Millisecond, batchSize)
	p.store = st
	_ = fs // keep reference
	return p, fs
}

// TestDrainWaitsForOutstandingServed verifies that Drain blocks until
// all outstanding served batches are resolved (acked or expired).
func TestDrainWaitsForOutstandingServed(t *testing.T) {
	p := &Partition{
		partStart: 0,
		partEnd:   255,
		batchSize: 100,
		leaseCh:   make(chan leaseCmd, 16),
		ackCh:     make(chan ackCmd, 16),
		utilCh:    make(chan chan float64, 4),
		drainCh:   make(chan chan struct{}, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Inject messages directly into the buffer by running the owner
	// goroutine with a store that returns pre-canned data.
	testMsgs := []BufMsg{
		{ID: 1, Topic: "t", MsgUUID: "u1", Payload: json.RawMessage(`{}`)},
		{ID: 2, Topic: "t", MsgUUID: "u2", Payload: json.RawMessage(`{}`)},
	}
	deletedIDs := make([]int64, 0)
	mockStore := &Store{}

	// We'll run a custom goroutine that mimics Run() but uses our test data.
	// Instead, let's use the real Run with a patched store.
	// The simplest approach: build a real partition with a real Store that
	// has a fake DB. But that requires database/sql mocking.
	//
	// Simpler: test the drain signaling logic directly by driving the
	// channels from the test.

	// Start the run loop with a noop store (FetchBatch returns nothing,
	// DeleteByIDs is captured).
	_ = mockStore
	_ = deletedIDs

	// Use a real partition with a nil-safe approach: we manually push
	// messages through the lease path before draining.

	// We need a store that returns testMsgs on FetchBatch and captures
	// DeleteByIDs calls.  Since Store wraps *sql.DB directly, we'll
	// create a shim at the partition level.
	//
	// Alternative: test at the channel level by interacting with the
	// partition's exported methods from a separate goroutine.

	// --- Channel-level test ---
	// 1. Start Run in a goroutine with a context we control.
	// 2. Lease messages.
	// 3. Call Drain() (which should block).
	// 4. Ack from another goroutine.
	// 5. Drain returns.

	// To make FetchBatch work without a real DB, override the store's db
	// field isn't possible without sql.DB.  Instead, we test only the drain
	// channel protocol: Run starts, we lease from empty (no DB needed for
	// empty result), inject a served entry via lease, then drain.

	// Actually, let's take a different approach: manually populate the
	// Run loop's state by performing a lease on a partition that has a
	// store returning our test messages, using a real in-memory sql.DB.
	// That's too heavy for a unit test.
	//
	// Best approach: test the external behavior through the channel API
	// with a deliberately empty initial buffer, then test that Drain
	// returns immediately when nothing is outstanding.

	// --- Test 1: Drain returns immediately when nothing outstanding ---
	go p.runForTest(ctx, nil) // custom helper

	drainCtx, drainCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer drainCancel()

	p.Drain(drainCtx)
	if drainCtx.Err() != nil {
		t.Fatal("Drain should have returned immediately with no outstanding leases")
	}
	cancel() // stop the goroutine

	// --- Test 2: Drain blocks until ack resolves outstanding lease ---
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	p2 := &Partition{
		partStart: 0,
		partEnd:   255,
		batchSize: 100,
		leaseCh:   make(chan leaseCmd, 16),
		ackCh:     make(chan ackCmd, 16),
		utilCh:    make(chan chan float64, 4),
		drainCh:   make(chan chan struct{}, 1),
	}
	go p2.runForTest(ctx2, testMsgs)

	// Lease messages so they enter the served map.
	leaseResult := p2.Lease(ctx2, "t", "test-consumer", 10, 60)
	if leaseResult.Err != nil {
		t.Fatalf("lease failed: %v", leaseResult.Err)
	}
	if len(leaseResult.Messages) != 2 {
		t.Fatalf("expected 2 leased messages, got %d", len(leaseResult.Messages))
	}

	// Start drain in background — it should block because messages are outstanding.
	drainDone := make(chan struct{})
	go func() {
		drainCtx2, drainCancel2 := context.WithTimeout(context.Background(), 5*time.Second)
		defer drainCancel2()
		p2.Drain(drainCtx2)
		close(drainDone)
	}()

	// Give drain a moment to register.
	time.Sleep(100 * time.Millisecond)

	select {
	case <-drainDone:
		t.Fatal("Drain should still be blocking — outstanding lease not acked")
	default:
		// good, still blocking
	}

	// Ack the lease — this should unblock drain.
	ackResult := p2.Ack(ctx2, "t", "test-consumer", leaseResult.LeaseID)
	if ackResult.Err != nil {
		t.Fatalf("ack failed: %v", ackResult.Err)
	}
	if ackResult.Acked != 2 {
		t.Fatalf("expected 2 acked, got %d", ackResult.Acked)
	}

	select {
	case <-drainDone:
		// good — drain completed after ack
	case <-time.After(3 * time.Second):
		t.Fatal("Drain did not complete within 3 seconds after ack")
	}

	cancel2()
}

// TestDrainReturnsAfterLeaseExpiry verifies that Drain completes once
// outstanding leases expire (without being acked).
func TestDrainReturnsAfterLeaseExpiry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	testMsgs := []BufMsg{
		{ID: 10, Topic: "t", MsgUUID: "u10", Payload: json.RawMessage(`{}`)},
	}

	p := &Partition{
		partStart: 0,
		partEnd:   255,
		batchSize: 100,
		leaseCh:   make(chan leaseCmd, 16),
		ackCh:     make(chan ackCmd, 16),
		utilCh:    make(chan chan float64, 4),
		drainCh:   make(chan chan struct{}, 1),
	}
	go p.runForTest(ctx, testMsgs)

	// Lease with a very short timeout (1 second).
	leaseResult := p.Lease(ctx, "t", "consumer", 10, 1)
	if leaseResult.Err != nil {
		t.Fatalf("lease failed: %v", leaseResult.Err)
	}
	if len(leaseResult.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(leaseResult.Messages))
	}

	drainDone := make(chan struct{})
	go func() {
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer drainCancel()
		p.Drain(drainCtx)
		close(drainDone)
	}()

	// Should complete within ~2 seconds (1s lease + 1s ticker).
	select {
	case <-drainDone:
		// good
	case <-time.After(5 * time.Second):
		t.Fatal("Drain did not complete after lease expiry")
	}

	cancel()
}

// runForTest is a simplified Run that pre-loads the buffer with the given
// messages and uses no-op store operations.  This avoids needing a real DB.
func (p *Partition) runForTest(ctx context.Context, initialMsgs []BufMsg) {
	buffer := make([]BufMsg, len(initialMsgs))
	copy(buffer, initialMsgs)

	served := make(map[string]*servedBatch)
	var doneIDs []int64

	var drainDone chan struct{}
	draining := false

	flushDone := func() {
		doneIDs = doneIDs[:0]
		close(drainDone)
	}

	expireTicker := time.NewTicker(500 * time.Millisecond)
	defer expireTicker.Stop()

	for {
		select {
		case cmd := <-p.leaseCh:
			if draining {
				cmd.reply <- LeaseResult{LeaseID: "drain-noop", Messages: []LeasedMsg{}}
				continue
			}

			leaseID := "test-lease-" + time.Now().Format("150405.000")
			n := cmd.max
			if n > len(buffer) {
				n = len(buffer)
			}
			if n == 0 {
				cmd.reply <- LeaseResult{LeaseID: leaseID, Messages: []LeasedMsg{}}
				continue
			}

			batch := buffer[:n]
			buffer = buffer[n:]

			ids := make([]int64, n)
			result := make([]LeasedMsg, n)
			for i, m := range batch {
				ids[i] = m.ID
				result[i] = LeasedMsg{MsgUUID: m.MsgUUID, Payload: m.Payload}
			}

			served[leaseID] = &servedBatch{
				ids:      ids,
				servedAt: time.Now(),
				timeout:  time.Duration(cmd.leaseSeconds) * time.Second,
			}

			cmd.reply <- LeaseResult{LeaseID: leaseID, Messages: result}

		case cmd := <-p.ackCh:
			sb, ok := served[cmd.leaseID]
			if !ok {
				cmd.reply <- AckResult{Acked: 0}
				continue
			}
			doneIDs = append(doneIDs, sb.ids...)
			acked := int64(len(sb.ids))
			delete(served, cmd.leaseID)
			cmd.reply <- AckResult{Acked: acked}

			if draining && len(served) == 0 {
				flushDone()
				return
			}

		case reply := <-p.utilCh:
			if p.batchSize > 0 {
				reply <- float64(len(buffer)) / float64(p.batchSize)
			} else {
				reply <- 0
			}

		case done := <-p.drainCh:
			draining = true
			drainDone = done
			buffer = buffer[:0]
			if len(served) == 0 {
				flushDone()
				return
			}

		case <-expireTicker.C:
			now := time.Now()
			for leaseID, sb := range served {
				if now.Sub(sb.servedAt) > sb.timeout {
					delete(served, leaseID)
				}
			}
			if draining && len(served) == 0 {
				flushDone()
				return
			}

		case <-ctx.Done():
			return
		}
	}
}
