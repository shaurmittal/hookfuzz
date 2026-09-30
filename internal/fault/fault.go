// Package fault turns an event history into a delivery schedule with
// at-least-once, out-of-order faults applied.
package fault

import (
	"math/rand"
	"slices"

	"github.com/shaurmittal/hookfuzz/internal/event"
)

// Config says how aggressively to perturb delivery. Probabilities are per event.
type Config struct {
	Duplicate     float64 `yaml:"duplicate"`      // delivered twice in a row
	ReorderWindow int     `yaml:"reorder_window"` // shuffle within windows of this size (<2 = off)
	Delay         float64 `yaml:"delay"`          // pushed 2-8 deliveries later
	TimeoutRetry  float64 `yaml:"timeout_retry"`  // processed, "timed out", redelivered later
}

// Plan turns events (in the order they happened) into a delivery schedule.
// The same events, config, and seed always give the same schedule. Faults
// only add or move deliveries, so every event is delivered at least once.
func Plan(events []event.Event, cfg Config, seed int64) []event.Event {
	rng := rand.New(rand.NewSource(seed))
	out := slices.Clone(events)
	out = duplicate(out, cfg.Duplicate, rng)
	out = timeoutRetry(out, cfg.TimeoutRetry, rng)
	out = delay(out, cfg.Delay, rng)
	out = reorder(out, cfg.ReorderWindow, rng)
	return out
}

func duplicate(in []event.Event, p float64, rng *rand.Rand) []event.Event {
	out := make([]event.Event, 0, len(in))
	for _, ev := range in {
		out = append(out, ev)
		if rng.Float64() < p {
			out = append(out, ev)
		}
	}
	return out
}

// timeoutRetry models Stripe timing out on a delivery the app did process,
// then retrying it a few deliveries later.
func timeoutRetry(in []event.Event, p float64, rng *rand.Rand) []event.Event {
	out := slices.Clone(in)
	// Walk backwards so inserting after i never shifts positions not yet visited.
	for i := len(in) - 1; i >= 0; i-- {
		if rng.Float64() < p {
			pos := min(i+2+rng.Intn(5), len(out))
			out = slices.Insert(out, pos, in[i])
		}
	}
	return out
}

func delay(in []event.Event, p float64, rng *rand.Rand) []event.Event {
	out := slices.Clone(in)
	for i := len(out) - 1; i >= 0; i-- {
		if rng.Float64() < p {
			ev := out[i]
			to := min(i+2+rng.Intn(7), len(out)-1)
			out = slices.Delete(out, i, i+1)
			out = slices.Insert(out, to, ev)
		}
	}
	return out
}

func reorder(in []event.Event, window int, rng *rand.Rand) []event.Event {
	out := slices.Clone(in)
	if window < 2 {
		return out
	}
	for start := 0; start < len(out); start += window {
		w := out[start:min(start+window, len(out))]
		rng.Shuffle(len(w), func(i, j int) { w[i], w[j] = w[j], w[i] })
	}
	return out
}
