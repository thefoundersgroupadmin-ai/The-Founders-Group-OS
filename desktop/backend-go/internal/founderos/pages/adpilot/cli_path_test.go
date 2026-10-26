package adpilot

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeRunnerFindsOwnerInstallUnderLaunchdPath(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(bin), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s' '{\"result\":\"Fixture analysis\"}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")
	got, err := (ClaudeCLI{}).Complete(context.Background(), "Fixture; no real AI")
	if err != nil || got != "Fixture analysis" {
		t.Fatalf("service runner: %q %v", got, err)
	}
}
