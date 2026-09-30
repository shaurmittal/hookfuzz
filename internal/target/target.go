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

	"github.com/shauryamittal/hookfuzz/internal/event"
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
