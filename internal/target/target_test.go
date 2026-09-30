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

	"github.com/shauryamittal/hookfuzz/internal/event"
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
