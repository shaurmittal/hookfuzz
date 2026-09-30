# hookfuzz MVP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the hookfuzz MVP: a Go CLI that delivers signed, Stripe-shaped webhooks to an app with seeded faults (duplicates, reordering, delays, retries), checks config-declared invariants against the app's state, and shrinks any failure to a minimal, replayable sequence. It ships with a Node/Express sample shop that has three planted bugs and a fixed mode.

**Architecture:** One run goes through a pipeline of small Go packages: `scenario` generates the true event history, `fault` turns it into a delivery schedule, `deliver` POSTs it through `target` (HTTP + signing), and `invariant` checks the app's state against the events the app acknowledged. `runner` wires this together for each seed and calls `shrink` (ddmin) when a check fails. `report` prints the result. Delivery is sequential and fully deterministic for a given seed, which is what makes shrinking and `replay` work.

**Tech Stack:** Go (stdlib + `gopkg.in/yaml.v3`), Node 22 + Express 4 + `stripe` 22 (sample app, signature verification), `node:test`, vhs (demo GIF).

**Spec:** `hookfuzz-writeup.md` (repo root)

## Global Constraints

- CLI and delivery engine in **Go**, built as a single static binary (`go build ./cmd/hookfuzz`).
- Sample shop in **Node/Express** with three planted bugs: non-idempotent fulfillment, order-dependent refund, stale-update overwrite. It also has a **fixed** version of the same app.
- **Seeded randomness everywhere**: the same config and seed always give the same events, schedule, and minimal reproduction.
- Payloads are **Stripe-shaped** and signed with a valid `Stripe-Signature` HMAC (`t=<unix>,v1=<hex hmac_sha256(secret, "<t>.<body>")>`). The app's real handler code runs unmodified (the sample app verifies with stripe-node's `constructEvent`).
- Exactly **3 scenarios**: `checkout`, `refund`, `subscription`.
- **4 fault types**: duplicate delivery, reordering within a window, delays, retry after a failed response.
- **Invariants live in a config file** (`hookfuzz.yaml`). Ordering invariants compare against the event `created` timestamp, **never arrival order**.
- **Shrinking** uses delta debugging. Output is a readable timeline plus the seed and a `hookfuzz replay --seed N` command.
- Out of scope: recording/replaying real Stripe traffic, a web UI, other providers, a GitHub Action.
- Go module path: `github.com/shaurmittal/hookfuzz` (change it in Task 1 if the GitHub username differs, and update the imports to match).

Design decisions this plan makes where the spec leaves a choice (keep them consistent across tasks):
- **Delivery is sequential.** Concurrency would make runs non-reproducible. "Delay" means moving a delivery later in the sequence, not wall-clock sleeping.
- **Retries:** the engine always retries non-2xx responses the way Stripe does (up to 3 attempts, re-queued 3 deliveries later). The `timeout_retry` fault models the other real case: Stripe times out on a delivery the app actually processed, then redelivers it later.
- **Invariants are three built-in kinds** (`max_count_per_key`, `field_le`, `matches_latest_event`) with parameters in YAML. There is no expression language.
- **Inspection contract:** the app exposes `GET <state_url>`, which returns a JSON object mapping a collection name to an array of objects, and `POST <reset_url>`, which clears all state.
- `matches_latest_event` compares against the newest **acknowledged** (2xx) event for each object. On equal `created` values, the first acknowledged event wins.

## Review Focus

1. **A typo in an invariant's collection or field name.** Expected: a violation that names the missing field. It must not pass silently, because a checker that can't see the field proves nothing. Pinned in Task 6 (`TestMisconfiguredFieldsAreViolations`).
2. **The target app isn't running, or a URL is wrong.** Expected: exit code 2 and a message containing "is the app running?". There should be no `FAIL` report and no panic. Pinned in Task 4 (`TestUnreachableTargetExplainsItself`) and Task 11 (`TestAppNotRunning`).
3. **A misspelled config key** (e.g. `duplicte: 0.2`). Expected: a config error naming the key. It must not silently fall back to 0 and give a false PASS. Pinned in Task 8.
4. **Large integer amounts** (JSON decodes `1000000` as a float64, and `fmt.Sprint` prints that as `1e+06`). Expected: no false positive against the event's integer value. Pinned in Task 6 (`TestLargeIntegersCompareEqual`).
5. **The app returns 500 for an event on every attempt.** Expected: a bounded number of retries (3), the event counted as not acknowledged, and the run finishing normally. It must not loop forever. Pinned in Task 5 (`TestGivesUpAfterMaxAttempts`).

---

## File Structure

```
go.mod / go.sum
.gitignore
Makefile
hookfuzz.yaml                      example config for the sample app
README.md
demo.tape                          vhs script for the README GIF
cmd/hookfuzz/main.go               CLI: run / replay subcommands, exit codes
cmd/hookfuzz/main_test.go
internal/event/event.go            Event type (Stripe shape), ObjectID()
internal/event/sign.go             Stripe-Signature header
internal/event/event_test.go
internal/scenario/scenario.go      seeded generators: checkout, refund, subscription
internal/scenario/scenario_test.go
internal/fault/fault.go            Config + Plan(): events -> delivery schedule
internal/fault/fault_test.go
internal/target/target.go          HTTP client: Send (signed), Reset, State
internal/target/target_test.go
internal/deliver/deliver.go        sequential delivery with Stripe-style retries
internal/deliver/deliver_test.go
internal/invariant/invariant.go    Spec, Validate, Check (3 kinds)
internal/invariant/invariant_test.go
internal/shrink/shrink.go          generic ddmin
internal/shrink/shrink_test.go
internal/config/config.go          YAML load, defaults, validation
internal/config/config_test.go
internal/config/example_test.go    the shipped hookfuzz.yaml loads
internal/runner/runner.go          per-seed pipeline + shrinking
internal/runner/runner_test.go
internal/report/report.go          FAIL/PASS output, timeline annotations
internal/report/report_test.go
e2e/e2e_test.go                    (build tag e2e) against the real Node app
sample-app/package.json
sample-app/store.js                in-memory state + snapshot
sample-app/handlers.js             buggy and fixed handler sets
sample-app/app.js                  createApp({mode, secret})
sample-app/server.js               entry point (env: PORT, HOOKFUZZ_APP_MODE, STRIPE_WEBHOOK_SECRET)
sample-app/test/app.test.js
```

---

### Task 1: Repo setup, Event type, and Stripe signing

**Files:**
- Create: `go.mod`, `.gitignore`
- Create: `internal/event/event.go`, `internal/event/sign.go`
- Test: `internal/event/event_test.go`

**Interfaces:**
- Consumes: nothing
- Produces:
  - `type event.Event struct { ID, Object, Type string; Created int64; APIVersion string; Livemode bool; Data event.Data }` with JSON tags `id, object, type, created, api_version, livemode, data`
  - `type event.Data struct { Object map[string]any }` (JSON `object`)
  - `func (e event.Event) ObjectID() string`: returns `data.object.id`, or `""`
  - `func event.Sign(payload []byte, secret string, t int64) string`: returns `"t=<t>,v1=<hex>"`

- [ ] **Step 1: Install Go and initialize the repo**

Go is not installed on this machine yet.

```bash
brew install go
go version        # expect go1.22 or newer
cd /Users/shauryamittal/Desktop/proj
git init
go mod init github.com/shaurmittal/hookfuzz
```

Create `.gitignore`:

```gitignore
/hookfuzz
node_modules/
.DS_Store
```

- [ ] **Step 2: Write the failing test**

`internal/event/event_test.go`:

```go
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
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/event/`
Expected: FAIL to compile (`undefined: Sign`, `undefined: Event`).

- [ ] **Step 4: Implement**

`internal/event/event.go`:

```go
// Package event defines the Stripe-shaped webhook events hookfuzz delivers.
package event

// Event is a webhook event in Stripe's wire format.
type Event struct {
	ID         string `json:"id"`
	Object     string `json:"object"`
	Type       string `json:"type"`
	Created    int64  `json:"created"`
	APIVersion string `json:"api_version"`
	Livemode   bool   `json:"livemode"`
	Data       Data   `json:"data"`
}

// Data wraps the API object the event is about.
type Data struct {
	Object map[string]any `json:"object"`
}

// ObjectID returns data.object.id, or "" if it is missing.
func (e Event) ObjectID() string {
	id, _ := e.Data.Object["id"].(string)
	return id
}
```

`internal/event/sign.go`:

```go
package event

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Sign returns a Stripe-Signature header value for payload, signed at unix
// time t: "t=<t>,v1=<hex HMAC-SHA256 of "<t>.<payload>">".
func Sign(payload []byte, secret string, t int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d.", t)
	mac.Write(payload)
	return fmt.Sprintf("t=%d,v1=%s", t, hex.EncodeToString(mac.Sum(nil)))
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/event/`
Expected: `ok`

- [ ] **Step 6: Commit**

```bash
git add .gitignore go.mod internal/event hookfuzz-writeup.md docs/
git commit -m "feat: add Stripe-shaped event type and signature helper"
```

---

### Task 2: Scenario generation

**Files:**
- Create: `internal/scenario/scenario.go`
- Test: `internal/scenario/scenario_test.go`

**Interfaces:**
- Consumes: `event.Event`, `event.Data`
- Produces:
  - `func scenario.Generate(names []string, n int, seed int64) ([]event.Event, error)`: builds `n` objects for each named scenario and returns events in true order, with `Created` strictly increasing and IDs unique
  - `func scenario.Names() []string`: returns `["checkout", "refund", "subscription"]`
  - Consts `scenario.BaseTime int64 = 1_700_000_000`, `scenario.APIVersion = "2024-06-20"`
  - Numbers in `Data.Object` are `int64`

- [ ] **Step 1: Write the failing test**

`internal/scenario/scenario_test.go`:

```go
package scenario

import (
	"reflect"
	"strings"
	"testing"

	"github.com/shaurmittal/hookfuzz/internal/event"
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/scenario/`
Expected: FAIL to compile (`undefined: Generate`).

- [ ] **Step 3: Implement**

`internal/scenario/scenario.go`:

```go
// Package scenario generates realistic, seeded Stripe event histories.
package scenario

import (
	"fmt"
	"math/rand"
	"strings"

	"github.com/shaurmittal/hookfuzz/internal/event"
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
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/scenario/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/scenario
git commit -m "feat: add seeded checkout, refund, and subscription scenarios"
```

---

### Task 3: Fault plan

**Files:**
- Create: `internal/fault/fault.go`
- Test: `internal/fault/fault_test.go`

**Interfaces:**
- Consumes: `event.Event`
- Produces:
  - `type fault.Config struct { Duplicate float64; ReorderWindow int; Delay float64; TimeoutRetry float64 }` with YAML tags `duplicate, reorder_window, delay, timeout_retry`
  - `func fault.Plan(events []event.Event, cfg fault.Config, seed int64) []event.Event`: returns the delivery schedule. It is deterministic, and every input event appears at least once.

- [ ] **Step 1: Write the failing test**

`internal/fault/fault_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/fault/`
Expected: FAIL to compile (`undefined: Plan`, `undefined: Config`).

- [ ] **Step 3: Implement**

`internal/fault/fault.go`:

```go
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
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/fault/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/fault
git commit -m "feat: add seeded fault plan (duplicate, timeout retry, delay, reorder)"
```

---

### Task 4: Target HTTP client

**Files:**
- Create: `internal/target/target.go`
- Test: `internal/target/target_test.go`

**Interfaces:**
- Consumes: `event.Event`, `event.Sign`
- Produces:
  - `type target.State map[string][]map[string]any` (numbers decode as `float64`)
  - `type target.Client struct { WebhookURL, StateURL, ResetURL, Secret string; HTTP *http.Client; Now func() time.Time }` (nil `HTTP` means a 10s-timeout client; nil `Now` means `time.Now`)
  - `func (c *target.Client) Send(ctx context.Context, ev event.Event) (int, error)`: returns the status code. Non-2xx is **not** an error; transport failures are.
  - `func (c *target.Client) Reset(ctx context.Context) error`: non-2xx is an error
  - `func (c *target.Client) State(ctx context.Context) (target.State, error)`
  - Transport errors contain the text `is the app running?`

- [ ] **Step 1: Write the failing test**

`internal/target/target_test.go`:

```go
package target

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shaurmittal/hookfuzz/internal/event"
)

func TestSendSignsAndPostsJSON(t *testing.T) {
	var gotSig, gotType string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("Stripe-Signature")
		gotType = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	c := &Client{WebhookURL: srv.URL, Secret: "whsec_x", Now: func() time.Time { return time.Unix(1700000000, 0) }}
	status, err := c.Send(context.Background(), event.Event{ID: "evt_1", Object: "event", Type: "charge.succeeded"})
	if err != nil {
		t.Fatal(err)
	}
	if status != http.StatusAccepted {
		t.Errorf("status = %d, want 202", status)
	}
	if gotType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotType)
	}
	if want := event.Sign(gotBody, "whsec_x", 1700000000); gotSig != want {
		t.Errorf("Stripe-Signature = %q, want %q", gotSig, want)
	}
	var decoded event.Event
	if err := json.Unmarshal(gotBody, &decoded); err != nil || decoded.ID != "evt_1" {
		t.Errorf("body = %s, want the JSON event evt_1", gotBody)
	}
}

func TestSendReturnsNon2xxWithoutError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	status, err := (&Client{WebhookURL: srv.URL}).Send(context.Background(), event.Event{ID: "evt_1"})
	if err != nil || status != 500 {
		t.Fatalf("Send = %d, %v; want 500, nil", status, err)
	}
}

func TestStateDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"shipments":[{"session_id":"cs_1"}],"charges":[]}`)
	}))
	defer srv.Close()
	state, err := (&Client{StateURL: srv.URL}).State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state["shipments"][0]["session_id"] != "cs_1" {
		t.Errorf("shipments = %v", state["shipments"])
	}
	if charges, ok := state["charges"]; !ok || len(charges) != 0 {
		t.Errorf("charges = %v, %v; want present and empty", charges, ok)
	}
}

func TestStateRejectsNonJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "<html>not json</html>")
	}))
	defer srv.Close()
	_, err := (&Client{StateURL: srv.URL}).State(context.Background())
	if err == nil || !strings.Contains(err.Error(), "state endpoint") {
		t.Fatalf("err = %v, want a state endpoint error", err)
	}
}

func TestResetNon2xxIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	err := (&Client{ResetURL: srv.URL}).Reset(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("err = %v, want HTTP 404", err)
	}
}

func TestUnreachableTargetExplainsItself(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close() // nothing listens here now

	err = (&Client{ResetURL: "http://" + addr + "/reset"}).Reset(context.Background())
	if err == nil || !strings.Contains(err.Error(), "is the app running?") {
		t.Fatalf("err = %v, want it to ask whether the app is running", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/target/`
Expected: FAIL to compile (`undefined: Client`).

- [ ] **Step 3: Implement**

`internal/target/target.go`:

```go
// Package target talks HTTP to the app under test: signed webhook deliveries
// plus the reset and state inspection endpoints.
package target

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/shaurmittal/hookfuzz/internal/event"
)

// State is the app's inspection snapshot: collection name -> objects.
type State map[string][]map[string]any

// Client delivers events to one app and inspects its state.
type Client struct {
	WebhookURL string
	StateURL   string
	ResetURL   string
	Secret     string
	HTTP       *http.Client     // nil uses a client with a 10s timeout
	Now        func() time.Time // nil uses time.Now; the signature timestamp
}

var defaultHTTP = &http.Client{Timeout: 10 * time.Second}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTP
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Send POSTs ev as signed JSON and returns the response status. A non-2xx
// status is not an error; only failing to get a response is.
func (c *Client) Send(ctx context.Context, ev event.Event) (int, error) {
	body, err := json.Marshal(ev)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", event.Sign(body, c.Secret, c.now().Unix()))
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, unreachable("webhook", c.WebhookURL, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// Reset asks the app to clear all state.
func (c *Client) Reset(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ResetURL, nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return unreachable("reset", c.ResetURL, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("reset endpoint %s returned HTTP %d", c.ResetURL, resp.StatusCode)
	}
	return nil
}

// State fetches the app's inspection snapshot.
func (c *Client) State(ctx context.Context) (State, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.StateURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, unreachable("state", c.StateURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("state endpoint %s returned HTTP %d", c.StateURL, resp.StatusCode)
	}
	var s State
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("state endpoint %s did not return a JSON object of arrays: %w", c.StateURL, err)
	}
	return s, nil
}

func unreachable(what, url string, err error) error {
	return fmt.Errorf("cannot reach %s endpoint %s (is the app running?): %w", what, url, err)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/target/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/target
git commit -m "feat: add target client for signed delivery, reset, and state"
```

---

### Task 5: Delivery engine

**Files:**
- Create: `internal/deliver/deliver.go`
- Test: `internal/deliver/deliver_test.go`

**Interfaces:**
- Consumes: `event.Event`
- Produces:
  - `type deliver.Sender interface { Send(ctx context.Context, ev event.Event) (int, error) }` (`*target.Client` satisfies it)
  - Consts `deliver.MaxAttempts = 3`, `deliver.RetryGap = 3`
  - `type deliver.Result struct { Acknowledged []event.Event; Attempts int }`. `Acknowledged` holds events that got at least one 2xx, unique by ID, in order of first acknowledgement.
  - `func deliver.Run(ctx context.Context, s deliver.Sender, schedule []event.Event) (deliver.Result, error)`

- [ ] **Step 1: Write the failing test**

`internal/deliver/deliver_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/deliver/`
Expected: FAIL to compile (`undefined: Run`).

- [ ] **Step 3: Implement**

`internal/deliver/deliver.go`:

```go
// Package deliver sends a schedule of events to the app one at a time,
// retrying failures the way Stripe does.
package deliver

import (
	"context"
	"fmt"
	"slices"

	"github.com/shaurmittal/hookfuzz/internal/event"
)

const (
	// MaxAttempts caps deliveries of one scheduled event that keeps failing.
	MaxAttempts = 3
	// RetryGap is how many deliveries later a failed one is retried.
	RetryGap = 3
)

// Sender delivers one event and reports the HTTP status.
type Sender interface {
	Send(ctx context.Context, ev event.Event) (int, error)
}

// Result summarizes one delivery run.
type Result struct {
	// Acknowledged holds events that got at least one 2xx, once each, in the
	// order they were first acknowledged.
	Acknowledged []event.Event
	Attempts     int
}

// Run delivers the schedule in order. A non-2xx response puts the event back
// in the queue RetryGap deliveries later, up to MaxAttempts in total. A
// transport error (no response at all) aborts the run.
func Run(ctx context.Context, s Sender, schedule []event.Event) (Result, error) {
	type item struct {
		ev      event.Event
		attempt int
	}
	queue := make([]item, len(schedule))
	for i, ev := range schedule {
		queue[i] = item{ev: ev, attempt: 1}
	}
	var res Result
	acked := map[string]bool{}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		status, err := s.Send(ctx, it.ev)
		if err != nil {
			return res, fmt.Errorf("delivering %s (%s): %w", it.ev.ID, it.ev.Type, err)
		}
		res.Attempts++
		if status >= 200 && status < 300 {
			if !acked[it.ev.ID] {
				acked[it.ev.ID] = true
				res.Acknowledged = append(res.Acknowledged, it.ev)
			}
			continue
		}
		if it.attempt < MaxAttempts {
			queue = slices.Insert(queue, min(RetryGap, len(queue)), item{ev: it.ev, attempt: it.attempt + 1})
		}
	}
	return res, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/deliver/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/deliver
git commit -m "feat: add sequential delivery engine with Stripe-style retries"
```

---

### Task 6: Invariants

**Files:**
- Create: `internal/invariant/invariant.go`
- Test: `internal/invariant/invariant_test.go`

**Interfaces:**
- Consumes: `event.Event`, `target.State`
- Produces:
  - Consts `invariant.KindMaxCountPerKey = "max_count_per_key"`, `invariant.KindFieldLE = "field_le"`, `invariant.KindMatchesLatestEvent = "matches_latest_event"`
  - `type invariant.Spec struct { Name, Kind, Collection, Key string; Max int; Left, Right, Field string; EventTypes []string; EventField string }` with YAML tags `name, kind, collection, key, max, left, right, field, event_types, event_field`
  - `type invariant.Violation struct { Invariant, Message string }`
  - `func (s invariant.Spec) Validate() error`
  - `func (s invariant.Spec) Check(state target.State, acked []event.Event) []invariant.Violation`
  - `func invariant.CheckAll(specs []invariant.Spec, state target.State, acked []event.Event) []invariant.Violation`: returns violations in spec order

- [ ] **Step 1: Write the failing test**

`internal/invariant/invariant_test.go`:

```go
package invariant

import (
	"strings"
	"testing"

	"github.com/shaurmittal/hookfuzz/internal/event"
	"github.com/shaurmittal/hookfuzz/internal/target"
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/invariant/`
Expected: FAIL to compile (`undefined: Spec`).

- [ ] **Step 3: Implement**

`internal/invariant/invariant.go`:

```go
// Package invariant checks user-declared correctness rules against the app's
// state after a delivery run.
package invariant

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/shaurmittal/hookfuzz/internal/event"
	"github.com/shaurmittal/hookfuzz/internal/target"
)

const (
	KindMaxCountPerKey     = "max_count_per_key"
	KindFieldLE            = "field_le"
	KindMatchesLatestEvent = "matches_latest_event"
)

// Spec is one invariant from the config file. Which fields apply depends on Kind.
type Spec struct {
	Name       string `yaml:"name"`
	Kind       string `yaml:"kind"`
	Collection string `yaml:"collection"`

	Key string `yaml:"key"` // max_count_per_key
	Max int    `yaml:"max"` // max_count_per_key

	Left  string `yaml:"left"`  // field_le
	Right string `yaml:"right"` // field_le

	Field      string   `yaml:"field"`       // matches_latest_event: field on the state object
	EventTypes []string `yaml:"event_types"` // matches_latest_event: empty = all types
	EventField string   `yaml:"event_field"` // matches_latest_event: field on data.object
}

// Violation is one broken invariant, with a human-readable explanation.
type Violation struct {
	Invariant string
	Message   string
}

// Validate reports config mistakes in s.
func (s Spec) Validate() error {
	if s.Name == "" {
		return errors.New("invariant is missing a name")
	}
	if s.Collection == "" {
		return fmt.Errorf("invariant %q: collection is required", s.Name)
	}
	switch s.Kind {
	case KindMaxCountPerKey:
		if s.Key == "" {
			return fmt.Errorf("invariant %q: key is required", s.Name)
		}
		if s.Max < 1 {
			return fmt.Errorf("invariant %q: max must be at least 1", s.Name)
		}
	case KindFieldLE:
		if s.Left == "" || s.Right == "" {
			return fmt.Errorf("invariant %q: left and right are required", s.Name)
		}
	case KindMatchesLatestEvent:
		if s.Field == "" || s.EventField == "" {
			return fmt.Errorf("invariant %q: field and event_field are required", s.Name)
		}
	default:
		return fmt.Errorf("invariant %q: unknown kind %q (known: %s, %s, %s)",
			s.Name, s.Kind, KindMaxCountPerKey, KindFieldLE, KindMatchesLatestEvent)
	}
	return nil
}

// CheckAll evaluates every spec, returning violations in spec order.
func CheckAll(specs []Spec, state target.State, acked []event.Event) []Violation {
	var out []Violation
	for _, s := range specs {
		out = append(out, s.Check(state, acked)...)
	}
	return out
}

// Check evaluates s against the app state and the events the app acknowledged.
// A missing collection or field is itself a violation: a rule that can't see
// its data proves nothing.
func (s Spec) Check(state target.State, acked []event.Event) []Violation {
	items, ok := state[s.Collection]
	if !ok {
		return []Violation{s.violation("app state has no %q collection", s.Collection)}
	}
	switch s.Kind {
	case KindMaxCountPerKey:
		return s.checkMaxCount(items)
	case KindFieldLE:
		return s.checkFieldLE(items)
	case KindMatchesLatestEvent:
		return s.checkLatest(items, acked)
	}
	return []Violation{s.violation("unknown kind %q", s.Kind)}
}

func (s Spec) violation(format string, args ...any) Violation {
	return Violation{Invariant: s.Name, Message: fmt.Sprintf(format, args...)}
}

func (s Spec) checkMaxCount(items []map[string]any) []Violation {
	var out []Violation
	counts := map[string]int{}
	var order []string
	for i, item := range items {
		v, ok := item[s.Key]
		if !ok {
			out = append(out, s.violation("%s[%d] has no field %q", s.Collection, i, s.Key))
			continue
		}
		k := norm(v)
		if counts[k] == 0 {
			order = append(order, k)
		}
		counts[k]++
	}
	for _, k := range order {
		if counts[k] > s.Max {
			out = append(out, s.violation("%s=%s appears %d times in %s (max %d)", s.Key, k, counts[k], s.Collection, s.Max))
		}
	}
	return out
}

func (s Spec) checkFieldLE(items []map[string]any) []Violation {
	var out []Violation
	for i, item := range items {
		l, lok := item[s.Left].(float64)
		r, rok := item[s.Right].(float64)
		name := label(s.Collection, i, item)
		if !lok || !rok {
			out = append(out, s.violation("%s: %q and %q must both be numbers (got %v and %v)",
				name, s.Left, s.Right, item[s.Left], item[s.Right]))
			continue
		}
		if l > r {
			out = append(out, s.violation("%s: %s = %s is greater than %s = %s", name, s.Left, norm(l), s.Right, norm(r)))
		}
	}
	return out
}

func (s Spec) checkLatest(items []map[string]any, acked []event.Event) []Violation {
	latest := map[string]event.Event{}
	for _, ev := range acked {
		if len(s.EventTypes) > 0 && !slices.Contains(s.EventTypes, ev.Type) {
			continue
		}
		// Newest by created, never by arrival. On a tie the first acknowledged wins.
		id := ev.ObjectID()
		if cur, ok := latest[id]; !ok || ev.Created > cur.Created {
			latest[id] = ev
		}
	}
	var out []Violation
	for i, item := range items {
		idv, ok := item["id"]
		if !ok {
			out = append(out, s.violation("%s[%d] has no field \"id\"", s.Collection, i))
			continue
		}
		ev, ok := latest[norm(idv)]
		if !ok {
			continue
		}
		name := label(s.Collection, i, item)
		got, ok := item[s.Field]
		if !ok {
			out = append(out, s.violation("%s has no field %q", name, s.Field))
			continue
		}
		want, ok := ev.Data.Object[s.EventField]
		if !ok {
			out = append(out, s.violation("event %s (%s) has no data.object.%s", ev.ID, ev.Type, s.EventField))
			continue
		}
		if norm(got) != norm(want) {
			out = append(out, s.violation("%s: %s = %s, but the latest event %s (%s, created %d) says %s",
				name, s.Field, norm(got), ev.ID, ev.Type, ev.Created, norm(want)))
		}
	}
	return out
}

func label(collection string, i int, item map[string]any) string {
	if id, ok := item["id"]; ok {
		return fmt.Sprintf("%s[%s]", collection, norm(id))
	}
	return fmt.Sprintf("%s[%d]", collection, i)
}

// norm formats a value for comparison and display. JSON numbers arrive as
// float64, so whole floats are printed as integers (1e+06 -> 1000000) to
// compare equal to the int64 values in generated events.
func norm(v any) string {
	if f, ok := v.(float64); ok && f == math.Trunc(f) && math.Abs(f) < 1e18 {
		return strconv.FormatInt(int64(f), 10)
	}
	return fmt.Sprint(v)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/invariant/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/invariant
git commit -m "feat: add invariant kinds that check app state against acknowledged events"
```

---

### Task 7: Shrinker (ddmin)

**Files:**
- Create: `internal/shrink/shrink.go`
- Test: `internal/shrink/shrink_test.go`

**Interfaces:**
- Consumes: nothing
- Produces: `func shrink.Minimize[T any](items []T, fails func([]T) (bool, error)) ([]T, error)`. It returns a 1-minimal subsequence (order preserved) for which `fails` is true. The caller guarantees `fails(items)` is true on entry. Inputs of length 0 or 1 are returned without calling `fails`.

- [ ] **Step 1: Write the failing test**

`internal/shrink/shrink_test.go`:

```go
package shrink

import (
	"errors"
	"math/rand"
	"slices"
	"testing"
)

func seq(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i + 1
	}
	return out
}

func containsAll(want ...int) func([]int) (bool, error) {
	return func(xs []int) (bool, error) {
		for _, w := range want {
			if !slices.Contains(xs, w) {
				return false, nil
			}
		}
		return true, nil
	}
}

func TestMinimizeFindsASingleCause(t *testing.T) {
	got, err := Minimize(seq(40), containsAll(17))
	if err != nil || !slices.Equal(got, []int{17}) {
		t.Fatalf("Minimize = %v, %v; want [17]", got, err)
	}
}

func TestMinimizeFindsAPair(t *testing.T) {
	got, _ := Minimize(seq(10), containsAll(3, 7))
	if !slices.Equal(got, []int{3, 7}) {
		t.Fatalf("Minimize = %v, want [3 7]", got)
	}
}

func TestMinimizeKeepsOrder(t *testing.T) {
	sevenBeforeThree := func(xs []int) (bool, error) {
		i, j := slices.Index(xs, 7), slices.Index(xs, 3)
		return i >= 0 && j >= 0 && i < j, nil
	}
	got, _ := Minimize([]int{9, 7, 5, 3, 1}, sevenBeforeThree)
	if !slices.Equal(got, []int{7, 3}) {
		t.Fatalf("Minimize = %v, want [7 3]", got)
	}
}

func TestMinimizeIsOneMinimal(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 100; trial++ {
		var target []int
		for _, x := range rng.Perm(40)[:1+rng.Intn(4)] {
			target = append(target, x+1)
		}
		got, _ := Minimize(seq(40), containsAll(target...))
		slices.Sort(target)
		if !slices.Equal(got, target) {
			t.Fatalf("trial %d: Minimize = %v, want %v", trial, got, target)
		}
	}
}

func TestMinimizeTinyInputsSkipTheCheck(t *testing.T) {
	calls := 0
	count := func([]int) (bool, error) { calls++; return true, nil }
	if got, _ := Minimize([]int{}, count); len(got) != 0 {
		t.Errorf("Minimize([]) = %v", got)
	}
	if got, _ := Minimize([]int{4}, count); !slices.Equal(got, []int{4}) {
		t.Errorf("Minimize([4]) = %v", got)
	}
	if calls != 0 {
		t.Errorf("fails called %d times, want 0", calls)
	}
}

func TestMinimizePropagatesErrors(t *testing.T) {
	boom := errors.New("target down")
	_, err := Minimize(seq(8), func([]int) (bool, error) { return false, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/shrink/`
Expected: FAIL to compile (`undefined: Minimize`).

- [ ] **Step 3: Implement**

`internal/shrink/shrink.go`:

```go
// Package shrink reduces a failing input to a minimal one with delta
// debugging (Zeller's ddmin).
package shrink

import "slices"

// Minimize returns a 1-minimal subsequence of items for which fails is still
// true: removing any single remaining element makes it pass. Order is kept.
// It tries large chunks first, then smaller ones, down to single elements.
// fails(items) must be true on entry.
func Minimize[T any](items []T, fails func([]T) (bool, error)) ([]T, error) {
	n := 2
	for len(items) >= 2 {
		chunks := split(items, n)
		reduced := false

		// Does one chunk alone still fail?
		for _, c := range chunks {
			ok, err := fails(c)
			if err != nil {
				return nil, err
			}
			if ok {
				items, n, reduced = c, 2, true
				break
			}
		}

		// Does removing one chunk still fail? (With n == 2 the complements
		// are the chunks we just tried.)
		if !reduced && n > 2 {
			for i := range chunks {
				comp := complement(chunks, i)
				ok, err := fails(comp)
				if err != nil {
					return nil, err
				}
				if ok {
					items, n, reduced = comp, max(n-1, 2), true
					break
				}
			}
		}

		if !reduced {
			if n >= len(items) {
				break // every single element was necessary
			}
			n = min(n*2, len(items))
		}
	}
	return slices.Clone(items), nil
}

func split[T any](items []T, n int) [][]T {
	chunks := make([][]T, 0, n)
	size, rem := len(items)/n, len(items)%n
	start := 0
	for i := 0; i < n; i++ {
		end := start + size
		if i < rem {
			end++
		}
		chunks = append(chunks, items[start:end])
		start = end
	}
	return chunks
}

func complement[T any](chunks [][]T, skip int) []T {
	var out []T
	for i, c := range chunks {
		if i != skip {
			out = append(out, c...)
		}
	}
	return out
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/shrink/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/shrink
git commit -m "feat: add generic ddmin shrinker"
```

---

### Task 8: Config loader

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: `fault.Config`, `invariant.Spec` (+ `Validate`), `scenario.Names()`
- Produces:
  - `type config.Target struct { WebhookURL, StateURL, ResetURL, SigningSecret string }` with YAML tags `webhook_url, state_url, reset_url, signing_secret`
  - `type config.Config struct { Target config.Target; Scenarios []string; ScenarioSize int; Faults fault.Config; Invariants []invariant.Spec }` with YAML tags `target, scenarios, scenario_size, faults, invariants`
  - `const config.DefaultScenarioSize = 5`
  - `func config.Parse(data []byte) (config.Config, error)` (strict: unknown keys are errors) and `func config.Load(path string) (config.Config, error)`

- [ ] **Step 1: Add the YAML dependency**

```bash
go get gopkg.in/yaml.v3
```

- [ ] **Step 2: Write the failing test**

`internal/config/config_test.go`:

```go
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
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/config/`
Expected: FAIL to compile (`undefined: Parse`).

- [ ] **Step 4: Implement**

`internal/config/config.go`:

```go
// Package config loads and validates hookfuzz.yaml.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/shaurmittal/hookfuzz/internal/fault"
	"github.com/shaurmittal/hookfuzz/internal/invariant"
	"github.com/shaurmittal/hookfuzz/internal/scenario"
)

// DefaultScenarioSize is how many objects each scenario creates per run.
const DefaultScenarioSize = 5

// Target says where the app under test lives.
type Target struct {
	WebhookURL    string `yaml:"webhook_url"`
	StateURL      string `yaml:"state_url"`
	ResetURL      string `yaml:"reset_url"`
	SigningSecret string `yaml:"signing_secret"`
}

// Config is the parsed hookfuzz.yaml.
type Config struct {
	Target       Target           `yaml:"target"`
	Scenarios    []string         `yaml:"scenarios"`
	ScenarioSize int              `yaml:"scenario_size"`
	Faults       fault.Config     `yaml:"faults"`
	Invariants   []invariant.Spec `yaml:"invariants"`
}

// Load reads and validates the config file at path.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("reading config: %w", err)
	}
	c, err := Parse(data)
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return c, nil
}

// Parse decodes and validates a config. Unknown keys are errors, so a typo
// can't silently turn a fault or rule off.
func Parse(data []byte) (Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		if errors.Is(err, io.EOF) {
			return Config{}, errors.New("config is empty")
		}
		return Config{}, err
	}
	if c.ScenarioSize == 0 {
		c.ScenarioSize = DefaultScenarioSize
	}
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) validate() error {
	for _, u := range []struct{ key, val string }{
		{"target.webhook_url", c.Target.WebhookURL},
		{"target.state_url", c.Target.StateURL},
		{"target.reset_url", c.Target.ResetURL},
	} {
		parsed, err := url.Parse(u.val)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("%s must be an http(s) URL, got %q", u.key, u.val)
		}
	}
	if c.Target.SigningSecret == "" {
		return errors.New("target.signing_secret is required")
	}

	if len(c.Scenarios) == 0 {
		return fmt.Errorf("scenarios: list at least one of %s", strings.Join(scenario.Names(), ", "))
	}
	for _, s := range c.Scenarios {
		if !slices.Contains(scenario.Names(), s) {
			return fmt.Errorf("unknown scenario %q (known: %s)", s, strings.Join(scenario.Names(), ", "))
		}
	}
	if c.ScenarioSize < 1 {
		return fmt.Errorf("scenario_size must be at least 1, got %d", c.ScenarioSize)
	}

	for _, p := range []struct {
		key string
		val float64
	}{
		{"duplicate", c.Faults.Duplicate},
		{"delay", c.Faults.Delay},
		{"timeout_retry", c.Faults.TimeoutRetry},
	} {
		if p.val < 0 || p.val > 1 {
			return fmt.Errorf("faults.%s must be between 0 and 1, got %v", p.key, p.val)
		}
	}
	if c.Faults.ReorderWindow < 0 {
		return fmt.Errorf("faults.reorder_window must be 0 or more, got %d", c.Faults.ReorderWindow)
	}

	if len(c.Invariants) == 0 {
		return errors.New("invariants: declare at least one")
	}
	seen := map[string]bool{}
	for _, inv := range c.Invariants {
		if err := inv.Validate(); err != nil {
			return err
		}
		if seen[inv.Name] {
			return fmt.Errorf("duplicate invariant name %q", inv.Name)
		}
		seen[inv.Name] = true
	}
	return nil
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/config/`
Expected: `ok`

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/config
git commit -m "feat: add strict YAML config loading and validation"
```

---

### Task 9: Runner

**Files:**
- Create: `internal/runner/runner.go`
- Test: `internal/runner/runner_test.go`

**Interfaces:**
- Consumes: `config.Config`, `scenario.Generate`, `fault.Plan`, `deliver.Run`, `invariant.CheckAll`, `invariant.Violation`, `shrink.Minimize`, `target.State`, `event.Event`
- Produces:
  - `type runner.Target interface { Send(ctx, event.Event) (int, error); Reset(ctx) error; State(ctx) (target.State, error) }` (`*target.Client` satisfies it)
  - `type runner.Failure struct { Seed int64; Invariant string; Schedule, Minimal []event.Event; Violations []invariant.Violation }`. `Violations` are the named invariant's violations from replaying `Minimal`, and may be empty if the app is nondeterministic.
  - `type runner.Runner struct { Config config.Config; Target runner.Target }`
  - `func (r *Runner) Schedule(seed int64) ([]event.Event, error)`
  - `func (r *Runner) Execute(ctx, schedule []event.Event) ([]invariant.Violation, error)`: reset, deliver, fetch state, check
  - `func (r *Runner) RunSeed(ctx, seed int64) (*runner.Failure, error)`: returns nil on pass
  - `func (r *Runner) Run(ctx, start int64, runs int) (*runner.Failure, int, error)`: stops at the first failure and returns how many seeds ran

- [ ] **Step 1: Write the failing test**

`internal/runner/runner_test.go`:

```go
package runner

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/shaurmittal/hookfuzz/internal/config"
	"github.com/shaurmittal/hookfuzz/internal/event"
	"github.com/shaurmittal/hookfuzz/internal/fault"
	"github.com/shaurmittal/hookfuzz/internal/invariant"
	"github.com/shaurmittal/hookfuzz/internal/target"
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/runner/`
Expected: FAIL to compile (`undefined: Runner`).

- [ ] **Step 3: Implement**

`internal/runner/runner.go`:

```go
// Package runner executes seeded fault-injection runs and shrinks failures.
package runner

import (
	"context"
	"fmt"

	"github.com/shaurmittal/hookfuzz/internal/config"
	"github.com/shaurmittal/hookfuzz/internal/deliver"
	"github.com/shaurmittal/hookfuzz/internal/event"
	"github.com/shaurmittal/hookfuzz/internal/fault"
	"github.com/shaurmittal/hookfuzz/internal/invariant"
	"github.com/shaurmittal/hookfuzz/internal/scenario"
	"github.com/shaurmittal/hookfuzz/internal/shrink"
	"github.com/shaurmittal/hookfuzz/internal/target"
)

// Target is the app under test.
type Target interface {
	Send(ctx context.Context, ev event.Event) (int, error)
	Reset(ctx context.Context) error
	State(ctx context.Context) (target.State, error)
}

// Failure is a broken invariant plus its shrunk reproduction.
type Failure struct {
	Seed      int64
	Invariant string
	Schedule  []event.Event // the full delivery schedule for Seed
	Minimal   []event.Event // the shrunk schedule that still breaks Invariant
	// Violations of Invariant from replaying Minimal. Empty means the minimal
	// schedule passed on replay, which points at a nondeterministic app.
	Violations []invariant.Violation
}

// Runner ties config, the fault pipeline, and the target together.
type Runner struct {
	Config config.Config
	Target Target
}

// Schedule returns the delivery schedule for seed.
func (r *Runner) Schedule(seed int64) ([]event.Event, error) {
	evs, err := scenario.Generate(r.Config.Scenarios, r.Config.ScenarioSize, seed)
	if err != nil {
		return nil, err
	}
	return fault.Plan(evs, r.Config.Faults, seed), nil
}

// Execute resets the app, delivers schedule, and checks every invariant.
func (r *Runner) Execute(ctx context.Context, schedule []event.Event) ([]invariant.Violation, error) {
	if err := r.Target.Reset(ctx); err != nil {
		return nil, err
	}
	res, err := deliver.Run(ctx, r.Target, schedule)
	if err != nil {
		return nil, err
	}
	state, err := r.Target.State(ctx)
	if err != nil {
		return nil, err
	}
	return invariant.CheckAll(r.Config.Invariants, state, res.Acknowledged), nil
}

// RunSeed runs one seed. If an invariant fails, it shrinks the schedule to
// the smallest one that still breaks that same invariant. Nil means pass.
func (r *Runner) RunSeed(ctx context.Context, seed int64) (*Failure, error) {
	schedule, err := r.Schedule(seed)
	if err != nil {
		return nil, err
	}
	violations, err := r.Execute(ctx, schedule)
	if err != nil {
		return nil, err
	}
	if len(violations) == 0 {
		return nil, nil
	}
	name := violations[0].Invariant
	minimal, err := shrink.Minimize(schedule, func(sub []event.Event) (bool, error) {
		vs, err := r.Execute(ctx, sub)
		return len(only(vs, name)) > 0, err
	})
	if err != nil {
		return nil, fmt.Errorf("shrinking seed %d: %w", seed, err)
	}
	final, err := r.Execute(ctx, minimal)
	if err != nil {
		return nil, err
	}
	return &Failure{Seed: seed, Invariant: name, Schedule: schedule, Minimal: minimal, Violations: only(final, name)}, nil
}

// Run tries seeds start, start+1, ... up to runs of them, and stops at the
// first failure. It returns how many seeds it executed.
func (r *Runner) Run(ctx context.Context, start int64, runs int) (*Failure, int, error) {
	for i := 0; i < runs; i++ {
		f, err := r.RunSeed(ctx, start+int64(i))
		if err != nil {
			return nil, i, err
		}
		if f != nil {
			return f, i + 1, nil
		}
	}
	return nil, runs, nil
}

func only(vs []invariant.Violation, name string) []invariant.Violation {
	var out []invariant.Violation
	for _, v := range vs {
		if v.Invariant == name {
			out = append(out, v)
		}
	}
	return out
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/runner/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/runner
git commit -m "feat: add runner that executes seeds and shrinks failures"
```

---

### Task 10: Report output

**Files:**
- Create: `internal/report/report.go`
- Test: `internal/report/report_test.go`

**Interfaces:**
- Consumes: `runner.Failure`, `event.Event`
- Produces:
  - `func report.Failure(w io.Writer, f *runner.Failure, configPath string)`
  - `func report.Pass(w io.Writer, start int64, runs, invariants int)`
  - `func report.Annotate(schedule []event.Event) []string`: returns one note per delivery: `"duplicate"`, `"out of order (older than #k)"`, both joined by `", "`, or `""`

- [ ] **Step 1: Write the failing test**

`internal/report/report_test.go`:

```go
package report

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/shaurmittal/hookfuzz/internal/event"
	"github.com/shaurmittal/hookfuzz/internal/invariant"
	"github.com/shaurmittal/hookfuzz/internal/runner"
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/report/`
Expected: FAIL to compile (`undefined: Failure`).

- [ ] **Step 3: Implement**

`internal/report/report.go`:

```go
// Package report prints run results for humans.
package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/shaurmittal/hookfuzz/internal/event"
	"github.com/shaurmittal/hookfuzz/internal/runner"
)

// Failure prints the broken invariant, the minimal timeline, and how to replay it.
func Failure(w io.Writer, f *runner.Failure, configPath string) {
	fmt.Fprintf(w, "FAIL invariant %q (seed %d)\n", f.Invariant, f.Seed)
	if len(f.Violations) == 0 {
		fmt.Fprintln(w, "  (the minimal schedule passed when replayed; the app may be nondeterministic)")
	}
	for _, v := range f.Violations {
		fmt.Fprintf(w, "  %s\n", v.Message)
	}
	fmt.Fprintf(w, "Minimal reproduction (%d of %d deliveries):\n", len(f.Minimal), len(f.Schedule))
	notes := Annotate(f.Minimal)
	for i, ev := range f.Minimal {
		line := fmt.Sprintf("  %d. %-30s %-10s %s", i+1, ev.Type, ev.ObjectID(), ev.ID)
		if notes[i] != "" {
			line += "  <- " + notes[i]
		}
		fmt.Fprintln(w, line)
	}
	fmt.Fprintf(w, "Replay: hookfuzz replay --seed %d --config %s\n", f.Seed, configPath)
}

// Pass prints a one-line success summary.
func Pass(w io.Writer, start int64, runs, invariants int) {
	if runs == 1 {
		fmt.Fprintf(w, "PASS seed %d, %d invariants held\n", start, invariants)
		return
	}
	fmt.Fprintf(w, "PASS %d runs (seeds %d..%d), %d invariants held\n", runs, start, start+int64(runs)-1, invariants)
}

// Annotate explains what is unusual about each delivery compared with the
// ones before it: a repeated event ID, or an event older than one already
// delivered for the same object.
func Annotate(schedule []event.Event) []string {
	notes := make([]string, len(schedule))
	seen := map[string]bool{}
	newest := map[string]int{} // object ID -> index of its newest delivery so far
	for i, ev := range schedule {
		var parts []string
		if seen[ev.ID] {
			parts = append(parts, "duplicate")
		}
		obj := ev.ObjectID()
		j, ok := newest[obj]
		if ok && ev.Created < schedule[j].Created {
			parts = append(parts, fmt.Sprintf("out of order (older than #%d)", j+1))
		} else if !ok || ev.Created > schedule[j].Created {
			newest[obj] = i
		}
		seen[ev.ID] = true
		notes[i] = strings.Join(parts, ", ")
	}
	return notes
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/report/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/report
git commit -m "feat: add failure timeline and pass summary output"
```

---

### Task 11: CLI

**Files:**
- Create: `cmd/hookfuzz/main.go`
- Test: `cmd/hookfuzz/main_test.go`

**Interfaces:**
- Consumes: `config.Load`, `target.Client`, `runner.Runner` (`Run`, `RunSeed`), `report.Failure`, `report.Pass`
- Produces:
  - Binary `hookfuzz` with the commands `run [--config hookfuzz.yaml] [--seed 1] [--runs 100]` and `replay --seed N [--config hookfuzz.yaml]`
  - Exit codes: `0` pass, `1` invariant failure, `2` usage/config/target error
  - `func run(ctx context.Context, args []string, stdout, stderr io.Writer) int` (for tests)

- [ ] **Step 1: Write the failing test**

`cmd/hookfuzz/main_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/shaurmittal/hookfuzz/internal/event"
)

// fakeShop is a tiny HTTP app with hookfuzz's inspection endpoints. It ships
// an order per checkout.session.completed; fixed mode skips seen event IDs.
func fakeShop(fixed bool) http.Handler {
	var mu sync.Mutex
	shipments := []map[string]any{}
	seen := map[string]bool{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhook", func(w http.ResponseWriter, r *http.Request) {
		var ev event.Event
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if fixed && seen[ev.ID] {
			return
		}
		seen[ev.ID] = true
		if ev.Type == "checkout.session.completed" {
			shipments = append(shipments, map[string]any{"session_id": ev.ObjectID()})
		}
	})
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"shipments": shipments})
	})
	mux.HandleFunc("POST /reset", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		shipments, seen = []map[string]any{}, map[string]bool{}
	})
	return mux
}

const configTemplate = `target:
  webhook_url: %[1]s/webhook
  state_url: %[1]s/state
  reset_url: %[1]s/reset
  signing_secret: whsec_test
scenarios: [checkout]
faults:
  duplicate: 0.2
  reorder_window: 3
invariants:
  - name: one_shipment_per_session
    kind: max_count_per_key
    collection: shipments
    key: session_id
    max: 1
`

func writeConfig(t *testing.T, baseURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hookfuzz.yaml")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(configTemplate, baseURL)), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runCLI(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestNoArgsPrintsUsage(t *testing.T) {
	code, _, stderr := runCLI()
	if code != 2 || !strings.Contains(stderr, "Usage:") {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
}

func TestUnknownCommand(t *testing.T) {
	code, _, stderr := runCLI("fuzz")
	if code != 2 || !strings.Contains(stderr, `unknown command "fuzz"`) {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
}

func TestReplayRequiresSeed(t *testing.T) {
	code, _, stderr := runCLI("replay", "--config", "whatever.yaml")
	if code != 2 || !strings.Contains(stderr, "replay needs --seed") {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
}

func TestMissingConfig(t *testing.T) {
	code, _, stderr := runCLI("run", "--config", "/nonexistent/h.yaml")
	if code != 2 || !strings.Contains(stderr, "/nonexistent/h.yaml") {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
}

func TestAppNotRunning(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + l.Addr().String()
	l.Close()

	code, stdout, stderr := runCLI("run", "--config", writeConfig(t, base))
	if code != 2 || !strings.Contains(stderr, "is the app running?") || strings.Contains(stdout, "FAIL") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestRunReportsAFailure(t *testing.T) {
	srv := httptest.NewServer(fakeShop(false))
	defer srv.Close()
	code, stdout, stderr := runCLI("run", "--config", writeConfig(t, srv.URL))
	if code != 1 {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	for _, want := range []string{`FAIL invariant "one_shipment_per_session"`, "Minimal reproduction (2 of", "Replay: hookfuzz replay --seed"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestRunPassesOnAFixedApp(t *testing.T) {
	srv := httptest.NewServer(fakeShop(true))
	defer srv.Close()
	code, stdout, stderr := runCLI("run", "--config", writeConfig(t, srv.URL), "--runs", "20")
	if code != 0 || !strings.Contains(stdout, "PASS 20 runs") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestReplayReproducesTheRunReport(t *testing.T) {
	srv := httptest.NewServer(fakeShop(false))
	defer srv.Close()
	cfg := writeConfig(t, srv.URL)
	_, runOut, _ := runCLI("run", "--config", cfg)
	m := regexp.MustCompile(`--seed (\d+)`).FindStringSubmatch(runOut)
	if m == nil {
		t.Fatalf("no seed in run output:\n%s", runOut)
	}
	code, replayOut, _ := runCLI("replay", "--seed", m[1], "--config", cfg)
	if code != 1 || replayOut != runOut {
		t.Fatalf("replay (code %d) differs from run.\nrun:\n%s\nreplay:\n%s", code, runOut, replayOut)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/hookfuzz/`
Expected: FAIL to compile (`undefined: run`).

- [ ] **Step 3: Implement**

`cmd/hookfuzz/main.go`:

```go
// Command hookfuzz delivers webhooks to your app twice, out of order, late,
// or after failures, and checks that it still ends up in a correct state.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/shaurmittal/hookfuzz/internal/config"
	"github.com/shaurmittal/hookfuzz/internal/report"
	"github.com/shaurmittal/hookfuzz/internal/runner"
	"github.com/shaurmittal/hookfuzz/internal/target"
)

const usage = `hookfuzz: chaos testing for payment webhook handlers

Usage:
  hookfuzz run    [--config hookfuzz.yaml] [--seed 1] [--runs 100]
  hookfuzz replay --seed N [--config hookfuzz.yaml]

Exit codes: 0 all invariants held, 1 an invariant failed, 2 usage or setup error.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "run":
		return cmdRun(ctx, args[1:], stdout, stderr)
	case "replay":
		return cmdReplay(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
	return 2
}

func cmdRun(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "hookfuzz.yaml", "path to the config file")
	seed := fs.Int64("seed", 1, "first seed to try")
	runs := fs.Int("runs", 100, "how many seeds to try")
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	if *runs < 1 {
		fmt.Fprintln(stderr, "hookfuzz: --runs must be at least 1")
		return 2
	}
	r, err := newRunner(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "hookfuzz:", err)
		return 2
	}
	f, n, err := r.Run(ctx, *seed, *runs)
	if err != nil {
		fmt.Fprintln(stderr, "hookfuzz:", err)
		return 2
	}
	if f != nil {
		report.Failure(stdout, f, *configPath)
		return 1
	}
	report.Pass(stdout, *seed, n, len(r.Config.Invariants))
	return 0
}

func cmdReplay(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "hookfuzz.yaml", "path to the config file")
	seed := fs.Int64("seed", 0, "seed to replay (required; copy it from a FAIL report)")
	if err := fs.Parse(args); err != nil {
		return flagExit(err)
	}
	seedSet := false
	fs.Visit(func(f *flag.Flag) { seedSet = seedSet || f.Name == "seed" })
	if !seedSet {
		fmt.Fprintln(stderr, "hookfuzz: replay needs --seed (copy it from a FAIL report)")
		return 2
	}
	r, err := newRunner(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "hookfuzz:", err)
		return 2
	}
	f, err := r.RunSeed(ctx, *seed)
	if err != nil {
		fmt.Fprintln(stderr, "hookfuzz:", err)
		return 2
	}
	if f != nil {
		report.Failure(stdout, f, *configPath)
		return 1
	}
	report.Pass(stdout, *seed, 1, len(r.Config.Invariants))
	return 0
}

func newRunner(configPath string) (*runner.Runner, error) {
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	return &runner.Runner{
		Config: cfg,
		Target: &target.Client{
			WebhookURL: cfg.Target.WebhookURL,
			StateURL:   cfg.Target.StateURL,
			ResetURL:   cfg.Target.ResetURL,
			Secret:     cfg.Target.SigningSecret,
		},
	}, nil
}

func flagExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}
```

- [ ] **Step 4: Run all Go tests**

Run: `go test ./... && go vet ./...`
Expected: every package `ok`, and no vet output.

- [ ] **Step 5: Commit**

```bash
git add cmd
git commit -m "feat: add hookfuzz run and replay commands"
```

---

### Task 12: Sample shop (Node/Express)

**Files:**
- Create: `sample-app/package.json`, `sample-app/store.js`, `sample-app/handlers.js`, `sample-app/app.js`, `sample-app/server.js`
- Test: `sample-app/test/app.test.js`

**Interfaces:**
- Consumes: the wire contract only. It accepts `POST /webhook` with `Stripe-Signature`, `GET /__hookfuzz/state`, and `POST /__hookfuzz/reset`.
- Produces:
  - State JSON: `{"shipments":[{order_id, session_id, amount}], "charges":[{id, captured, refunded}], "subscriptions":[{id, status}]}`
  - `createApp({ mode: 'buggy' | 'fixed', secret })` returns an Express app and throws on an unknown mode
  - Env for `server.js`: `PORT` (default 4242), `HOOKFUZZ_APP_MODE` (default `buggy`), `STRIPE_WEBHOOK_SECRET` (default `whsec_test_hookfuzz`)
  - Planted bugs (buggy mode): (1) ships on every delivery; (2) `charge.succeeded` overwrites and an early `charge.refunded` is dropped; (3) the last delivery wins for subscription status

- [ ] **Step 1: Scaffold and install**

`sample-app/package.json`:

```json
{
  "name": "hookfuzz-sample-shop",
  "version": "0.1.0",
  "private": true,
  "description": "A tiny shop with planted webhook bugs, for hookfuzz to find.",
  "main": "server.js",
  "scripts": {
    "start": "node server.js",
    "test": "node --test"
  }
}
```

```bash
cd sample-app && npm install express@4 stripe@22
```

- [ ] **Step 2: Write the failing test**

`sample-app/test/app.test.js`:

```js
const test = require('node:test');
const assert = require('node:assert/strict');
const Stripe = require('stripe');
const { createApp } = require('../app');

const secret = 'whsec_unit_test';
const stripe = new Stripe('sk_test_unused');

async function startShop(t, mode) {
  const server = createApp({ mode, secret }).listen(0);
  await new Promise((resolve) => server.once('listening', resolve));
  t.after(() => {
    server.closeAllConnections();
    server.close();
  });
  const base = `http://127.0.0.1:${server.address().port}`;
  return {
    async send(event, { signature } = {}) {
      const payload = JSON.stringify(event);
      const header = signature ?? stripe.webhooks.generateTestHeaderString({ payload, secret });
      const res = await fetch(`${base}/webhook`, {
        method: 'POST',
        headers: { 'content-type': 'application/json', 'stripe-signature': header },
        body: payload,
      });
      return res.status;
    },
    async state() {
      return (await fetch(`${base}/__hookfuzz/state`)).json();
    },
    async reset() {
      return (await fetch(`${base}/__hookfuzz/reset`, { method: 'POST' })).status;
    },
  };
}

let seq = 0;
function event(type, created, object) {
  seq += 1;
  return { id: `evt_${seq}`, object: 'event', type, created, api_version: '2024-06-20', livemode: false, data: { object } };
}
const session = (id) => ({ id, object: 'checkout.session', amount_total: 1000 });
const charge = (id, amountRefunded) => ({ id, object: 'charge', amount: 1000, amount_captured: 1000, amount_refunded: amountRefunded });
const sub = (id, status) => ({ id, object: 'subscription', status });

test('rejects a bad signature', async (t) => {
  const shop = await startShop(t, 'buggy');
  const status = await shop.send(event('checkout.session.completed', 1, session('cs_1')), { signature: 't=1,v1=deadbeef' });
  assert.equal(status, 400);
  assert.deepEqual((await shop.state()).shipments, []);
});

test('buggy: a redelivered checkout ships twice', async (t) => {
  const shop = await startShop(t, 'buggy');
  const ev = event('checkout.session.completed', 1, session('cs_1'));
  assert.equal(await shop.send(ev), 200);
  assert.equal(await shop.send(ev), 200);
  assert.equal((await shop.state()).shipments.length, 2);
});

test('fixed: a redelivered checkout ships once', async (t) => {
  const shop = await startShop(t, 'fixed');
  const ev = event('checkout.session.completed', 1, session('cs_1'));
  await shop.send(ev);
  await shop.send(ev);
  assert.equal((await shop.state()).shipments.length, 1);
});

test('buggy: a refund that arrives before its charge is dropped', async (t) => {
  const shop = await startShop(t, 'buggy');
  await shop.send(event('charge.refunded', 2, charge('ch_1', 500)));
  await shop.send(event('charge.succeeded', 1, charge('ch_1', 0)));
  assert.deepEqual((await shop.state()).charges, [{ id: 'ch_1', captured: 1000, refunded: 0 }]);
});

test('fixed: a refund that arrives before its charge is kept', async (t) => {
  const shop = await startShop(t, 'fixed');
  await shop.send(event('charge.refunded', 2, charge('ch_1', 500)));
  await shop.send(event('charge.succeeded', 1, charge('ch_1', 0)));
  assert.deepEqual((await shop.state()).charges, [{ id: 'ch_1', captured: 1000, refunded: 500 }]);
});

test('buggy: a stale subscription update overwrites the newer one', async (t) => {
  const shop = await startShop(t, 'buggy');
  await shop.send(event('customer.subscription.updated', 3, sub('sub_1', 'past_due')));
  await shop.send(event('customer.subscription.created', 1, sub('sub_1', 'active')));
  assert.deepEqual((await shop.state()).subscriptions, [{ id: 'sub_1', status: 'active' }]);
});

test('fixed: a stale subscription update is ignored', async (t) => {
  const shop = await startShop(t, 'fixed');
  await shop.send(event('customer.subscription.updated', 3, sub('sub_1', 'past_due')));
  await shop.send(event('customer.subscription.created', 1, sub('sub_1', 'active')));
  assert.deepEqual((await shop.state()).subscriptions, [{ id: 'sub_1', status: 'past_due' }]);
});

test('reset clears all state, including processed event IDs', async (t) => {
  const shop = await startShop(t, 'fixed');
  const ev = event('checkout.session.completed', 1, session('cs_1'));
  await shop.send(ev);
  assert.equal(await shop.reset(), 200);
  assert.deepEqual(await shop.state(), { shipments: [], charges: [], subscriptions: [] });
  await shop.send(ev);
  assert.equal((await shop.state()).shipments.length, 1);
});

test('rejects an unknown mode', () => {
  assert.throws(() => createApp({ mode: 'chaotic', secret }), /unknown mode/);
});
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `cd sample-app && npm test`
Expected: FAIL with `Cannot find module '../app'`.

- [ ] **Step 4: Implement**

`sample-app/store.js`:

```js
// In-memory state for the sample shop. Real apps would use a database.
function createStore() {
  return {
    shipments: [], // { order_id, session_id, amount }
    charges: new Map(), // id -> { id, captured, refunded }
    subscriptions: new Map(), // id -> { id, status, last_event_created? }
    processedEvents: new Set(), // event IDs (fixed mode only)
  };
}

// The JSON shape hookfuzz reads from GET /__hookfuzz/state.
function snapshot(store) {
  return {
    shipments: store.shipments,
    charges: [...store.charges.values()].map(({ id, captured, refunded }) => ({ id, captured, refunded })),
    subscriptions: [...store.subscriptions.values()].map(({ id, status }) => ({ id, status })),
  };
}

module.exports = { createStore, snapshot };
```

`sample-app/handlers.js`:

```js
// Two versions of the same webhook handlers. `buggy` has the three planted
// bugs hookfuzz should find; `fixed` shows the standard fix for each.

function dispatch(table, store, event) {
  const handle = table[event.type];
  if (handle) handle(store, event);
}

function ship(store, session) {
  store.shipments.push({
    order_id: `ord_${store.shipments.length + 1}`,
    session_id: session.id,
    amount: session.amount_total,
  });
}

const buggyTable = {
  // BUG 1 (not idempotent): every delivery ships an order, so a redelivered
  // event ships the same checkout twice.
  'checkout.session.completed'(store, event) {
    ship(store, event.data.object);
  },

  // BUG 2 (order-dependent): charge.succeeded overwrites the stored charge,
  // and charge.refunded is dropped if the charge isn't stored yet. Either way
  // a refund that arrives "early" is lost.
  'charge.succeeded'(store, event) {
    const charge = event.data.object;
    store.charges.set(charge.id, { id: charge.id, captured: charge.amount_captured, refunded: charge.amount_refunded });
  },
  'charge.refunded'(store, event) {
    const charge = event.data.object;
    const existing = store.charges.get(charge.id);
    if (!existing) return;
    existing.refunded = charge.amount_refunded;
  },

  // BUG 3 (stale overwrite): the last delivery wins, even when it is older.
  'customer.subscription.created': setSubscriptionStatus,
  'customer.subscription.updated': setSubscriptionStatus,
  'customer.subscription.deleted': setSubscriptionStatus,
};

function setSubscriptionStatus(store, event) {
  const sub = event.data.object;
  store.subscriptions.set(sub.id, { id: sub.id, status: sub.status });
}

const fixedTable = {
  'checkout.session.completed'(store, event) {
    ship(store, event.data.object);
  },
  'charge.succeeded': mergeCharge,
  'charge.refunded': mergeCharge,
  'customer.subscription.created': applySubscriptionIfNewer,
  'customer.subscription.updated': applySubscriptionIfNewer,
  'customer.subscription.deleted': applySubscriptionIfNewer,
};

// FIX 2: merge instead of overwrite, and accept a refund for a charge we
// haven't seen yet. amount_refunded only grows, so the larger value is newer.
function mergeCharge(store, event) {
  const charge = event.data.object;
  const existing = store.charges.get(charge.id);
  store.charges.set(charge.id, {
    id: charge.id,
    captured: charge.amount_captured,
    refunded: Math.max(existing ? existing.refunded : 0, charge.amount_refunded),
  });
}

// FIX 3: compare event.created, not arrival order, and ignore older events.
function applySubscriptionIfNewer(store, event) {
  const sub = event.data.object;
  const existing = store.subscriptions.get(sub.id);
  if (existing && existing.last_event_created >= event.created) return;
  store.subscriptions.set(sub.id, { id: sub.id, status: sub.status, last_event_created: event.created });
}

module.exports = {
  buggy: {
    handle(store, event) {
      dispatch(buggyTable, store, event);
    },
  },
  fixed: {
    handle(store, event) {
      // FIX 1: remember processed event IDs and skip redeliveries.
      if (store.processedEvents.has(event.id)) return;
      store.processedEvents.add(event.id);
      dispatch(fixedTable, store, event);
    },
  },
};
```

`sample-app/app.js`:

```js
const express = require('express');
const Stripe = require('stripe');
const { createStore, snapshot } = require('./store');
const handlers = require('./handlers');

function createApp({ mode = 'buggy', secret }) {
  const handler = handlers[mode];
  if (!handler) throw new Error(`unknown mode "${mode}"; use "buggy" or "fixed"`);
  const stripe = new Stripe('sk_test_unused'); // only used for webhook verification
  let store = createStore();
  const app = express();

  // Signature verification needs the raw body, so don't parse JSON first.
  app.post('/webhook', express.raw({ type: 'application/json' }), (req, res) => {
    let event;
    try {
      event = stripe.webhooks.constructEvent(req.body, req.get('stripe-signature'), secret);
    } catch (err) {
      return res.status(400).send(`signature verification failed: ${err.message}`);
    }
    handler.handle(store, event);
    res.json({ received: true });
  });

  // Test-only inspection endpoints used by hookfuzz. Never expose these in production.
  app.get('/__hookfuzz/state', (req, res) => res.json(snapshot(store)));
  app.post('/__hookfuzz/reset', (req, res) => {
    store = createStore();
    res.json({ reset: true });
  });

  return app;
}

module.exports = { createApp };
```

`sample-app/server.js`:

```js
const { createApp } = require('./app');

const port = Number(process.env.PORT || 4242);
const mode = process.env.HOOKFUZZ_APP_MODE || 'buggy';
const secret = process.env.STRIPE_WEBHOOK_SECRET || 'whsec_test_hookfuzz';

createApp({ mode, secret }).listen(port, () => {
  console.log(`sample shop (${mode}) listening on http://localhost:${port}`);
});
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `cd sample-app && npm test`
Expected: `# pass 9`, `# fail 0`

- [ ] **Step 6: Commit**

```bash
git add sample-app/package.json sample-app/package-lock.json sample-app/*.js sample-app/test
git commit -m "feat: add sample shop with three planted webhook bugs and fixes"
```

---

### Task 13: End-to-end tests against the real sample app

**Files:**
- Create: `e2e/e2e_test.go` (build tag `e2e`)

**Interfaces:**
- Consumes: `runner.Runner`, `config.Config`, `fault.Config`, `invariant.Spec`, `target.Client`, and the sample app from Task 12 (`node sample-app/server.js` with `PORT`, `HOOKFUZZ_APP_MODE`, `STRIPE_WEBHOOK_SECRET`)
- Produces: proof of the spec's claims. hookfuzz finds each planted bug and shrinks it to the expected 2-delivery reproduction. It reports no failures against the fixed app. A seed replays identically. These tests also prove the Go signer is accepted by stripe-node.

- [ ] **Step 1: Write the tests**

`e2e/e2e_test.go`:

```go
//go:build e2e

// Package e2e runs hookfuzz against the real Node sample shop.
// Run with: make e2e   (or: cd sample-app && npm install; go test -tags e2e ./e2e/ -v)
package e2e

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/shaurmittal/hookfuzz/internal/config"
	"github.com/shaurmittal/hookfuzz/internal/event"
	"github.com/shaurmittal/hookfuzz/internal/fault"
	"github.com/shaurmittal/hookfuzz/internal/invariant"
	"github.com/shaurmittal/hookfuzz/internal/runner"
	"github.com/shaurmittal/hookfuzz/internal/target"
)

const secret = "whsec_e2e_secret"

var (
	oneShipment = invariant.Spec{
		Name: "one_shipment_per_session", Kind: invariant.KindMaxCountPerKey,
		Collection: "shipments", Key: "session_id", Max: 1,
	}
	refundLECaptured = invariant.Spec{
		Name: "refund_le_captured", Kind: invariant.KindFieldLE,
		Collection: "charges", Left: "refunded", Right: "captured",
	}
	refundMatchesLatest = invariant.Spec{
		Name: "refund_matches_latest_charge_event", Kind: invariant.KindMatchesLatestEvent,
		Collection: "charges", Field: "refunded",
		EventTypes: []string{"charge.succeeded", "charge.refunded"}, EventField: "amount_refunded",
	}
	subscriptionMatchesLatest = invariant.Spec{
		Name: "subscription_status_matches_latest_event", Kind: invariant.KindMatchesLatestEvent,
		Collection: "subscriptions", Field: "status",
		EventTypes: []string{"customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted"},
		EventField: "status",
	}
	faults = fault.Config{Duplicate: 0.2, ReorderWindow: 4, Delay: 0.15, TimeoutRetry: 0.1}
)

// startApp launches the sample shop on a free port and returns its base URL.
func startApp(t *testing.T, mode string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	cmd := exec.Command("node", "server.js")
	cmd.Dir = filepath.Join("..", "sample-app")
	cmd.Env = append(os.Environ(), fmt.Sprintf("PORT=%d", port), "HOOKFUZZ_APP_MODE="+mode, "STRIPE_WEBHOOK_SECRET="+secret)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting sample app: %v", err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get(base + "/__hookfuzz/state")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return base
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("sample app did not start on %s (did you run npm install in sample-app?)", base)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func newRunner(base string, scenarios []string, specs ...invariant.Spec) *runner.Runner {
	return &runner.Runner{
		Config: config.Config{Scenarios: scenarios, ScenarioSize: 5, Faults: faults, Invariants: specs},
		Target: &target.Client{
			WebhookURL: base + "/webhook",
			StateURL:   base + "/__hookfuzz/state",
			ResetURL:   base + "/__hookfuzz/reset",
			Secret:     secret,
		},
	}
}

func mustFail(t *testing.T, r *runner.Runner, invariantName string) *runner.Failure {
	t.Helper()
	f, n, err := r.Run(context.Background(), 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		t.Fatalf("no failure found in %d runs", n)
	}
	if f.Invariant != invariantName || len(f.Violations) == 0 {
		t.Fatalf("failure = %+v, want violations of %s", f, invariantName)
	}
	return f
}

func describe(evs []event.Event) []string {
	var out []string
	for _, ev := range evs {
		out = append(out, fmt.Sprintf("%s %s %s created=%d", ev.Type, ev.ObjectID(), ev.ID, ev.Created))
	}
	return out
}

func TestFindsNonIdempotentFulfillment(t *testing.T) {
	base := startApp(t, "buggy")
	f := mustFail(t, newRunner(base, []string{"checkout"}, oneShipment), oneShipment.Name)
	m := f.Minimal
	if len(m) != 2 || m[0].Type != "checkout.session.completed" || m[0].ID != m[1].ID {
		t.Fatalf("minimal = %v, want the same checkout.session.completed twice", describe(m))
	}
}

func TestFindsRefundBeforeCharge(t *testing.T) {
	base := startApp(t, "buggy")
	f := mustFail(t, newRunner(base, []string{"refund"}, refundMatchesLatest), refundMatchesLatest.Name)
	m := f.Minimal
	if len(m) != 2 || m[0].Type != "charge.refunded" || m[1].Type != "charge.succeeded" || m[0].ObjectID() != m[1].ObjectID() {
		t.Fatalf("minimal = %v, want charge.refunded then charge.succeeded for one charge", describe(m))
	}
}

func TestFindsStaleSubscriptionUpdate(t *testing.T) {
	base := startApp(t, "buggy")
	f := mustFail(t, newRunner(base, []string{"subscription"}, subscriptionMatchesLatest), subscriptionMatchesLatest.Name)
	m := f.Minimal
	if len(m) != 2 || m[0].ObjectID() != m[1].ObjectID() || m[0].Created <= m[1].Created ||
		m[0].Data.Object["status"] == m[1].Data.Object["status"] {
		t.Fatalf("minimal = %v, want a newer event then an older one with a different status, same subscription", describe(m))
	}
}

func TestFixedAppHasNoFalsePositives(t *testing.T) {
	base := startApp(t, "fixed")
	r := newRunner(base, []string{"checkout", "refund", "subscription"},
		oneShipment, refundLECaptured, refundMatchesLatest, subscriptionMatchesLatest)
	f, n, err := r.Run(context.Background(), 1, 200)
	if err != nil {
		t.Fatal(err)
	}
	if f != nil {
		t.Fatalf("false positive at seed %d: %s %v\nminimal: %v", f.Seed, f.Invariant, f.Violations, describe(f.Minimal))
	}
	if n != 200 {
		t.Fatalf("ran %d seeds, want 200", n)
	}
}

func TestReplayIsDeterministic(t *testing.T) {
	base := startApp(t, "buggy")
	r := newRunner(base, []string{"checkout", "refund", "subscription"},
		oneShipment, refundLECaptured, refundMatchesLatest, subscriptionMatchesLatest)
	first, _, err := r.Run(context.Background(), 1, 100)
	if err != nil || first == nil {
		t.Fatalf("setup: %v, %v", first, err)
	}
	again, err := r.RunSeed(context.Background(), first.Seed)
	if err != nil || again == nil {
		t.Fatalf("replay: %v, %v", again, err)
	}
	if !slices.Equal(describe(first.Minimal), describe(again.Minimal)) {
		t.Fatalf("seed %d shrank differently:\n%v\n%v", first.Seed, describe(first.Minimal), describe(again.Minimal))
	}
}
```

- [ ] **Step 2: Run the e2e tests**

Run: `(cd sample-app && npm install) && go test -tags e2e ./e2e/ -v`
Expected: all 5 tests PASS. If `TestFixedAppHasNoFalsePositives` fails, the output names the seed and minimal schedule. Treat it as a real bug in either the fixed handlers or the invariant logic, and debug it with superpowers:systematic-debugging. Don't loosen the test.

- [ ] **Step 3: Confirm plain `go test ./...` still skips e2e**

Run: `go test ./...`
Expected: all `ok`. The `e2e` package shows `[no test files]` without the tag.

- [ ] **Step 4: Commit**

```bash
git add e2e
git commit -m "test: add e2e tests proving hookfuzz finds each planted bug and has no false positives"
```

---

### Task 14: Example config, Makefile, README, demo

**Files:**
- Create: `hookfuzz.yaml`, `Makefile`, `README.md`, `demo.tape`
- Test: `internal/config/example_test.go`

**Interfaces:**
- Consumes: everything above
- Produces: a working quickstart that someone who has never seen the repo can follow

- [ ] **Step 1: Write the failing test**

`internal/config/example_test.go`:

```go
package config

import (
	"path/filepath"
	"testing"
)

func TestExampleConfigLoads(t *testing.T) {
	c, err := Load(filepath.Join("..", "..", "hookfuzz.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Scenarios) != 3 || len(c.Invariants) != 4 {
		t.Fatalf("example config has %d scenarios and %d invariants, want 3 and 4", len(c.Scenarios), len(c.Invariants))
	}
}
```

Run: `go test ./internal/config/ -run Example`
Expected: FAIL (`no such file or directory`).

- [ ] **Step 2: Write `hookfuzz.yaml`**

```yaml
# hookfuzz config for the sample shop in ./sample-app.
# Start the app first:  cd sample-app && npm start
target:
  webhook_url: http://localhost:4242/webhook
  state_url: http://localhost:4242/__hookfuzz/state
  reset_url: http://localhost:4242/__hookfuzz/reset
  signing_secret: whsec_test_hookfuzz

scenarios: [checkout, refund, subscription]
scenario_size: 5 # objects per scenario per run

faults:
  duplicate: 0.2 # chance each event is delivered twice in a row
  reorder_window: 4 # shuffle deliveries within windows of this size
  delay: 0.15 # chance a delivery is pushed 2-8 deliveries later
  timeout_retry: 0.1 # chance an event "times out" and is redelivered later

invariants:
  # A checkout session must never ship more than one order.
  - name: one_shipment_per_session
    kind: max_count_per_key
    collection: shipments
    key: session_id
    max: 1

  # Never record more refunded than captured.
  - name: refund_le_captured
    kind: field_le
    collection: charges
    left: refunded
    right: captured

  # The stored refund must match the newest charge event (by created, not arrival).
  - name: refund_matches_latest_charge_event
    kind: matches_latest_event
    collection: charges
    field: refunded
    event_types: [charge.succeeded, charge.refunded]
    event_field: amount_refunded

  # Final subscription status must match the newest subscription event (by created).
  - name: subscription_status_matches_latest_event
    kind: matches_latest_event
    collection: subscriptions
    field: status
    event_types: [customer.subscription.created, customer.subscription.updated, customer.subscription.deleted]
    event_field: status
```

Run: `go test ./internal/config/`
Expected: `ok`

- [ ] **Step 3: Write the `Makefile`** (recipe lines must start with a real tab)

```make
.PHONY: build test e2e demo

build:
	go build -o hookfuzz ./cmd/hookfuzz

test:
	go test ./...
	cd sample-app && npm test

e2e:
	cd sample-app && npm install
	go test -tags e2e ./e2e/ -v

demo: build
	vhs demo.tape
```

Run: `make build && make test`
Expected: the binary `./hookfuzz` exists. All Go packages report `ok`, and Node reports `# fail 0`.

- [ ] **Step 4: Smoke-test the real CLI against the sample app**

```bash
(cd sample-app && npm start) &
sleep 2
./hookfuzz run; echo "exit=$?"
```

Expected: a `FAIL invariant "..." (seed N)` report with `Minimal reproduction (2 of ~40 deliveries)` and `exit=1`. **Save this exact output** for the README.

```bash
./hookfuzz replay --seed N; echo "exit=$?"   # N from the report
```

Expected: identical output and `exit=1`.

```bash
kill %1
(cd sample-app && HOOKFUZZ_APP_MODE=fixed npm start) &
sleep 2
./hookfuzz run; echo "exit=$?"
kill %1
```

Expected: `PASS 100 runs (seeds 1..100), 4 invariants held` and `exit=0`.

- [ ] **Step 5: Write `README.md`**

Paste the real output from Step 4 where the file says `<paste>`.

````markdown
# hookfuzz

Chaos testing for payment webhook handlers.

hookfuzz finds bugs in payment integrations by deliberately delivering webhooks
twice, out of order, late, or after failures, then checking that your app still
ends up in a correct state. When it finds a bug, it shrinks it to the smallest
sequence of events that reproduces it.

![demo](demo.gif)

## Why

Stripe delivers webhooks **at least once** and **in no particular order**. The
same event can arrive twice, a refund can arrive before its charge, and a stale
`subscription.updated` can land after a newer one. Most integrations are only
tested on the happy path, so these bugs are usually found by customers.
`stripe trigger` sends one event, once, in order. hookfuzz is adversarial and
then checks correctness.

## Quickstart (sample shop)

Requires Go 1.22+ and Node 22+.

```bash
make build
cd sample-app && npm install && npm start &   # buggy mode on :4242
./hookfuzz run
```

```
<paste the FAIL output from a real run>
```

Try the fixed version: `HOOKFUZZ_APP_MODE=fixed npm start`, then `./hookfuzz run`
reports `PASS`.

## How it works

1. **Scenarios** generate a realistic event history (`checkout`, `refund`,
   `subscription`). Payloads are Stripe-shaped and signed with a valid
   `Stripe-Signature`, so your real handler (including `constructEvent`) runs
   unmodified.
2. **Faults** turn that history into a seeded delivery schedule with duplicates,
   reordering within a window, delays, and redeliveries after simulated timeouts.
   Any non-2xx response is retried the way Stripe does it (3 attempts).
3. **Invariants** from `hookfuzz.yaml` are checked against your app's state after
   each run. Ordering rules compare against event `created` timestamps, not arrival order.
4. **Shrinking** (delta debugging) removes deliveries until the failure is
   minimal, then prints the timeline and the seed. `hookfuzz replay --seed N`
   reproduces it exactly.

## Using it on your app

Add two test-only endpoints to your app (never expose them in production):

- `GET /__hookfuzz/state` returns a JSON object mapping a collection name to an
  array of objects, e.g. `{"shipments": [{"session_id": "cs_1"}]}`
- `POST /__hookfuzz/reset` clears all state (hookfuzz calls it before every run
  and every shrink attempt, so make it fast)

Point `hookfuzz.yaml` at your app, set `signing_secret` to the webhook secret
your app verifies with, and declare invariants.

### Invariant kinds

| kind | checks | fields |
|---|---|---|
| `max_count_per_key` | no `key` value appears more than `max` times in `collection` | `key`, `max` |
| `field_le` | for every object, `left <= right` | `left`, `right` |
| `matches_latest_event` | each object's `field` equals `event_field` on the newest (by `created`) acknowledged event for that object | `field`, `event_field`, `event_types` |

A missing collection or field counts as a violation, so a typo can't hide a bug.

### Commands

```
hookfuzz run    [--config hookfuzz.yaml] [--seed 1] [--runs 100]
hookfuzz replay --seed N [--config hookfuzz.yaml]
```

Exit codes: `0` all invariants held, `1` an invariant failed, `2` usage or setup error.

## Limitations

- Deliveries are sent one at a time so runs are reproducible. Concurrency races
  inside a single handler are out of scope.
- One webhook endpoint per config.
- Only Stripe-shaped events.

## Development

```bash
make test   # Go unit tests + sample app tests
make e2e    # hookfuzz against the real sample app: finds all 3 bugs, no false positives on the fixed app
```
````

- [ ] **Step 6: Record the demo GIF**

`demo.tape`:

```
Output demo.gif
Set FontSize 16
Set Width 1200
Set Height 520
Set TypingSpeed 40ms

Hide
Type "cd sample-app && npm start >/dev/null 2>&1 & sleep 2; clear"
Enter
Sleep 3s
Show

Type "./hookfuzz run"
Enter
Sleep 5s
```

The `Hide` block starts the app from the repo root, then the visible part runs `./hookfuzz` from the repo root. If the `cd` leaves the shell in `sample-app`, change the hidden line to `(cd sample-app && npm start >/dev/null 2>&1 &); sleep 2; clear`.

```bash
brew install vhs
make demo
```

Expected: `demo.gif` shows the FAIL report. Open it and check that it is readable.

- [ ] **Step 7: Final verification and commit**

```bash
gofmt -l .           # expect no output
go vet ./...         # expect no output
make test && make e2e
git add hookfuzz.yaml Makefile README.md demo.tape demo.gif internal/config/example_test.go
git commit -m "docs: add example config, README quickstart, Makefile, and demo GIF"
```

---

## Self-Review Notes

- **Spec coverage:** scenario generation → T2. Signing → T1/T4, with stripe-node compatibility proven in T13. The 4 faults → T3 (duplicate, reorder, delay, timeout_retry) plus the engine retry on non-2xx in T5. Invariant checking via the inspection endpoint → T4/T6. Invariants in a config file → T8/T14. Shrinking → T7/T9. Timeline, seed, and replay → T10/T11. Sample app with 3 planted bugs and a fixed version → T12. "Finds each bug and shrinks to the expected minimal sequence" and "no false positives" → T13. README and demo GIF → T14.
- **Minimal sequences (why T13 can assert exactly 2 deliveries):** a checkout failure needs two deliveries of one session. A refund failure needs `charge.refunded` delivered before `charge.succeeded` for one charge. A subscription failure needs the newest-created event and the last-delivered event to differ in status. ddmin returns a 1-minimal result, and in each case any extra delivery can be removed without fixing the failure, so the result is exactly those 2.
- **Type consistency:** checked `Schedule`/`Minimal` as `[]event.Event` throughout (no Delivery wrapper), `target.State`, `invariant.Violation{Invariant, Message}`, `runner.Failure` fields, `report.Pass(w, start, runs, invariants)`, and `Kind*` constant names.
