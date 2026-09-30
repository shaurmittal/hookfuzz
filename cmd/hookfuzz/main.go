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

Exit codes: 0 all invariants held, 1 an invariant failed, 2 usage or setup error,
130 interrupted.
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
		return runError(ctx, err, stderr)
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
		return runError(ctx, err, stderr)
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

// runError reports a run that could not finish and picks its exit code.
func runError(ctx context.Context, err error, stderr io.Writer) int {
	if ctx.Err() != nil {
		fmt.Fprintln(stderr, "hookfuzz: interrupted")
		return 130
	}
	fmt.Fprintln(stderr, "hookfuzz:", err)
	return 2
}

func flagExit(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}
