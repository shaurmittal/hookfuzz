// Package runner executes seeded fault-injection runs and shrinks failures.
package runner

import (
	"context"
	"fmt"
	"slices"
	"strings"

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
	// Incomplete says why shrinking stopped early (an interrupt, or the app
	// going away). Minimal is then the smallest failing schedule found so far
	// and Violations come from the original run.
	Incomplete string
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
	vs, _, err := r.execute(ctx, schedule)
	return vs, err
}

func (r *Runner) execute(ctx context.Context, schedule []event.Event) ([]invariant.Violation, deliver.Result, error) {
	if err := r.Target.Reset(ctx); err != nil {
		return nil, deliver.Result{}, err
	}
	res, err := deliver.Run(ctx, r.Target, schedule)
	if err != nil {
		return nil, res, err
	}
	state, err := r.Target.State(ctx)
	if err != nil {
		return nil, res, err
	}
	return invariant.CheckAll(r.Config.Invariants, state, res.Acknowledged), res, nil
}

// RunSeed runs one seed. If an invariant fails, it shrinks the schedule to
// the smallest one that still breaks that same invariant. Nil means pass.
func (r *Runner) RunSeed(ctx context.Context, seed int64) (*Failure, error) {
	schedule, err := r.Schedule(seed)
	if err != nil {
		return nil, err
	}
	violations, res, err := r.execute(ctx, schedule)
	if err != nil {
		return nil, err
	}
	// If the app accepted nothing, every invariant holds vacuously. That is a
	// broken setup (usually the signing secret or webhook path), not a pass.
	if len(schedule) > 0 && len(res.Acknowledged) == 0 {
		return nil, fmt.Errorf("seed %d: the app acknowledged none of %d deliveries (%s); check target.signing_secret and target.webhook_url",
			seed, len(schedule), statusSummary(res.Rejected))
	}
	if len(violations) == 0 {
		return nil, nil
	}
	name := violations[0].Invariant
	minimal, err := shrink.Minimize(schedule, func(sub []event.Event) (bool, error) {
		vs, err := r.Execute(ctx, sub)
		return len(only(vs, name)) > 0, err
	})
	f := &Failure{Seed: seed, Invariant: name, Schedule: schedule, Minimal: minimal}
	if err == nil {
		var final []invariant.Violation
		if final, err = r.Execute(ctx, minimal); err == nil {
			f.Violations = only(final, name)
			return f, nil
		}
	}
	// The bug is already found; don't throw it away because shrinking stopped.
	f.Violations = only(violations, name)
	f.Incomplete = err.Error()
	return f, nil
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

// statusSummary formats rejected status counts, e.g. "HTTP 400 x132".
func statusSummary(rejected map[int]int) string {
	var codes []int
	for code := range rejected {
		codes = append(codes, code)
	}
	slices.Sort(codes)
	var parts []string
	for _, code := range codes {
		parts = append(parts, fmt.Sprintf("HTTP %d x%d", code, rejected[code]))
	}
	return strings.Join(parts, ", ")
}
