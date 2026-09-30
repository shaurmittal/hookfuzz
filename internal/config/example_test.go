package config

import (
	"path/filepath"
	"testing"
)

func TestExampleConfigLoads(t *testing.T) {
	c, err := Load(filepath.Join("..", "..", "hookfuzz.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Scenarios) != 3 || len(c.Invariants) != 4 {
		t.Fatalf("example config has %d scenarios and %d invariants, want 3 and 4", len(c.Scenarios), len(c.Invariants))
	}
}
