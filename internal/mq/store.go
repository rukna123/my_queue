package mq

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// Store manages queue_messages persistence.
// It is safe for concurrent use — all concurrency is handled by PostgreSQL
// (FOR UPDATE SKIP LOCKED), not by Go-level locks.
type Store struct {
	db *sql.DB
}

// NewStore wraps an existing *sql.DB connection pool.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// PublishMsg represents a single message in a publish request.
type PublishMsg struct {
	MsgUUID uuid.UUID       `json:"msg_uuid"`
	Payload json.RawMessage `json:"payload"`
}

// LeasedMsg is a message returned from a successful Lease call.
type LeasedMsg struct {
	MsgUUID uuid.UUID       `json:"msg_uuid"`
	Payload json.RawMessage `json:"payload"`
}

// ---------------------------------------------------------------------------
// Publish
// ---------------------------------------------------------------------------

// Publish inserts messages into queue_messages with state='ready'.
// Duplicates (same topic+msg_uuid) are silently skipped via ON CONFLICT DO NOTHING.
// The caller only gets a success response after the transaction commits.
func (s *Store) Publish(ctx context.Context, topic string, msgs []PublishMsg) (accepted, duplicates int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, queryPublishOne)
	if err != nil {
		return 0, 0, fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()

	for _, m := range msgs {
		var ok bool
		err := stmt.QueryRowContext(ctx, topic, m.MsgUUID, string(m.Payload)).Scan(&ok)
		switch {
		case err == sql.ErrNoRows:
			duplicates++
		case err != nil:
			return 0, 0, fmt.Errorf("insert msg %s: %w", m.MsgUUID, err)
		default:
			accepted++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit: %w", err)
	}
	return accepted, duplicates, nil
}

// ---------------------------------------------------------------------------
// Lease
// ---------------------------------------------------------------------------

// Lease atomically claims up to max messages that are ready or have an expired
// lease.  A new lease_id is generated per call and shared across all claimed
// messages.  Returns uuid.Nil and an empty slice when no messages are available.
func (s *Store) Lease(ctx context.Context, topic, consumerID string, max, leaseSeconds int) (uuid.UUID, []LeasedMsg, error) {
	leaseID := uuid.New()

	rows, err := s.db.QueryContext(ctx, queryLease,
		topic, max, consumerID, leaseID, leaseSeconds,
	)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("lease query: %w", err)
	}
	defer rows.Close()

	var msgs []LeasedMsg
	for rows.Next() {
		var m LeasedMsg
		if err := rows.Scan(&m.MsgUUID, &m.Payload); err != nil {
			return uuid.Nil, nil, fmt.Errorf("scan: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return uuid.Nil, nil, fmt.Errorf("rows: %w", err)
	}

	if len(msgs) == 0 {
		return uuid.Nil, msgs, nil
	}
	return leaseID, msgs, nil
}

// ---------------------------------------------------------------------------
// Ack
// ---------------------------------------------------------------------------

// Ack deletes all messages that belong to the given lease batch.  Only the
// consumer that originally leased the messages (leased_by check) can ack.
func (s *Store) Ack(ctx context.Context, topic, consumerID string, leaseID uuid.UUID) (int64, error) {
	res, err := s.db.ExecContext(ctx, queryAck, topic, consumerID, leaseID)
	if err != nil {
		return 0, fmt.Errorf("ack exec: %w", err)
	}
	return res.RowsAffected()
}
