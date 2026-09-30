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
