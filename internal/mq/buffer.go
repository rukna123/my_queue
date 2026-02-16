package mq

import (
	"context"
	"log/slog"
	"time"
)

// publishItem is a single publish request sent from the HTTP handler to the
// WriteBuffer's flusher goroutine.
type publishItem struct {
	topic string
	msgs  []PublishMsg
	reply chan publishResult
}

// publishResult is returned to the handler after the flusher commits the batch.
type publishResult struct {
	accepted   int
	duplicates int
	err        error
}

// WriteBuffer decouples incoming Publish HTTP requests from direct PostgreSQL
// writes by collecting messages in an in-memory buffer and flushing them
// asynchronously in larger batches.
//
// It uses the owner-goroutine pattern (no mutexes): a single flusher
// goroutine owns the buffer slice and is the only reader/writer.
type WriteBuffer struct {
	inCh    chan publishItem
	store   *Store
	maxSize int
	flush   time.Duration
}

// NewWriteBuffer creates a WriteBuffer. Start its background flusher
// by calling Run in a goroutine.
func NewWriteBuffer(store *Store, maxSize int, flushInterval time.Duration) *WriteBuffer {
	return &WriteBuffer{
		inCh:    make(chan publishItem, 256),
		store:   store,
		maxSize: maxSize,
		flush:   flushInterval,
	}
}

// Submit sends a publish request to the buffer and blocks until the batch
// containing it is flushed to the database (or the context expires).
func (wb *WriteBuffer) Submit(ctx context.Context, topic string, msgs []PublishMsg) (accepted, duplicates int, err error) {
	reply := make(chan publishResult, 1)
	item := publishItem{
		topic: topic,
		msgs:  msgs,
		reply: reply,
	}

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

// Run is the flusher's main loop. It collects publishItems until the buffer
// reaches maxSize or the flush timer fires, then commits them all in one
// Store.Publish call.
//
// Run blocks until inCh is closed. Call Close to shut it down gracefully.
func (wb *WriteBuffer) Run() {
	var pending []publishItem
	msgCount := 0
	timer := time.NewTimer(wb.flush)
	defer timer.Stop()

	doFlush := func() {
		if len(pending) == 0 {
			return
		}

		// Group pending items by topic for efficient flushing.
		grouped := make(map[string][]publishItem)
		for i := range pending {
			grouped[pending[i].topic] = append(grouped[pending[i].topic], pending[i])
		}

		for topic, items := range grouped {
			// Merge all messages for this topic into a single slice.
			var allMsgs []PublishMsg
			for _, it := range items {
				allMsgs = append(allMsgs, it.msgs...)
			}

			accepted, dupes, err := wb.flushWithRetry(topic, allMsgs)
			if err != nil {
				slog.Error("buffer flush failed",
					"topic", topic,
					"messages", len(allMsgs),
					"error", err,
				)
			}

			// Distribute results back to waiting handlers.
			// Each handler gets a proportional share of accepted/duplicates.
			wb.distributeResults(items, accepted, dupes, err)
		}

		pending = pending[:0]
		msgCount = 0

		// Reset the timer for the next flush window.
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
				// Channel closed — drain remaining items and exit.
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

// Close signals the flusher to drain and stop. It blocks until Run returns.
func (wb *WriteBuffer) Close() {
	close(wb.inCh)
}

// flushWithRetry calls Store.Publish with a simple retry (up to 3 attempts
// with exponential backoff) to tolerate transient DB errors.
func (wb *WriteBuffer) flushWithRetry(topic string, msgs []PublishMsg) (accepted, duplicates int, err error) {
	const maxRetries = 3
	backoff := 50 * time.Millisecond

	for attempt := 0; attempt < maxRetries; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		accepted, duplicates, err = wb.store.Publish(ctx, topic, msgs)
		cancel()

		if err == nil {
			return accepted, duplicates, nil
		}

		slog.Warn("buffer flush retry",
			"topic", topic,
			"attempt", attempt+1,
			"error", err,
		)
		time.Sleep(backoff)
		backoff *= 2
	}
	return 0, 0, err
}

// distributeResults sends per-handler results back on their reply channels.
// When a single topic flush covers multiple handlers, each handler gets the
// counts for its own messages.
func (wb *WriteBuffer) distributeResults(items []publishItem, totalAccepted, totalDupes int, flushErr error) {
	if flushErr != nil {
		for _, it := range items {
			it.reply <- publishResult{err: flushErr}
		}
		return
	}

	// Distribute proportionally: iterate through items and assign
	// accepted/duplicate counts based on each item's message count.
	remaining := totalAccepted
	remainingDupes := totalDupes

	for i, it := range items {
		n := len(it.msgs)
		if i == len(items)-1 {
			// Last item gets whatever is left to avoid rounding issues.
			it.reply <- publishResult{accepted: remaining, duplicates: remainingDupes}
		} else {
			// Proportional share.
			share := 0
			dupeShare := 0
			total := 0
			for _, item := range items {
				total += len(item.msgs)
			}
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
