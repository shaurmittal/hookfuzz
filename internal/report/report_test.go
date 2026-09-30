package report

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/shauryamittal/hookfuzz/internal/event"
	"github.com/shauryamittal/hookfuzz/internal/invariant"
	"github.com/shauryamittal/hookfuzz/internal/runner"
)

func ev(id, typ, obj string, created int64) event.Event {
	return event.Event{ID: id, Type: typ, Created: created, Data: event.Data{Object: map[string]any{"id": obj}}}
}

func TestFailureReport(t *testing.T) {
	dup := ev("evt_0003", "checkout.session.completed", "cs_0002", 12)
	f := &runner.Failure{
		Seed:      42,
		Invariant: "one_shipment_per_session",
		Schedule: []event.Event{
			ev("evt_0001", "payment_intent.succeeded", "pi_0001", 10), dup, dup,
			ev("evt_0004", "payment_intent.succeeded", "pi_0004", 14),
			ev("evt_0006", "checkout.session.completed", "cs_0005", 16),
		},
		Minimal: []event.Event{dup, dup},
		Violations: []invariant.Violation{{
			Invariant: "one_shipment_per_session",
			Message:   "session_id=cs_0002 appears 2 times in shipments (max 1)",
		}},
	}
	var b bytes.Buffer
	Failure(&b, f, "hookfuzz.yaml")
	want := `FAIL invariant "one_shipment_per_session" (seed 42)
  session_id=cs_0002 appears 2 times in shipments (max 1)
Minimal reproduction (2 of 5 deliveries):
  1. checkout.session.completed     cs_0002    evt_0003
  2. checkout.session.completed     cs_0002    evt_0003  <- duplicate
Replay: hookfuzz replay --seed 42 --config hookfuzz.yaml
`
	if b.String() != want {
		t.Fatalf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}

func TestFailureReportWithoutReplayedViolations(t *testing.T) {
	var b bytes.Buffer
	Failure(&b, &runner.Failure{Seed: 1, Invariant: "x"}, "hookfuzz.yaml")
	if !strings.Contains(b.String(), "may be nondeterministic") {
		t.Fatalf("got:\n%s", b.String())
	}
}

func TestAnnotate(t *testing.T) {
	older := ev("evt_1", "customer.subscription.created", "sub_1", 10)
	newer := ev("evt_2", "customer.subscription.updated", "sub_1", 20)
	got := Annotate([]event.Event{newer, older, newer, older})
	want := []string{"", "out of order (older than #1)", "duplicate", "duplicate, out of order (older than #1)"}
	if !slices.Equal(got, want) {
		t.Fatalf("Annotate = %q, want %q", got, want)
	}
}

func TestPass(t *testing.T) {
	var b bytes.Buffer
	Pass(&b, 1, 100, 4)
	if got, want := b.String(), "PASS 100 runs (seeds 1..100), 4 invariants held\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	b.Reset()
	Pass(&b, 7, 1, 4)
	if got, want := b.String(), "PASS seed 7, 4 invariants held\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFailureReportSaysWhenShrinkingStoppedEarly(t *testing.T) {
	var b bytes.Buffer
	Failure(&b, &runner.Failure{
		Seed: 3, Invariant: "x", Incomplete: "interrupted",
		Violations: []invariant.Violation{{Invariant: "x", Message: "boom"}},
	}, "hookfuzz.yaml")
	if !strings.Contains(b.String(), "shrinking stopped early (interrupted)") {
		t.Fatalf("got:\n%s", b.String())
	}
}
