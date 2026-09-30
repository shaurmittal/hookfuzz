package fault

import (
	"fmt"
	"slices"
	"testing"

	"github.com/shaurmittal/hookfuzz/internal/event"
)

func testEvents(n int) []event.Event {
	evs := make([]event.Event, n)
	for i := range evs {
		evs[i] = event.Event{
			ID: fmt.Sprintf("evt_%d", i), Type: "test", Created: int64(i),
			Data: event.Data{Object: map[string]any{"id": fmt.Sprintf("obj_%d", i)}},
		}
	}
	return evs
}

func ids(evs []event.Event) []string {
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = ev.ID
	}
	return out
}

func counts(evs []event.Event) map[string]int {
	m := map[string]int{}
	for _, ev := range evs {
		m[ev.ID]++
	}
	return m
}

var aggressive = Config{Duplicate: 0.3, ReorderWindow: 4, Delay: 0.3, TimeoutRetry: 0.3}

func TestNoFaultsKeepsOrder(t *testing.T) {
	in := testEvents(20)
	if got := ids(Plan(in, Config{}, 1)); !slices.Equal(got, ids(in)) {
		t.Fatalf("Plan with no faults = %v, want %v", got, ids(in))
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	in := testEvents(40)
	a, b := ids(Plan(in, aggressive, 1)), ids(Plan(in, aggressive, 1))
	if !slices.Equal(a, b) {
		t.Fatal("same seed produced different schedules")
	}
	if slices.Equal(a, ids(Plan(in, aggressive, 2))) {
		t.Fatal("different seeds produced identical schedules")
	}
}

func TestPlanNeverDropsEvents(t *testing.T) {
	in := testEvents(30)
	for seed := int64(1); seed <= 200; seed++ {
		c := counts(Plan(in, aggressive, seed))
		for _, ev := range in {
			if c[ev.ID] < 1 {
				t.Fatalf("seed %d: %s was never delivered", seed, ev.ID)
			}
		}
		if len(c) != len(in) {
			t.Fatalf("seed %d: schedule has %d distinct IDs, want %d", seed, len(c), len(in))
		}
	}
}

func TestDuplicateAlwaysDeliversTwiceInARow(t *testing.T) {
	in := testEvents(10)
	out := Plan(in, Config{Duplicate: 1}, 1)
	if len(out) != 20 {
		t.Fatalf("len = %d, want 20", len(out))
	}
	for i, ev := range in {
		if out[2*i].ID != ev.ID || out[2*i+1].ID != ev.ID {
			t.Fatalf("positions %d,%d = %s,%s, want %s twice", 2*i, 2*i+1, out[2*i].ID, out[2*i+1].ID, ev.ID)
		}
	}
}

func TestTimeoutRetryRedeliversLater(t *testing.T) {
	in := testEvents(10)
	out := Plan(in, Config{TimeoutRetry: 1}, 1)
	for id, n := range counts(out) {
		if n != 2 {
			t.Fatalf("%s delivered %d times, want 2", id, n)
		}
	}
	for _, ev := range in {
		first := slices.IndexFunc(out, func(e event.Event) bool { return e.ID == ev.ID })
		last := lastIndex(out, ev.ID)
		if last <= first {
			t.Fatalf("%s retry at %d is not after original at %d", ev.ID, last, first)
		}
	}
}

func TestDelayAndReorderPreserveTheMultiset(t *testing.T) {
	in := testEvents(25)
	for seed := int64(1); seed <= 50; seed++ {
		out := Plan(in, Config{Delay: 0.5, ReorderWindow: 4}, seed)
		if len(out) != len(in) {
			t.Fatalf("seed %d: len = %d, want %d", seed, len(out), len(in))
		}
		for id, n := range counts(out) {
			if n != 1 {
				t.Fatalf("seed %d: %s appears %d times", seed, id, n)
			}
		}
	}
}

func TestReorderStaysInsideItsWindow(t *testing.T) {
	in := testEvents(20)
	for seed := int64(1); seed <= 50; seed++ {
		for i, ev := range Plan(in, Config{ReorderWindow: 3}, seed) {
			if int(ev.Created)/3 != i/3 { // Created doubles as the original index here.
				t.Fatalf("seed %d: %s moved from window %d to %d", seed, ev.ID, ev.Created/3, i/3)
			}
		}
	}
}

func TestReorderWindowOneIsANoop(t *testing.T) {
	in := testEvents(10)
	if got := ids(Plan(in, Config{ReorderWindow: 1}, 3)); !slices.Equal(got, ids(in)) {
		t.Fatalf("window 1 reordered: %v", got)
	}
}

func lastIndex(evs []event.Event, id string) int {
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].ID == id {
			return i
		}
	}
	return -1
}
