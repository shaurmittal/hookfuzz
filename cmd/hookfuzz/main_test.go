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

	"github.com/shauryamittal/hookfuzz/internal/event"
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
