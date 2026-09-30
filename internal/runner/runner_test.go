package runner

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/shauryamittal/hookfuzz/internal/config"
	"github.com/shauryamittal/hookfuzz/internal/event"
	"github.com/shauryamittal/hookfuzz/internal/fault"
	"github.com/shauryamittal/hookfuzz/internal/invariant"
	"github.com/shauryamittal/hookfuzz/internal/target"
)

// fakeShop ships an order for every checkout.session.completed it sees.
// With fixed set, it skips event IDs it has already processed.
type fakeShop struct {
	fixed     bool
	shipments []map[string]any
	seen      map[string]bool
	resetErr  error
}

func (f *fakeShop) Send(_ context.Context, ev event.Event) (int, error) {
	if f.fixed && f.seen[ev.ID] {
		return 200, nil
	}
	f.seen[ev.ID] = true
	if ev.Type == "checkout.session.completed" {
		f.shipments = append(f.shipments, map[string]any{"session_id": ev.ObjectID()})
	}
	return 200, nil
}

func (f *fakeShop) Reset(context.Context) error {
	f.shipments, f.seen = []map[string]any{}, map[string]bool{}
	return f.resetErr
}

func (f *fakeShop) State(context.Context) (target.State, error) {
	return target.State{"shipments": f.shipments}, nil
}

func testConfig() config.Config {
	return config.Config{
		Scenarios:    []string{"checkout"},
		ScenarioSize: 5,
		Faults:       fault.Config{Duplicate: 0.2, ReorderWindow: 3},
		Invariants: []invariant.Spec{{
			Name: "one_shipment_per_session", Kind: invariant.KindMaxCountPerKey,
			Collection: "shipments", Key: "session_id", Max: 1,
		}},
	}
}

func ids(evs []event.Event) []string {
	var out []string
	for _, ev := range evs {
		out = append(out, ev.ID)
	}
	return out
}

func TestRunFindsAndShrinksADuplicateShipment(t *testing.T) {
	r := &Runner{Config: testConfig(), Target: &fakeShop{}}
	f, n, err := r.Run(context.Background(), 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		t.Fatalf("no failure found in %d runs", n)
	}
	if f.Invariant != "one_shipment_per_session" || len(f.Violations) != 1 {
		t.Fatalf("failure = %+v", f)
	}
	if len(f.Minimal) != 2 || len(f.Schedule) <= 2 {
		t.Fatalf("minimal %d of %d deliveries, want 2 of more than 2", len(f.Minimal), len(f.Schedule))
	}
	a, b := f.Minimal[0], f.Minimal[1]
	if a.Type != "checkout.session.completed" || a.ID != b.ID {
		t.Fatalf("minimal = %v, want the same checkout.session.completed twice", ids(f.Minimal))
	}
}

func TestRunPassesOnAFixedShop(t *testing.T) {
	r := &Runner{Config: testConfig(), Target: &fakeShop{fixed: true}}
	f, n, err := r.Run(context.Background(), 1, 50)
	if err != nil || f != nil || n != 50 {
		t.Fatalf("Run = %+v, %d, %v; want no failure over 50 runs", f, n, err)
	}
}

func TestRunSeedIsReproducible(t *testing.T) {
	r := &Runner{Config: testConfig(), Target: &fakeShop{}}
	first, _, err := r.Run(context.Background(), 1, 50)
	if err != nil || first == nil {
		t.Fatalf("setup: %v, %v", first, err)
	}
	again, err := r.RunSeed(context.Background(), first.Seed)
	if err != nil || again == nil {
		t.Fatalf("replay of seed %d: %v, %v", first.Seed, again, err)
	}
	if !slices.Equal(ids(first.Schedule), ids(again.Schedule)) || !slices.Equal(ids(first.Minimal), ids(again.Minimal)) {
		t.Fatalf("seed %d replayed differently", first.Seed)
	}
}

func TestTargetErrorsAbortTheRun(t *testing.T) {
	boom := errors.New("reset failed")
	r := &Runner{Config: testConfig(), Target: &fakeShop{resetErr: boom}}
	if _, _, err := r.Run(context.Background(), 1, 5); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}

// rejectingShop answers every webhook with the same non-2xx status, like an
// app whose signing secret or webhook path is misconfigured.
type rejectingShop struct{ status int }

func (r rejectingShop) Send(context.Context, event.Event) (int, error) { return r.status, nil }
func (rejectingShop) Reset(context.Context) error                      { return nil }
func (rejectingShop) State(context.Context) (target.State, error) {
	return target.State{"shipments": {}}, nil
}

func TestAnAppThatAcknowledgesNothingIsASetupError(t *testing.T) {
	r := &Runner{Config: testConfig(), Target: rejectingShop{status: 400}}
	f, _, err := r.Run(context.Background(), 1, 5)
	if f != nil || err == nil {
		t.Fatalf("Run = %+v, %v; want a setup error, not a pass or a failure", f, err)
	}
	for _, want := range []string{"acknowledged none", "HTTP 400", "signing_secret", "webhook_url"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, missing %q", err, want)
		}
	}
}
