package collector_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prompted/prompted/internal/collector"
	"github.com/prompted/prompted/internal/config"
)

// ---------------------------------------------------------------------------
// Mock MQ client
// ---------------------------------------------------------------------------

type mockMQ struct {
	leaseResp *collector.LeaseResponse
	leaseErr  error
	ackCalls  atomic.Int64
	ackErr    error
}

func (m *mockMQ) Lease(_ context.Context, _, _ string, _, _ int) (*collector.LeaseResponse, error) {
	if m.leaseErr != nil {
		return nil, m.leaseErr
	}
	return m.leaseResp, nil
}

func (m *mockMQ) Ack(_ context.Context, _, _, _ string) (*collector.AckResponse, error) {
	m.ackCalls.Add(1)
	if m.ackErr != nil {
		return nil, m.ackErr
	}
	return &collector.AckResponse{Acked: 1}, nil
}

// ---------------------------------------------------------------------------
// Mock persister
// ---------------------------------------------------------------------------

type mockPersister struct {
	persistErr error
	persisted  int
	duplicates int
}

func (m *mockPersister) PersistBatch(_ context.Context, _ []collector.TelemetryRow) (int, int, error) {
	if m.persistErr != nil {
		return 0, 0, m.persistErr
	}
	return m.persisted, m.duplicates, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func testCfg() config.Collector {
	return config.Collector{
		CollectorID:    "test-collector",
		MQBaseURL:      "http://mock",
		LeaseMax:       10,
		LeaseSeconds:   30,
		PollIntervalMS: 50,
	}
}

func sampleMessages() []json.RawMessage {
	row := collector.TelemetryRow{
		UUID:       "550e8400-e29b-41d4-a716-446655440001",
		GPUID:      "GPU-test",
		MetricName: "gpu_utilization",
		Timestamp:  time.Now().UTC(),
		Value:      85.0,
	}
	b, _ := json.Marshal(row)
	return []json.RawMessage{b}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCollector_NoAckOnPersistFailure(t *testing.T) {
	mq := &mockMQ{
		leaseResp: &collector.LeaseResponse{
			LeaseID:  "lease-abc",
			Messages: sampleMessages(),
		},
	}
	store := &mockPersister{
		persistErr: errors.New("simulated DB failure"),
	}

	cfg := testCfg()
	ctx, cancel := context.WithCancel(context.Background())

	go collector.Run(ctx, cfg, store, mq)

	// Wait for at least one poll cycle.
	time.Sleep(200 * time.Millisecond)
	cancel()

	if mq.ackCalls.Load() != 0 {
		t.Errorf("expected 0 ack calls when persist fails, got %d", mq.ackCalls.Load())
	}
}

func TestCollector_AcksOnSuccess(t *testing.T) {
	mq := &mockMQ{
		leaseResp: &collector.LeaseResponse{
			LeaseID:  "lease-def",
			Messages: sampleMessages(),
		},
	}
	store := &mockPersister{
		persisted:  1,
		duplicates: 0,
	}

	cfg := testCfg()
	ctx, cancel := context.WithCancel(context.Background())

	go collector.Run(ctx, cfg, store, mq)

	time.Sleep(200 * time.Millisecond)
	cancel()

	if mq.ackCalls.Load() == 0 {
		t.Error("expected at least 1 ack call on successful persist, got 0")
	}
}

func TestCollector_NoAckWhenNoMessages(t *testing.T) {
	mq := &mockMQ{
		leaseResp: &collector.LeaseResponse{
			LeaseID:  "lease-empty",
			Messages: []json.RawMessage{},
		},
	}
	store := &mockPersister{}

	cfg := testCfg()
	ctx, cancel := context.WithCancel(context.Background())

	go collector.Run(ctx, cfg, store, mq)

	time.Sleep(200 * time.Millisecond)
	cancel()

	if mq.ackCalls.Load() != 0 {
		t.Errorf("expected 0 ack calls when no messages, got %d", mq.ackCalls.Load())
	}
}
