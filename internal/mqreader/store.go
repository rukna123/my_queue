package mqreader

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Store provides DB operations for the reader service.
type Store struct {
	db *sql.DB
}

// NewStore wraps an existing *sql.DB pool.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// MemMsg is a message held in the reader's in-memory partition.
type MemMsg struct {
	ID         int64
	Topic      string
	MsgUUID    string
	Payload    json.RawMessage
	State      string // "ready" or "in_flight"
	LeasedBy   string
	LeaseID    string
	LeaseUntil time.Time
	Attempts   int
	CreatedAt  time.Time
}

// PollReady fetches new ready (or expired in-flight) rows for the partition
// range, starting after lastID.  Returns the rows and the maximum ID seen.
func (s *Store) PollReady(ctx context.Context, partStart, partEnd int, lastID int64, limit int) ([]*MemMsg, int64, error) {
	rows, err := s.db.QueryContext(ctx, queryPollReady, partStart, partEnd, lastID, limit)
	if err != nil {
		return nil, lastID, fmt.Errorf("poll ready: %w", err)
	}
	defer rows.Close()

	var msgs []*MemMsg
	maxID := lastID

	for rows.Next() {
		var m MemMsg
		var leasedBy, leaseID sql.NullString
		var leaseUntil sql.NullTime

		if err := rows.Scan(
			&m.ID, &m.Topic, &m.MsgUUID, &m.Payload, &m.State,
			&leasedBy, &leaseID, &leaseUntil,
			&m.Attempts, &m.CreatedAt,
		); err != nil {
			return nil, lastID, fmt.Errorf("scan: %w", err)
		}

		if leasedBy.Valid {
			m.LeasedBy = leasedBy.String
		}
		if leaseID.Valid {
			m.LeaseID = leaseID.String
		}
		if leaseUntil.Valid {
			m.LeaseUntil = leaseUntil.Time
		}

		// Reset expired in-flight messages to ready state in memory.
		if m.State == "in_flight" && m.LeaseUntil.Before(time.Now()) {
			m.State = "ready"
			m.LeasedBy = ""
			m.LeaseID = ""
			m.LeaseUntil = time.Time{}
		}

		msgs = append(msgs, &m)
		if m.ID > maxID {
			maxID = m.ID
		}
	}
	if err := rows.Err(); err != nil {
		return nil, lastID, fmt.Errorf("rows: %w", err)
	}
	return msgs, maxID, nil
}

// LeaseByIDs updates specific rows in the DB to in-flight state.
func (s *Store) LeaseByIDs(ctx context.Context, consumerID, leaseID string, leaseSeconds int, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}

	// Build parameterised IN-list: $4, $5, $6, …
	placeholders := make([]string, len(ids))
	args := make([]any, 0, 3+len(ids))
	args = append(args, consumerID, leaseID, leaseSeconds)
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+4)
		args = append(args, id)
	}

	query := `UPDATE queue_messages
SET state      = 'in_flight',
    leased_by  = $1,
    lease_id   = $2,
    lease_until = now() + $3::int * interval '1 second',
    attempts   = attempts + 1,
    updated_at = now()
WHERE id IN (` + strings.Join(placeholders, ", ") + `)`

	_, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("lease by IDs: %w", err)
	}
	return nil
}

// Ack deletes messages belonging to a lease batch within the partition range.
func (s *Store) Ack(ctx context.Context, topic, consumerID, leaseID string, partStart, partEnd int) (int64, error) {
	res, err := s.db.ExecContext(ctx, queryAck, topic, consumerID, leaseID, partStart, partEnd)
	if err != nil {
		return 0, fmt.Errorf("ack: %w", err)
	}
	return res.RowsAffected()
}
