package mqwriter

import (
	"context"
	"log/slog"
	"time"
)

// publishItem is a single publish request from the HTTP handler to the flusher.
type publishItem struct {
	topic string
	msgs  []IngestMsg
	reply chan publishResult
}

// publishResult is returned to the handler after the flusher commits.
type publishResult struct {
	accepted   int
	duplicates int
	err        error
}

// WriteBuffer decouples incoming Publish HTTP requests from PostgreSQL writes
// by batching them in memory and flushing asynchronously.
//
// Owner-goroutine pattern: a single flusher goroutine owns the buffer slice.
// No mutexes.
type WriteBuffer struct {
	inCh    chan publishItem
	store   *Store
	maxSize int
	flush   time.Duration
}

// NewWriteBuffer creates a WriteBuffer.  Start the flusher via go buf.Run().
func NewWriteBuffer(store *Store, maxSize int, flushInterval time.Duration) *WriteBuffer {
	return &WriteBuffer{
		inCh:    make(chan publishItem, 256),
		store:   store,
		maxSize: maxSize,
		flush:   flushInterval,
	}
}

// Submit sends a publish request to the buffer and blocks until the batch
// containing it is flushed (or ctx expires).
func (wb *WriteBuffer) Submit(ctx context.Context, topic string, msgs []IngestMsg) (accepted, duplicates int, err error) {
	reply := make(chan publishResult, 1)
	item := publishItem{topic: topic, msgs: msgs, reply: reply}

	select {
	case wb.inCh <- item:
	case <-ctx.Done():
		return 0, 0, ctx.Err()
	}

	select {
	case res := <-reply:
		return res.accepted, res.duplicates, res.err
	case <-ctx.Done():
		return 0, 0, ctx.Err()
	}
}

// BufferUtilization returns a 0.0–1.0 ratio of pending channel items.
// Useful as a metric for autoscaling.
func (wb *WriteBuffer) BufferUtilization() float64 {
	return float64(len(wb.inCh)) / float64(cap(wb.inCh))
}

// Run is the flusher's main loop.  Blocks until inCh is closed.
func (wb *WriteBuffer) Run() {
	var pending []publishItem
	msgCount := 0
	timer := time.NewTimer(wb.flush)
	defer timer.Stop()

	doFlush := func() {
		if len(pending) == 0 {
			return
		}

		grouped := make(map[string][]publishItem)
		for i := range pending {
			grouped[pending[i].topic] = append(grouped[pending[i].topic], pending[i])
		}

		for topic, items := range grouped {
			var allMsgs []IngestMsg
			for _, it := range items {
				allMsgs = append(allMsgs, it.msgs...)
			}

			accepted, dupes, err := wb.flushWithRetry(topic, allMsgs)
			if err != nil {
				slog.Error("buffer flush failed", "topic", topic, "messages", len(allMsgs), "error", err)
			}
			wb.distributeResults(items, accepted, dupes, err)
		}

		pending = pending[:0]
		msgCount = 0

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wb.flush)
	}

	for {
		select {
		case item, ok := <-wb.inCh:
			if !ok {
				doFlush()
				return
			}
			pending = append(pending, item)
			msgCount += len(item.msgs)
			if msgCount >= wb.maxSize {
				doFlush()
			}
		case <-timer.C:
			doFlush()
			timer.Reset(wb.flush)
		}
	}
}

// Close signals the flusher to drain and stop.
func (wb *WriteBuffer) Close() {
	close(wb.inCh)
}

func (wb *WriteBuffer) flushWithRetry(topic string, msgs []IngestMsg) (accepted, duplicates int, err error) {
	const maxRetries = 3
	backoff := 50 * time.Millisecond

	for attempt := range maxRetries {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		accepted, duplicates, err = wb.store.Publish(ctx, topic, msgs)
		cancel()
		if err == nil {
			return accepted, duplicates, nil
		}
		slog.Warn("buffer flush retry", "topic", topic, "attempt", attempt+1, "error", err)
		time.Sleep(backoff)
		backoff *= 2
	}
	return 0, 0, err
}

func (wb *WriteBuffer) distributeResults(items []publishItem, totalAccepted, totalDupes int, flushErr error) {
	if flushErr != nil {
		for _, it := range items {
			it.reply <- publishResult{err: flushErr}
		}
		return
	}

	remaining := totalAccepted
	remainingDupes := totalDupes
	total := 0
	for _, it := range items {
		total += len(it.msgs)
	}

	for i, it := range items {
		if i == len(items)-1 {
			it.reply <- publishResult{accepted: remaining, duplicates: remainingDupes}
		} else {
			n := len(it.msgs)
			share, dupeShare := 0, 0
			if total > 0 {
				share = totalAccepted * n / total
				dupeShare = totalDupes * n / total
			}
			remaining -= share
			remainingDupes -= dupeShare
			it.reply <- publishResult{accepted: share, duplicates: dupeShare}
		}
	}
}
