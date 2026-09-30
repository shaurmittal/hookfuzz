package event

import (
	"encoding/json"
	"testing"
)

func TestSignMatchesStripeScheme(t *testing.T) {
	// Reference value computed independently with Node's crypto:
	// hex(HMAC-SHA256("whsec_test", "1700000000." + body)).
	got := Sign([]byte(`{"id":"evt_1"}`), "whsec_test", 1700000000)
	want := "t=1700000000,v1=c89214b5b5da833daed6f0b8c5bb6bd58cea9022bd80ccc78230f3942d632925"
	if got != want {
		t.Fatalf("Sign() = %q, want %q", got, want)
	}
}

func TestEventJSONIsStripeShaped(t *testing.T) {
	ev := Event{
		ID: "evt_1", Object: "event", Type: "charge.succeeded", Created: 1700000000,
		APIVersion: "2024-06-20",
		Data:       Data{Object: map[string]any{"id": "ch_1", "amount": int64(1000)}},
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "object", "type", "created", "api_version", "livemode", "data"} {
		if _, ok := m[k]; !ok {
			t.Errorf("JSON is missing %q: %s", k, b)
		}
	}
	obj := m["data"].(map[string]any)["object"].(map[string]any)
	if obj["id"] != "ch_1" {
		t.Errorf("data.object.id = %v, want ch_1", obj["id"])
	}
}

func TestObjectID(t *testing.T) {
	ev := Event{Data: Data{Object: map[string]any{"id": "sub_1"}}}
	if got := ev.ObjectID(); got != "sub_1" {
		t.Errorf("ObjectID() = %q, want sub_1", got)
	}
	if got := (Event{}).ObjectID(); got != "" {
		t.Errorf("ObjectID() of empty event = %q, want empty", got)
	}
}
