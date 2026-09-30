package config

import (
	"strings"
	"testing"
)

const validYAML = `target:
  webhook_url: http://localhost:4242/webhook
  state_url: http://localhost:4242/__hookfuzz/state
  reset_url: http://localhost:4242/__hookfuzz/reset
  signing_secret: whsec_test
scenarios: [checkout]
faults:
  duplicate: 0.2
  reorder_window: 4
invariants:
  - name: one_shipment_per_session
    kind: max_count_per_key
    collection: shipments
    key: session_id
    max: 1
`

func TestParseValidConfigAppliesDefaults(t *testing.T) {
	c, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if c.ScenarioSize != DefaultScenarioSize {
		t.Errorf("ScenarioSize = %d, want default %d", c.ScenarioSize, DefaultScenarioSize)
	}
	if c.Faults.Duplicate != 0.2 || c.Faults.ReorderWindow != 4 {
		t.Errorf("Faults = %+v", c.Faults)
	}
	if c.Target.SigningSecret != "whsec_test" || len(c.Invariants) != 1 || c.Invariants[0].Max != 1 {
		t.Errorf("config = %+v", c)
	}
}

func TestParseRejectsBadConfigs(t *testing.T) {
	dupInvariant := validYAML + `  - name: one_shipment_per_session
    kind: max_count_per_key
    collection: shipments
    key: session_id
    max: 1
`
	cases := []struct {
		name, yaml, want string
	}{
		{"misspelled key", strings.Replace(validYAML, "duplicate:", "duplicte:", 1), "duplicte"},
		{"probability out of range", strings.Replace(validYAML, "duplicate: 0.2", "duplicate: 1.5", 1), "faults.duplicate must be between 0 and 1"},
		{"negative window", strings.Replace(validYAML, "reorder_window: 4", "reorder_window: -1", 1), "faults.reorder_window"},
		{"unknown scenario", strings.Replace(validYAML, "[checkout]", "[checkout, payouts]", 1), `unknown scenario "payouts"`},
		{"no scenarios", strings.Replace(validYAML, "[checkout]", "[]", 1), "scenarios"},
		{"unknown invariant kind", strings.Replace(validYAML, "kind: max_count_per_key", "kind: exactly_once", 1), "unknown kind"},
		{"not an http url", strings.Replace(validYAML, "webhook_url: http://localhost:4242/webhook", "webhook_url: localhost:4242/webhook", 1), "target.webhook_url must be an http(s) URL"},
		{"missing secret", strings.Replace(validYAML, "signing_secret: whsec_test", `signing_secret: ""`, 1), "target.signing_secret is required"},
		{"negative scenario size", validYAML + "scenario_size: -2\n", "scenario_size"},
		{"duplicate invariant names", dupInvariant, "duplicate invariant name"},
		{"empty file", "", "config is empty"},
		{"unknown event type", validYAML + `  - name: sub_latest
    kind: matches_latest_event
    collection: subscriptions
    field: status
    event_types: [customer.subscription.updatd]
    event_field: status
`, `unknown event type "customer.subscription.updatd"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.yaml))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Parse error = %v, want one containing %q", err, c.want)
			}
		})
	}
}

func TestLoadMissingFileNamesThePath(t *testing.T) {
	_, err := Load("/nonexistent/hookfuzz.yaml")
	if err == nil || !strings.Contains(err.Error(), "/nonexistent/hookfuzz.yaml") {
		t.Fatalf("err = %v, want it to name the path", err)
	}
}
