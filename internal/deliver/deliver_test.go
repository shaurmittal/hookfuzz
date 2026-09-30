package deliver

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/shaurmittal/hookfuzz/internal/event"
)

type fakeSender struct {
	statuses map[string][]int // per event ID, consumed in order; 200 once exhausted
	sent     []string
	err      error
}

func (f *fakeSender) Send(_ context.Context, ev event.Event) (int, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.sent = append(f.sent, ev.ID)
	if s := f.statuses[ev.ID]; len(s) > 0 {
		f.statuses[ev.ID] = s[1:]
		return s[0], nil
	}
	return 200, nil
}

func evs(ids ...string) []event.Event {
	out := make([]event.Event, len(ids))
	for i, id := range ids {
		out[i] = event.Event{ID: id}
	}
	return out
}

func ackedIDs(r Result) []string {
	var out []string
	for _, ev := range r.Acknowledged {
		out = append(out, ev.ID)
	}
	return out
}

func TestDeliversInOrder(t *testing.T) {
	f := &fakeSender{}
	res, err := Run(context.Background(), f, evs("a", "b", "c"))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"a", "b", "c"}; !slices.Equal(f.sent, want) || !slices.Equal(ackedIDs(res), want) {
		t.Fatalf("sent %v, acked %v; want %v for both", f.sent, ackedIDs(res), want)
	}
	if res.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", res.Attempts)
	}
}

func TestDuplicatesAreAcknowledgedOnce(t *testing.T) {
	f := &fakeSender{}
	res, _ := Run(context.Background(), f, evs("a", "a", "b"))
	if !slices.Equal(f.sent, []string{"a", "a", "b"}) {
		t.Errorf("sent %v", f.sent)
	}
	if !slices.Equal(ackedIDs(res), []string{"a", "b"}) {
		t.Errorf("acked %v, want [a b]", ackedIDs(res))
	}
}

func TestRetriesNon2xxRetryGapDeliveriesLater(t *testing.T) {
	f := &fakeSender{statuses: map[string][]int{"a": {500}}}
	res, _ := Run(context.Background(), f, evs("a", "b", "c", "d", "e"))
	if want := []string{"a", "b", "c", "d", "a", "e"}; !slices.Equal(f.sent, want) {
		t.Fatalf("sent %v, want %v", f.sent, want)
	}
	if want := []string{"b", "c", "d", "a", "e"}; !slices.Equal(ackedIDs(res), want) {
		t.Fatalf("acked %v, want %v", ackedIDs(res), want)
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	f := &fakeSender{statuses: map[string][]int{"a": {500, 500, 500, 500, 500}}}
	res, err := Run(context.Background(), f, evs("a", "b"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.Join(f.sent, ""), "a"); n != MaxAttempts {
		t.Fatalf("a attempted %d times, want %d", n, MaxAttempts)
	}
	if !slices.Equal(ackedIDs(res), []string{"b"}) {
		t.Fatalf("acked %v, want [b]", ackedIDs(res))
	}
}

func TestTransportErrorAbortsTheRun(t *testing.T) {
	boom := errors.New("connection refused")
	_, err := Run(context.Background(), &fakeSender{err: boom}, evs("evt_9"))
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "evt_9") {
		t.Fatalf("err = %v, want it to wrap the transport error and name evt_9", err)
	}
}
