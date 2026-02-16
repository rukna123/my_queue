package mq_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/prompted/prompted/internal/mq"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

const defaultTestDSN = "postgres://prompted:prompted@localhost:5432/prompted?sslmode=disable"

// testDB returns a *sql.DB connected to a test Postgres instance.
// It ensures the queue_messages schema exists and truncates the table.
// If the database is unreachable the test is skipped.
func testDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = defaultTestDSN
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("skipping: cannot open db: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Skipf("skipping: postgres not reachable: %v", err)
	}

	// Ensure the schema exists (mirrors the migration).
	_, err = db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS queue_messages (
			id          BIGSERIAL       PRIMARY KEY,
			topic       TEXT            NOT NULL,
			msg_uuid    UUID            NOT NULL,
			payload     JSONB           NOT NULL,
			state       TEXT            NOT NULL DEFAULT 'ready',
			leased_by   TEXT,
			lease_id    UUID,
			lease_until TIMESTAMPTZ,
			attempts    INT             NOT NULL DEFAULT 0,
			created_at  TIMESTAMPTZ     NOT NULL DEFAULT now(),
			updated_at  TIMESTAMPTZ     NOT NULL DEFAULT now()
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_qm_topic_msg_uuid
			ON queue_messages (topic, msg_uuid);
		CREATE INDEX IF NOT EXISTS idx_qm_topic_state_lease
			ON queue_messages (topic, state, lease_until);
		CREATE INDEX IF NOT EXISTS idx_qm_lease_id
			ON queue_messages (lease_id);
	`)
	if err != nil {
		t.Fatalf("ensure schema: %v", err)
	}

	_, _ = db.ExecContext(ctx, "TRUNCATE queue_messages")

	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "TRUNCATE queue_messages")
		db.Close()
	})

	return db
}

func makeMsg(payload string) mq.PublishMsg {
	return mq.PublishMsg{
		MsgUUID: uuid.New(),
		Payload: json.RawMessage(payload),
	}
}

func makeMsgs(n int) []mq.PublishMsg {
	msgs := make([]mq.PublishMsg, n)
	for i := range msgs {
		msgs[i] = makeMsg(fmt.Sprintf(`{"i":%d}`, i))
	}
	return msgs
}

// ---------------------------------------------------------------------------
// Publish tests
// ---------------------------------------------------------------------------

func TestPublish(t *testing.T) {
	db := testDB(t)
	store := mq.NewStore(db)
	ctx := context.Background()

	msgs := makeMsgs(3)
	accepted, dupes, err := store.Publish(ctx, "test", msgs)
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if accepted != 3 {
		t.Errorf("accepted = %d, want 3", accepted)
	}
	if dupes != 0 {
		t.Errorf("duplicates = %d, want 0", dupes)
	}
}

func TestPublishIdempotent(t *testing.T) {
	db := testDB(t)
	store := mq.NewStore(db)
	ctx := context.Background()

	msg := makeMsg(`{"key":"val"}`)
	msgs := []mq.PublishMsg{msg}

	accepted, _, err := store.Publish(ctx, "test", msgs)
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if accepted != 1 {
		t.Fatalf("first accepted = %d, want 1", accepted)
	}

	// Retry with the exact same msg_uuid
	accepted, dupes, err := store.Publish(ctx, "test", msgs)
	if err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if accepted != 0 {
		t.Errorf("second accepted = %d, want 0", accepted)
	}
	if dupes != 1 {
		t.Errorf("second duplicates = %d, want 1", dupes)
	}
}

func TestPublishMixedBatch(t *testing.T) {
	db := testDB(t)
	store := mq.NewStore(db)
	ctx := context.Background()

	existing := makeMsg(`{"existing":true}`)
	if _, _, err := store.Publish(ctx, "test", []mq.PublishMsg{existing}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	batch := []mq.PublishMsg{
		existing,                      // duplicate
		makeMsg(`{"new1":true}`),      // new
		makeMsg(`{"new2":true}`),      // new
	}

	accepted, dupes, err := store.Publish(ctx, "test", batch)
	if err != nil {
		t.Fatalf("publish mixed: %v", err)
	}
	if accepted != 2 {
		t.Errorf("accepted = %d, want 2", accepted)
	}
	if dupes != 1 {
		t.Errorf("duplicates = %d, want 1", dupes)
	}
}

// ---------------------------------------------------------------------------
// Lease tests
// ---------------------------------------------------------------------------

func TestLeaseBasic(t *testing.T) {
	db := testDB(t)
	store := mq.NewStore(db)
	ctx := context.Background()

	msgs := makeMsgs(5)
	if _, _, err := store.Publish(ctx, "test", msgs); err != nil {
		t.Fatalf("publish: %v", err)
	}

	leaseID, leased, err := store.Lease(ctx, "test", "c1", 10, 30)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if leaseID == uuid.Nil {
		t.Error("lease_id should not be nil")
	}
	if len(leased) != 5 {
		t.Errorf("leased = %d, want 5", len(leased))
	}
}

func TestLeaseRespectsMax(t *testing.T) {
	db := testDB(t)
	store := mq.NewStore(db)
	ctx := context.Background()

	if _, _, err := store.Publish(ctx, "test", makeMsgs(10)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	_, leased, err := store.Lease(ctx, "test", "c1", 3, 30)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if len(leased) != 3 {
		t.Errorf("leased = %d, want 3", len(leased))
	}
}

func TestLeaseEmpty(t *testing.T) {
	db := testDB(t)
	store := mq.NewStore(db)
	ctx := context.Background()

	leaseID, leased, err := store.Lease(ctx, "test", "c1", 10, 30)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if leaseID != uuid.Nil {
		t.Errorf("lease_id = %s, want Nil for empty lease", leaseID)
	}
	if len(leased) != 0 {
		t.Errorf("leased = %d, want 0", len(leased))
	}
}

func TestLeaseDoesNotDoubleDeliver(t *testing.T) {
	db := testDB(t)
	store := mq.NewStore(db)
	ctx := context.Background()

	if _, _, err := store.Publish(ctx, "test", makeMsgs(5)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// First consumer takes all 5
	_, first, err := store.Lease(ctx, "test", "c1", 10, 300)
	if err != nil {
		t.Fatalf("first lease: %v", err)
	}
	if len(first) != 5 {
		t.Fatalf("first leased = %d, want 5", len(first))
	}

	// Second consumer should get nothing (leases not expired)
	_, second, err := store.Lease(ctx, "test", "c2", 10, 300)
	if err != nil {
		t.Fatalf("second lease: %v", err)
	}
	if len(second) != 0 {
		t.Errorf("second leased = %d, want 0", len(second))
	}
}

func TestLeaseExpiredRedelivery(t *testing.T) {
	db := testDB(t)
	store := mq.NewStore(db)
	ctx := context.Background()

	if _, _, err := store.Publish(ctx, "test", makeMsgs(3)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Lease with 300s timeout
	_, first, err := store.Lease(ctx, "test", "c1", 10, 300)
	if err != nil {
		t.Fatalf("first lease: %v", err)
	}
	if len(first) != 3 {
		t.Fatalf("first leased = %d, want 3", len(first))
	}

	// Simulate expiry by moving lease_until into the past
	_, err = db.ExecContext(ctx,
		"UPDATE queue_messages SET lease_until = now() - interval '1 second' WHERE topic = $1",
		"test",
	)
	if err != nil {
		t.Fatalf("expire leases: %v", err)
	}

	// Another consumer should now pick them up
	_, second, err := store.Lease(ctx, "test", "c2", 10, 300)
	if err != nil {
		t.Fatalf("second lease: %v", err)
	}
	if len(second) != 3 {
		t.Errorf("second leased = %d, want 3 (expired re-delivery)", len(second))
	}
}

func TestLeaseConcurrentNoOverlap(t *testing.T) {
	db := testDB(t)
	store := mq.NewStore(db)
	ctx := context.Background()

	const total = 20
	if _, _, err := store.Publish(ctx, "test", makeMsgs(total)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	type result struct {
		msgs []mq.LeasedMsg
		err  error
	}

	ch := make(chan result, 4)
	for i := range 4 {
		go func(id int) {
			_, msgs, err := store.Lease(ctx, "test", fmt.Sprintf("c%d", id), total, 300)
			ch <- result{msgs, err}
		}(i)
	}

	var totalLeased int
	for range 4 {
		r := <-ch
		if r.err != nil {
			t.Fatalf("concurrent lease: %v", r.err)
		}
		totalLeased += len(r.msgs)
	}

	if totalLeased != total {
		t.Errorf("total leased = %d, want %d (some overlap or loss)", totalLeased, total)
	}
}

// ---------------------------------------------------------------------------
// Ack tests
// ---------------------------------------------------------------------------

func TestAck(t *testing.T) {
	db := testDB(t)
	store := mq.NewStore(db)
	ctx := context.Background()

	if _, _, err := store.Publish(ctx, "test", makeMsgs(5)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	leaseID, leased, err := store.Lease(ctx, "test", "c1", 10, 300)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if len(leased) != 5 {
		t.Fatalf("leased = %d, want 5", len(leased))
	}

	acked, err := store.Ack(ctx, "test", "c1", leaseID)
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	if acked != 5 {
		t.Errorf("acked = %d, want 5", acked)
	}

	// After ack, a new lease should return nothing
	_, remaining, err := store.Lease(ctx, "test", "c2", 10, 300)
	if err != nil {
		t.Fatalf("post-ack lease: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("remaining = %d, want 0", len(remaining))
	}
}

func TestAckWrongConsumer(t *testing.T) {
	db := testDB(t)
	store := mq.NewStore(db)
	ctx := context.Background()

	if _, _, err := store.Publish(ctx, "test", makeMsgs(3)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	leaseID, _, err := store.Lease(ctx, "test", "c1", 10, 300)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}

	// Try to ack with wrong consumer_id
	acked, err := store.Ack(ctx, "test", "wrong-consumer", leaseID)
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	if acked != 0 {
		t.Errorf("acked = %d, want 0 (wrong consumer)", acked)
	}

	// Messages should still be leasable after expiry
	_, err = db.ExecContext(ctx,
		"UPDATE queue_messages SET lease_until = now() - interval '1 second' WHERE topic = $1",
		"test",
	)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}

	_, leased, err := store.Lease(ctx, "test", "c2", 10, 300)
	if err != nil {
		t.Fatalf("re-lease: %v", err)
	}
	if len(leased) != 3 {
		t.Errorf("re-leased = %d, want 3", len(leased))
	}
}

func TestAckWrongLeaseID(t *testing.T) {
	db := testDB(t)
	store := mq.NewStore(db)
	ctx := context.Background()

	if _, _, err := store.Publish(ctx, "test", makeMsgs(3)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	_, _, err := store.Lease(ctx, "test", "c1", 10, 300)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}

	// Ack with a random (wrong) lease_id
	acked, err := store.Ack(ctx, "test", "c1", uuid.New())
	if err != nil {
		t.Fatalf("ack: %v", err)
	}
	if acked != 0 {
		t.Errorf("acked = %d, want 0 (wrong lease_id)", acked)
	}
}

func TestLeaseIncrementsAttempts(t *testing.T) {
	db := testDB(t)
	store := mq.NewStore(db)
	ctx := context.Background()

	msgs := makeMsgs(1)
	if _, _, err := store.Publish(ctx, "test", msgs); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Lease, expire, lease again – attempts should increment
	for attempt := 1; attempt <= 3; attempt++ {
		_, leased, err := store.Lease(ctx, "test", "c1", 10, 300)
		if err != nil {
			t.Fatalf("lease attempt %d: %v", attempt, err)
		}
		if len(leased) != 1 {
			t.Fatalf("lease attempt %d: leased = %d, want 1", attempt, len(leased))
		}

		// Expire the lease
		_, err = db.ExecContext(ctx,
			"UPDATE queue_messages SET lease_until = now() - interval '1 second' WHERE topic = $1",
			"test",
		)
		if err != nil {
			t.Fatalf("expire: %v", err)
		}
	}

	// Verify attempts counter
	var attempts int
	err := db.QueryRowContext(ctx,
		"SELECT attempts FROM queue_messages WHERE topic = $1 AND msg_uuid = $2",
		"test", msgs[0].MsgUUID,
	).Scan(&attempts)
	if err != nil {
		t.Fatalf("query attempts: %v", err)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}
