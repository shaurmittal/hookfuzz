// Package invariant checks user-declared correctness rules against the app's
// state after a delivery run.
package invariant

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/shauryamittal/hookfuzz/internal/event"
	"github.com/shauryamittal/hookfuzz/internal/target"
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
	var order []string // object IDs in first-acknowledged order, for stable output
	for _, ev := range acked {
		if len(s.EventTypes) > 0 && !slices.Contains(s.EventTypes, ev.Type) {
			continue
		}
		// Newest by created, never by arrival. On a tie the first acknowledged wins.
		id := ev.ObjectID()
		cur, ok := latest[id]
		if !ok {
			order = append(order, id)
		}
		if !ok || ev.Created > cur.Created {
			latest[id] = ev
		}
	}
	var out []Violation
	stored := map[string]bool{}
	for i, item := range items {
		idv, ok := item["id"]
		if !ok {
			out = append(out, s.violation("%s[%d] has no field \"id\"", s.Collection, i))
			continue
		}
		stored[norm(idv)] = true
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
	// An object the app acknowledged events for but never stored was lost.
	for _, id := range order {
		if !stored[id] {
			ev := latest[id]
			out = append(out, s.violation("%s has acknowledged events but is absent from %s (latest event %s, %s, says %s = %v)",
				id, s.Collection, ev.ID, ev.Type, s.EventField, norm(ev.Data.Object[s.EventField])))
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
