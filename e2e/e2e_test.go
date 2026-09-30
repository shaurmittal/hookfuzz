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

	"github.com/shauryamittal/hookfuzz/internal/config"
	"github.com/shauryamittal/hookfuzz/internal/event"
	"github.com/shauryamittal/hookfuzz/internal/fault"
	"github.com/shauryamittal/hookfuzz/internal/invariant"
	"github.com/shauryamittal/hookfuzz/internal/runner"
	"github.com/shauryamittal/hookfuzz/internal/target"
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
