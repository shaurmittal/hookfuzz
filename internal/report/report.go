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
	if f.Incomplete != "" {
		fmt.Fprintf(w, "  (shrinking stopped early (%s); this reproduction may not be minimal)\n", f.Incomplete)
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
