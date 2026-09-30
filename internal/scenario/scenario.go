// Package scenario generates realistic, seeded Stripe event histories.
package scenario

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/shauryamittal/hookfuzz/internal/event"
)

const (
	// BaseTime is the created timestamp that generated histories start after.
	BaseTime int64 = 1_700_000_000
	// APIVersion is stamped on every generated event.
	APIVersion = "2024-06-20"
)

var generators = map[string]func(*gen) []event.Event{
	"checkout":     checkout,
	"refund":       refund,
	"subscription": subscription,
}

// EventTypes lists every event type the scenarios can generate.
func EventTypes() []string {
	return []string{
		"payment_intent.succeeded", "checkout.session.completed",
		"charge.succeeded", "charge.refunded",
		"customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted",
	}
}

// Names lists the known scenarios.
func Names() []string { return []string{"checkout", "refund", "subscription"} }

// Generate builds n independent objects for each named scenario and returns
// their events in the order they happened. The same arguments always return
// the same events.
func Generate(names []string, n int, seed int64) ([]event.Event, error) {
	g := &gen{rng: rand.New(rand.NewSource(seed)), now: BaseTime}
	var out []event.Event
	for _, name := range names {
		f, ok := generators[name]
		if !ok {
			return nil, fmt.Errorf("unknown scenario %q (known: %s)", name, strings.Join(Names(), ", "))
		}
		for i := 0; i < n; i++ {
			out = append(out, f(g)...)
		}
	}
	return out, nil
}

// gen hands out increasing timestamps and readable sequential IDs.
type gen struct {
	rng *rand.Rand
	now int64
	seq int
}

func (g *gen) id(prefix string) string {
	g.seq++
	return fmt.Sprintf("%s_%04d", prefix, g.seq)
}

func (g *gen) event(typ string, obj map[string]any) event.Event {
	g.now += 1 + int64(g.rng.Intn(5))
	return event.Event{
		ID:         g.id("evt"),
		Object:     "event",
		Type:       typ,
		Created:    g.now,
		APIVersion: APIVersion,
		Data:       event.Data{Object: obj},
	}
}

func (g *gen) amount() int64 { return int64(500 + g.rng.Intn(20)*100) }

func checkout(g *gen) []event.Event {
	amount := g.amount()
	pi, cs := g.id("pi"), g.id("cs")
	return []event.Event{
		g.event("payment_intent.succeeded", map[string]any{
			"id": pi, "object": "payment_intent", "amount": amount, "currency": "usd", "status": "succeeded",
		}),
		g.event("checkout.session.completed", map[string]any{
			"id": cs, "object": "checkout.session", "payment_intent": pi, "amount_total": amount,
			"currency": "usd", "payment_status": "paid", "status": "complete",
		}),
	}
}

func refund(g *gen) []event.Event {
	amount := g.amount()
	refunded := amount
	if g.rng.Intn(2) == 0 {
		refunded = amount / 2
	}
	ch := g.id("ch")
	charge := func(amountRefunded int64) map[string]any {
		return map[string]any{
			"id": ch, "object": "charge", "amount": amount, "amount_captured": amount,
			"amount_refunded": amountRefunded, "refunded": amountRefunded == amount,
			"currency": "usd", "status": "succeeded",
		}
	}
	return []event.Event{
		g.event("charge.succeeded", charge(0)),
		g.event("charge.refunded", charge(refunded)),
	}
}

func subscription(g *gen) []event.Event {
	sub, cus := g.id("sub"), g.id("cus")
	obj := func(status string) map[string]any {
		return map[string]any{"id": sub, "object": "subscription", "customer": cus, "status": status}
	}
	evs := []event.Event{g.event("customer.subscription.created", obj("active"))}
	status := "active"
	updates := 1 + g.rng.Intn(3)
	for i := 0; i < updates; i++ {
		if status == "active" {
			status = "past_due"
		} else {
			status = "active"
		}
		evs = append(evs, g.event("customer.subscription.updated", obj(status)))
	}
	if g.rng.Intn(2) == 0 {
		evs = append(evs, g.event("customer.subscription.deleted", obj("canceled")))
	}
	return evs
}
