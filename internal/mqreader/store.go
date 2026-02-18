package mqreader

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// Store provides DB operations for the reader service.
type Store struct {
	db *sql.DB
}

// NewStore wraps an existing *sql.DB pool.
func NewStore(db *sql.DB) *Store {
	return &Store{db: db}
}

// BufMsg is a message loaded from the DB into the in-memory buffer.
// No state tracking — existence in DB means unprocessed.
type BufMsg struct {
	ID      int64
	Topic   string
	MsgUUID string
	Payload json.RawMessage
}

// FetchBatch loads the oldest `limit` unprocessed messages for the
// given partition range.
func (s *Store) FetchBatch(ctx context.Context, partStart, partEnd, limit int) ([]BufMsg, error) {
	rows, err := s.db.QueryContext(ctx, queryFetchBatch, partStart, partEnd, limit)
	if err != nil {
		return nil, fmt.Errorf("fetch batch: %w", err)
	}
	defer rows.Close()

	var msgs []BufMsg
	for rows.Next() {
		var m BufMsg
		if err := rows.Scan(&m.ID, &m.Topic, &m.MsgUUID, &m.Payload); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return msgs, nil
}

// DeleteByIDs removes processed messages from the queue in a single call.
func (s *Store) DeleteByIDs(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	query := "DELETE FROM queue_messages WHERE id IN (" + strings.Join(placeholders, ", ") + ")"
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("delete by IDs: %w", err)
	}
	return res.RowsAffected()
}
