package mqwriter

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"

	"github.com/google/uuid"
)

// Store manages queue_messages writes.  It is safe for concurrent use because
// PostgreSQL handles all concurrency via transactions.
type Store struct {
	db              *sql.DB
	totalPartitions int
}

// NewStore wraps an existing *sql.DB connection pool.
func NewStore(db *sql.DB, totalPartitions int) *Store {
	return &Store{db: db, totalPartitions: totalPartitions}
}

// IngestMsg is a single message ready for DB insertion.
type IngestMsg struct {
	UUID      uuid.UUID
	GPUID     string
	Payload   json.RawMessage
	Partition int
}

// PartitionFor computes the virtual partition for a given GPU ID using FNV-1a.
func PartitionFor(gpuID string, totalPartitions int) int {
	h := fnv.New32a()
	h.Write([]byte(gpuID))
	return int(h.Sum32() % uint32(totalPartitions))
}

// Publish inserts messages into queue_messages with state='ready'.
// Duplicates (same topic+msg_uuid) are silently skipped via ON CONFLICT.
func (s *Store) Publish(ctx context.Context, topic string, msgs []IngestMsg) (accepted, duplicates int, err error) {
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
		err := stmt.QueryRowContext(ctx, topic, m.UUID, string(m.Payload), m.Partition).Scan(&ok)
		switch {
		case err == sql.ErrNoRows:
			duplicates++
		case err != nil:
			return 0, 0, fmt.Errorf("insert msg %s: %w", m.UUID, err)
		default:
			accepted++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("commit: %w", err)
	}
	return accepted, duplicates, nil
}
