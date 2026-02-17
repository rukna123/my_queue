package mqreader

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// LeasedMsg is returned to clients from a lease call.
// It carries the mqwriter-generated msg_uuid so consumers can use it as
// the authoritative dedup key (e.g. for the telemetry table).
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

// leaseCmd is sent to the partition owner goroutine.
type leaseCmd struct {
	topic        string
	consumerID   string
	max          int
	leaseSeconds int
	reply        chan LeaseResult
}

// ackCmd is sent to the partition owner goroutine.
type ackCmd struct {
	topic      string
	consumerID string
	leaseID    string
	reply      chan AckResult
}

// Partition is an in-memory store for a range of virtual partitions.
// It uses the owner-goroutine pattern: a single goroutine (Run) owns all
// state and processes commands sequentially.  No mutexes.
type Partition struct {
	store        *Store
	partStart    int
	partEnd      int
	pollInterval time.Duration
	pollBatch    int

	leaseCh chan leaseCmd
	ackCh   chan ackCmd
}

// NewPartition creates a Partition for the given virtual-partition range.
func NewPartition(store *Store, partStart, partEnd int, pollInterval time.Duration, pollBatch int) *Partition {
	return &Partition{
		store:        store,
		partStart:    partStart,
		partEnd:      partEnd,
		pollInterval: pollInterval,
		pollBatch:    pollBatch,
		leaseCh:      make(chan leaseCmd, 16),
		ackCh:        make(chan ackCmd, 16),
	}
}

// Lease sends a lease command to the partition goroutine and waits for the result.
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

// Ack sends an ack command to the partition goroutine and waits for the result.
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

// Run is the partition's owner goroutine.  It polls the DB for new rows and
// processes lease/ack commands.  Blocks until ctx is cancelled.
func (p *Partition) Run(ctx context.Context) {
	msgs := make(map[int64]*MemMsg)
	var lastID int64

	doPoll := func() {
		newMsgs, maxID, err := p.store.PollReady(ctx, p.partStart, p.partEnd, lastID, p.pollBatch)
		if err != nil {
			slog.Error("partition poll failed",
				"part_start", p.partStart,
				"part_end", p.partEnd,
				"error", err,
			)
			return
		}
		for _, m := range newMsgs {
			msgs[m.ID] = m
		}
		if maxID > lastID {
			lastID = maxID
		}
		if len(newMsgs) > 0 {
			slog.Debug("partition polled",
				"part_start", p.partStart,
				"new_msgs", len(newMsgs),
				"total_in_mem", len(msgs),
			)
		}
	}

	doLease := func(cmd leaseCmd) {
		leaseID := uuid.New().String()
		now := time.Now()
		leaseUntil := now.Add(time.Duration(cmd.leaseSeconds) * time.Second)

		// Select up to max ready messages for this topic.
		var candidates []*MemMsg
		for _, m := range msgs {
			if m.Topic == cmd.topic && m.State == "ready" {
				candidates = append(candidates, m)
				if len(candidates) >= cmd.max {
					break
				}
			}
		}

		if len(candidates) == 0 {
			cmd.reply <- LeaseResult{LeaseID: leaseID, Messages: []LeasedMsg{}}
			return
		}

		// Collect IDs for DB update.
		ids := make([]int64, len(candidates))
		for i, c := range candidates {
			ids[i] = c.ID
		}

		// Persist lease to DB.
		if err := p.store.LeaseByIDs(ctx, cmd.consumerID, leaseID, cmd.leaseSeconds, ids); err != nil {
			slog.Error("partition lease DB update failed", "error", err)
			cmd.reply <- LeaseResult{Err: err}
			return
		}

		// Update in-memory state and build response.
		result := make([]LeasedMsg, len(candidates))
		for i, c := range candidates {
			c.State = "in_flight"
			c.LeasedBy = cmd.consumerID
			c.LeaseID = leaseID
			c.LeaseUntil = leaseUntil
			c.Attempts++
			result[i] = LeasedMsg{MsgUUID: c.MsgUUID, Payload: c.Payload}
		}

		slog.Info("partition leased",
			"topic", cmd.topic,
			"consumer_id", cmd.consumerID,
			"lease_id", leaseID,
			"count", len(result),
		)

		cmd.reply <- LeaseResult{LeaseID: leaseID, Messages: result}
	}

	doAck := func(cmd ackCmd) {
		// Delete from DB.
		acked, err := p.store.Ack(ctx, cmd.topic, cmd.consumerID, cmd.leaseID, p.partStart, p.partEnd)
		if err != nil {
			slog.Error("partition ack DB delete failed", "error", err)
			cmd.reply <- AckResult{Err: err}
			return
		}

		// Remove from memory.
		for id, m := range msgs {
			if m.Topic == cmd.topic && m.LeasedBy == cmd.consumerID && m.LeaseID == cmd.leaseID {
				delete(msgs, id)
			}
		}

		slog.Info("partition acked",
			"topic", cmd.topic,
			"consumer_id", cmd.consumerID,
			"lease_id", cmd.leaseID,
			"acked", acked,
		)

		cmd.reply <- AckResult{Acked: acked}
	}

	// Initial poll to warm the partition.
	doPoll()
	slog.Info("partition started",
		"part_start", p.partStart,
		"part_end", p.partEnd,
		"in_memory", len(msgs),
	)

	pollTicker := time.NewTicker(p.pollInterval)
	defer pollTicker.Stop()

	for {
		select {
		case cmd := <-p.leaseCh:
			doLease(cmd)
		case cmd := <-p.ackCh:
			doAck(cmd)
		case <-pollTicker.C:
			doPoll()
			// Also expire stale in-flight messages.
			now := time.Now()
			for _, m := range msgs {
				if m.State == "in_flight" && m.LeaseUntil.Before(now) {
					m.State = "ready"
					m.LeasedBy = ""
					m.LeaseID = ""
					m.LeaseUntil = time.Time{}
				}
			}
		case <-ctx.Done():
			return
		}
	}
}
