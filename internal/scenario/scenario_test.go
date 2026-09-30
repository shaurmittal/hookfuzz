package scenario

import (
	"reflect"
	"strings"
	"testing"

	"github.com/shauryamittal/hookfuzz/internal/event"
)

func TestGenerateIsDeterministic(t *testing.T) {
	a, err := Generate(Names(), 5, 7)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Generate(Names(), 5, 7)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same seed produced different events")
	}
	c, _ := Generate(Names(), 5, 8)
	if reflect.DeepEqual(a, c) {
		t.Fatal("different seeds produced identical events")
	}
}

func TestCreatedStrictlyIncreasesAndIDsAreUnique(t *testing.T) {
	for seed := int64(1); seed <= 50; seed++ {
		evs, err := Generate(Names(), 5, seed)
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for i, ev := range evs {
			if i > 0 && ev.Created <= evs[i-1].Created {
				t.Fatalf("seed %d: event %d created %d is not after %d", seed, i, ev.Created, evs[i-1].Created)
			}
			if ids[ev.ID] {
				t.Fatalf("seed %d: duplicate event ID %s", seed, ev.ID)
			}
			ids[ev.ID] = true
			if ev.Object != "event" || ev.APIVersion != APIVersion {
				t.Fatalf("seed %d: event %s is not Stripe-shaped: %+v", seed, ev.ID, ev)
			}
		}
	}
}

func TestGenerateRejectsUnknownScenario(t *testing.T) {
	_, err := Generate([]string{"checkout", "payouts"}, 1, 1)
	if err == nil || !strings.Contains(err.Error(), `"payouts"`) {
		t.Fatalf("err = %v, want unknown scenario \"payouts\"", err)
	}
}

func TestCheckoutEvents(t *testing.T) {
	evs, _ := Generate([]string{"checkout"}, 3, 1)
	if len(evs) != 6 {
		t.Fatalf("got %d events, want 6", len(evs))
	}
	sessions := map[string]bool{}
	for _, ev := range evs {
		switch ev.Type {
		case "payment_intent.succeeded":
		case "checkout.session.completed":
			sessions[ev.ObjectID()] = true
		default:
			t.Errorf("unexpected event type %s", ev.Type)
		}
	}
	if len(sessions) != 3 {
		t.Errorf("got %d distinct sessions, want 3", len(sessions))
	}
}

func TestRefundEvents(t *testing.T) {
	evs, _ := Generate([]string{"refund"}, 4, 1)
	if len(evs) != 8 {
		t.Fatalf("got %d events, want 8", len(evs))
	}
	for i := 0; i < len(evs); i += 2 {
		succeeded, refunded := evs[i], evs[i+1]
		if succeeded.Type != "charge.succeeded" || refunded.Type != "charge.refunded" || succeeded.ObjectID() != refunded.ObjectID() {
			t.Fatalf("events %d,%d = %s %s / %s %s, want charge.succeeded then charge.refunded for one charge",
				i, i+1, succeeded.Type, succeeded.ObjectID(), refunded.Type, refunded.ObjectID())
		}
		amount := refunded.Data.Object["amount"].(int64)
		amt := refunded.Data.Object["amount_refunded"].(int64)
		if amt <= 0 || amt > amount {
			t.Errorf("amount_refunded %d outside (0, %d]", amt, amount)
		}
		if got := succeeded.Data.Object["amount_refunded"].(int64); got != 0 {
			t.Errorf("charge.succeeded amount_refunded = %d, want 0", got)
		}
	}
}

func TestSubscriptionEvents(t *testing.T) {
	for seed := int64(1); seed <= 20; seed++ {
		evs, _ := Generate([]string{"subscription"}, 3, seed)
		bySub := map[string][]event.Event{}
		for _, ev := range evs {
			bySub[ev.ObjectID()] = append(bySub[ev.ObjectID()], ev)
		}
		if len(bySub) != 3 {
			t.Fatalf("seed %d: got %d subscriptions, want 3", seed, len(bySub))
		}
		for id, list := range bySub {
			if list[0].Type != "customer.subscription.created" {
				t.Errorf("seed %d %s: first event is %s", seed, id, list[0].Type)
			}
			if len(list) < 2 {
				t.Errorf("seed %d %s: only %d events, want created + at least one update", seed, id, len(list))
			}
			for i := 1; i < len(list); i++ {
				// Consecutive statuses must differ, or a stale delivery would be invisible.
				if list[i].Data.Object["status"] == list[i-1].Data.Object["status"] {
					t.Errorf("seed %d %s: events %d and %d share status %v", seed, id, i-1, i, list[i].Data.Object["status"])
				}
			}
		}
	}
}
