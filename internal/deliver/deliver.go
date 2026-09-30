// Package deliver sends a schedule of events to the app one at a time,
// retrying failures the way Stripe does.
package deliver

import (
	"context"
	"fmt"
	"slices"

	"github.com/shauryamittal/hookfuzz/internal/event"
)

const (
	// MaxAttempts caps deliveries of one scheduled event that keeps failing.
	MaxAttempts = 3
	// RetryGap is how many deliveries later a failed one is retried.
	RetryGap = 3
)

// Sender delivers one event and reports the HTTP status.
type Sender interface {
	Send(ctx context.Context, ev event.Event) (int, error)
}

// Result summarizes one delivery run.
type Result struct {
	// Acknowledged holds events that got at least one 2xx, once each, in the
	// order they were first acknowledged.
	Acknowledged []event.Event
	Attempts     int
}

// Run delivers the schedule in order. A non-2xx response puts the event back
// in the queue RetryGap deliveries later, up to MaxAttempts in total. A
// transport error (no response at all) aborts the run.
func Run(ctx context.Context, s Sender, schedule []event.Event) (Result, error) {
	type item struct {
		ev      event.Event
		attempt int
	}
	queue := make([]item, len(schedule))
	for i, ev := range schedule {
		queue[i] = item{ev: ev, attempt: 1}
	}
	var res Result
	acked := map[string]bool{}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		status, err := s.Send(ctx, it.ev)
		if err != nil {
			return res, fmt.Errorf("delivering %s (%s): %w", it.ev.ID, it.ev.Type, err)
		}
		res.Attempts++
		if status >= 200 && status < 300 {
			if !acked[it.ev.ID] {
				acked[it.ev.ID] = true
				res.Acknowledged = append(res.Acknowledged, it.ev)
			}
			continue
		}
		if it.attempt < MaxAttempts {
			queue = slices.Insert(queue, min(RetryGap, len(queue)), item{ev: it.ev, attempt: it.attempt + 1})
		}
	}
	return res, nil
}
