package invariant

import (
	"strings"
	"testing"

	"github.com/shauryamittal/hookfuzz/internal/event"
	"github.com/shauryamittal/hookfuzz/internal/target"
)

var (
	oneShipment = Spec{Name: "one_shipment", Kind: KindMaxCountPerKey, Collection: "shipments", Key: "session_id", Max: 1}
	refundLE    = Spec{Name: "refund_le_captured", Kind: KindFieldLE, Collection: "charges", Left: "refunded", Right: "captured"}
	subLatest   = Spec{
		Name: "sub_latest", Kind: KindMatchesLatestEvent, Collection: "subscriptions", Field: "status",
		EventTypes: []string{"customer.subscription.created", "customer.subscription.updated"}, EventField: "status",
	}
	refundLatest = Spec{
		Name: "refund_latest", Kind: KindMatchesLatestEvent, Collection: "charges", Field: "refunded",
		EventField: "amount_refunded",
	}
)

func ev(id, typ string, created int64, obj map[string]any) event.Event {
	return event.Event{ID: id, Type: typ, Created: created, Data: event.Data{Object: obj}}
}

func mustOne(t *testing.T, vs []Violation, name, substr string) {
	t.Helper()
	if len(vs) != 1 {
		t.Fatalf("got %d violations %v, want 1", len(vs), vs)
	}
	if vs[0].Invariant != name || !strings.Contains(vs[0].Message, substr) {
		t.Fatalf("violation = %+v, want invariant %q with message containing %q", vs[0], name, substr)
	}
}

func TestMaxCountPerKey(t *testing.T) {
	bad := target.State{"shipments": {{"session_id": "cs_1"}, {"session_id": "cs_2"}, {"session_id": "cs_1"}}}
	mustOne(t, oneShipment.Check(bad, nil), "one_shipment", "session_id=cs_1 appears 2 times in shipments (max 1)")

	good := target.State{"shipments": {{"session_id": "cs_1"}, {"session_id": "cs_2"}}}
	if vs := oneShipment.Check(good, nil); len(vs) != 0 {
		t.Fatalf("unexpected violations %v", vs)
	}
}

func TestFieldLE(t *testing.T) {
	state := target.State{"charges": {
		{"id": "ch_1", "refunded": 500.0, "captured": 1000.0},
		{"id": "ch_2", "refunded": 1500.0, "captured": 1000.0},
	}}
	mustOne(t, refundLE.Check(state, nil), "refund_le_captured", "charges[ch_2]: refunded = 1500 is greater than captured = 1000")
}

func TestMatchesLatestEventUsesCreatedNotArrivalOrder(t *testing.T) {
	// Arrival order: the newer update first, then the older creation.
	acked := []event.Event{
		ev("evt_2", "customer.subscription.updated", 30, map[string]any{"id": "sub_1", "status": "past_due"}),
		ev("evt_1", "customer.subscription.created", 10, map[string]any{"id": "sub_1", "status": "active"}),
	}
	stale := target.State{"subscriptions": {{"id": "sub_1", "status": "active"}}}
	mustOne(t, subLatest.Check(stale, acked), "sub_latest",
		"subscriptions[sub_1]: status = active, but the latest event evt_2 (customer.subscription.updated, created 30) says past_due")

	fresh := target.State{"subscriptions": {{"id": "sub_1", "status": "past_due"}}}
	if vs := subLatest.Check(fresh, acked); len(vs) != 0 {
		t.Fatalf("unexpected violations %v", vs)
	}
}

func TestMatchesLatestEventIgnoresOtherTypesAndUnseenObjects(t *testing.T) {
	acked := []event.Event{
		ev("evt_1", "customer.subscription.created", 10, map[string]any{"id": "sub_1", "status": "active"}),
		ev("evt_9", "invoice.paid", 99, map[string]any{"id": "sub_1", "status": "paid"}),
	}
	state := target.State{"subscriptions": {
		{"id": "sub_1", "status": "active"},
		{"id": "sub_2", "status": "whatever"}, // no events for sub_2: nothing to compare
	}}
	if vs := subLatest.Check(state, acked); len(vs) != 0 {
		t.Fatalf("unexpected violations %v", vs)
	}
}

func TestLargeIntegersCompareEqual(t *testing.T) {
	// JSON gives the state float64(1e6); the event holds int64(1000000).
	acked := []event.Event{ev("evt_1", "charge.refunded", 10, map[string]any{"id": "ch_1", "amount_refunded": int64(1_000_000)})}
	state := target.State{"charges": {{"id": "ch_1", "refunded": 1_000_000.0}}}
	if vs := refundLatest.Check(state, acked); len(vs) != 0 {
		t.Fatalf("false positive on large integer: %v", vs)
	}
}

func TestMisconfiguredFieldsAreViolations(t *testing.T) {
	acked := []event.Event{ev("evt_1", "customer.subscription.created", 10, map[string]any{"id": "sub_1", "status": "active"})}
	subs := target.State{"subscriptions": {{"id": "sub_1", "status": "active"}}}
	cases := []struct {
		name  string
		spec  Spec
		state target.State
		want  string
	}{
		{"missing collection", oneShipment, target.State{}, `app state has no "shipments" collection`},
		{"key typo", Spec{Name: "x", Kind: KindMaxCountPerKey, Collection: "shipments", Key: "sesion_id", Max: 1},
			target.State{"shipments": {{"session_id": "cs_1"}}}, `shipments[0] has no field "sesion_id"`},
		{"field_le typo", Spec{Name: "x", Kind: KindFieldLE, Collection: "charges", Left: "refund", Right: "captured"},
			target.State{"charges": {{"id": "ch_1", "refunded": 1.0, "captured": 2.0}}}, `must both be numbers`},
		{"state field typo", Spec{Name: "x", Kind: KindMatchesLatestEvent, Collection: "subscriptions", Field: "stat", EventField: "status"},
			subs, `subscriptions[sub_1] has no field "stat"`},
		{"event field typo", Spec{Name: "x", Kind: KindMatchesLatestEvent, Collection: "subscriptions", Field: "status", EventField: "stat"},
			subs, `has no data.object.stat`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			vs := c.spec.Check(c.state, acked)
			if len(vs) == 0 || !strings.Contains(vs[0].Message, c.want) {
				t.Fatalf("violations = %v, want one containing %q", vs, c.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	for _, good := range []Spec{oneShipment, refundLE, subLatest} {
		if err := good.Validate(); err != nil {
			t.Errorf("%s: unexpected error %v", good.Name, err)
		}
	}
	cases := []struct {
		spec Spec
		want string
	}{
		{Spec{Kind: KindFieldLE, Collection: "c", Left: "a", Right: "b"}, "missing a name"},
		{Spec{Name: "x", Kind: KindFieldLE, Left: "a", Right: "b"}, "collection is required"},
		{Spec{Name: "x", Kind: "exactly_once", Collection: "c"}, `unknown kind "exactly_once"`},
		{Spec{Name: "x", Kind: KindMaxCountPerKey, Collection: "c", Key: "k"}, "max must be at least 1"},
		{Spec{Name: "x", Kind: KindMaxCountPerKey, Collection: "c", Max: 1}, "key is required"},
		{Spec{Name: "x", Kind: KindFieldLE, Collection: "c", Left: "a"}, "left and right are required"},
		{Spec{Name: "x", Kind: KindMatchesLatestEvent, Collection: "c", Field: "f"}, "field and event_field are required"},
	}
	for _, c := range cases {
		err := c.spec.Validate()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Validate(%+v) = %v, want error containing %q", c.spec, err, c.want)
		}
	}
}

func TestCheckAllKeepsSpecOrder(t *testing.T) {
	state := target.State{
		"shipments": {{"session_id": "cs_1"}, {"session_id": "cs_1"}},
		"charges":   {{"id": "ch_1", "refunded": 2.0, "captured": 1.0}},
	}
	vs := CheckAll([]Spec{refundLE, oneShipment}, state, nil)
	if len(vs) != 2 || vs[0].Invariant != "refund_le_captured" || vs[1].Invariant != "one_shipment" {
		t.Fatalf("CheckAll = %v, want refund_le_captured then one_shipment", vs)
	}
}

func TestMatchesLatestEventFlagsObjectsMissingFromState(t *testing.T) {
	// The app acknowledged a refund for ch_9 but never stored the charge.
	acked := []event.Event{ev("evt_1", "charge.refunded", 10, map[string]any{"id": "ch_9", "amount_refunded": int64(500)})}
	state := target.State{"charges": {}}
	mustOne(t, refundLatest.Check(state, acked), "refund_latest", "ch_9 has acknowledged events but is absent from charges")
}
