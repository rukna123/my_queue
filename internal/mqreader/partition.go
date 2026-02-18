package mqreader

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// LeasedMsg is returned to clients from a lease call.
type LeasedMsg struct {
	MsgUUID string          `json:"msg_uuid"`
	Payload json.RawMessage `json:"payload"`
}

// LeaseResult is the response sent back through the reply channel.
type LeaseResult struct {
	LeaseID  string      `json:"lease_id"`
	Messages []LeasedMsg `json:"messages"`
	Err      error       `json:"-"`
}

// AckResult is the response sent back through the reply channel.
type AckResult struct {
	Acked int64 `json:"acked"`
	Err   error `json:"-"`
}

type leaseCmd struct {
	topic        string
	consumerID   string
	max          int
	leaseSeconds int
	reply        chan LeaseResult
}

type ackCmd struct {
	topic      string
	consumerID string
	leaseID    string
	reply      chan AckResult
}

// servedBatch tracks messages that have been given to a collector
// and are awaiting ack or expiry.
type servedBatch struct {
	ids      []int64
	servedAt time.Time
	timeout  time.Duration
}

// Partition is an in-memory buffer for a range of virtual partitions.
// Single owner goroutine, no mutexes.
type Partition struct {
	store     *Store
	partStart int
	partEnd   int
	batchSize int

	leaseCh chan leaseCmd
	ackCh   chan ackCmd
}

// NewPartition creates a Partition for the given virtual-partition range.
func NewPartition(store *Store, partStart, partEnd int, _ time.Duration, batchSize int) *Partition {
	return &Partition{
		store:     store,
		partStart: partStart,
		partEnd:   partEnd,
		batchSize: batchSize,
		leaseCh:   make(chan leaseCmd, 16),
		ackCh:     make(chan ackCmd, 16),
	}
}

// Lease sends a lease command to the owner goroutine and waits for the result.
func (p *Partition) Lease(ctx context.Context, topic, consumerID string, max, leaseSeconds int) LeaseResult {
	reply := make(chan LeaseResult, 1)
	cmd := leaseCmd{
		topic:        topic,
		consumerID:   consumerID,
		max:          max,
		leaseSeconds: leaseSeconds,
		reply:        reply,
	}

	select {
	case p.leaseCh <- cmd:
	case <-ctx.Done():
		return LeaseResult{Err: ctx.Err()}
	}

	select {
	case res := <-reply:
		return res
	case <-ctx.Done():
		return LeaseResult{Err: ctx.Err()}
	}
}

// Ack sends an ack command to the owner goroutine and waits for the result.
func (p *Partition) Ack(ctx context.Context, topic, consumerID, leaseID string) AckResult {
	reply := make(chan AckResult, 1)
	cmd := ackCmd{
		topic:      topic,
		consumerID: consumerID,
		leaseID:    leaseID,
		reply:      reply,
	}

	select {
	case p.ackCh <- cmd:
	case <-ctx.Done():
		return AckResult{Err: ctx.Err()}
	}

	select {
	case res := <-reply:
		return res
	case <-ctx.Done():
		return AckResult{Err: ctx.Err()}
	}
}

// Run is the owner goroutine.  It manages a buffer of messages loaded from
// the DB and processes lease/ack commands.  Blocks until ctx is cancelled.
//
// Lifecycle of a message:
//   - Exists in DB  →  loaded into buffer (ready to serve)
//   - Lease request →  popped from buffer, tracked in served map
//   - Ack received  →  marked done, ID queued for batch DELETE
//   - Lease expired →  dropped from served map; row stays in DB,
//     will be re-loaded on next refill
//   - Batch complete (buffer empty + no outstanding served) →
//     DELETE done IDs from DB, refill buffer
func (p *Partition) Run(ctx context.Context) {
	// buffer holds messages ready to be served (FIFO order).
	var buffer []BufMsg

	// served tracks messages given to collectors, keyed by lease_id.
	served := make(map[string]*servedBatch)

	// doneIDs accumulates DB row IDs to batch-delete.
	var doneIDs []int64

	// refill loads a batch from DB into the buffer, skipping IDs
	// currently in the served map.
	servedIDs := func() map[int64]bool {
		m := make(map[int64]bool)
		for _, sb := range served {
			for _, id := range sb.ids {
				m[id] = true
			}
		}
		return m
	}

	refill := func() {
		// Flush completed deletes before refilling.
		if len(doneIDs) > 0 {
			deleted, err := p.store.DeleteByIDs(ctx, doneIDs)
			if err != nil {
				slog.Error("batch delete failed", "error", err)
			} else {
				slog.Info("batch deleted from DB", "deleted", deleted)
			}
			doneIDs = doneIDs[:0]
		}

		msgs, err := p.store.FetchBatch(ctx, p.partStart, p.partEnd, p.batchSize)
		if err != nil {
			slog.Error("fetch batch failed",
				"part_start", p.partStart,
				"part_end", p.partEnd,
				"error", err,
			)
			return
		}

		// Skip messages that are currently outstanding (served but not acked/expired).
		skip := servedIDs()
		for _, m := range msgs {
			if !skip[m.ID] {
				buffer = append(buffer, m)
			}
		}

		if len(buffer) > 0 {
			slog.Info("buffer refilled",
				"loaded", len(buffer),
				"skipped_served", len(skip),
			)
		}
	}

	// Initial fill.
	refill()
	slog.Info("partition started",
		"part_start", p.partStart,
		"part_end", p.partEnd,
		"buffered", len(buffer),
	)

	expireTicker := time.NewTicker(1 * time.Second)
	defer expireTicker.Stop()

	for {
		select {
		case cmd := <-p.leaseCh:
			// If buffer is empty and nothing outstanding, try refilling now.
			if len(buffer) == 0 && len(served) == 0 {
				refill()
			}

			leaseID := uuid.New().String()

			// Pop up to cmd.max messages from the front of the buffer.
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

			slog.Info("served from buffer",
				"topic", cmd.topic,
				"consumer_id", cmd.consumerID,
				"lease_id", leaseID,
				"count", n,
				"buffer_remaining", len(buffer),
			)

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

			slog.Info("acked",
				"lease_id", cmd.leaseID,
				"consumer_id", cmd.consumerID,
				"acked", acked,
				"pending_delete", len(doneIDs),
			)

			// If batch is complete (nothing in buffer, nothing outstanding)
			// flush deletes now so next lease triggers a clean refill.
			if len(buffer) == 0 && len(served) == 0 && len(doneIDs) > 0 {
				deleted, err := p.store.DeleteByIDs(ctx, doneIDs)
				if err != nil {
					slog.Error("batch delete failed", "error", err)
				} else {
					slog.Info("batch deleted from DB", "deleted", deleted)
				}
				doneIDs = doneIDs[:0]
			}

			cmd.reply <- AckResult{Acked: acked}

		case <-expireTicker.C:
			now := time.Now()
			for leaseID, sb := range served {
				if now.Sub(sb.servedAt) > sb.timeout {
					slog.Warn("lease expired, dropping from memory (will be re-read from DB)",
						"lease_id", leaseID,
						"ids", sb.ids,
					)
					delete(served, leaseID)
				}
			}

		case <-ctx.Done():
			return
		}
	}
}
