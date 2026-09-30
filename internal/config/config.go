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

	"github.com/shauryamittal/hookfuzz/internal/fault"
	"github.com/shauryamittal/hookfuzz/internal/invariant"
	"github.com/shauryamittal/hookfuzz/internal/scenario"
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
		// A misspelled event type would silently filter out every event.
		for _, typ := range inv.EventTypes {
			if !slices.Contains(scenario.EventTypes(), typ) {
				return fmt.Errorf("invariant %q: unknown event type %q (known: %s)",
					inv.Name, typ, strings.Join(scenario.EventTypes(), ", "))
			}
		}
		seen[inv.Name] = true
	}
	return nil
}
