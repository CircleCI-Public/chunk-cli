package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunGhNotFound(t *testing.T) {
	t.Setenv("PATH", "/nonexistent")
	err := Run()
	if err == nil {
		t.Fatal("expected error when gh is not on PATH")
	}
	if !strings.Contains(err.Error(), "gh CLI not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// writeFakeGh creates a fake "gh" script in a temp directory that
// records its arguments and returns the given exit code.
// It returns the directory containing the fake.
func writeFakeGh(t *testing.T, authExit, upgradeExit int) string {
	t.Helper()
	dir := t.TempDir()

	if runtime.GOOS == "windows" {
		t.Skip("fake gh script not supported on Windows")
	}

	// The fake gh script inspects its arguments to decide behavior.
	// "auth status" → authExit
	// "extension upgrade ..." → upgradeExit
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "auth" ] && [ "$2" = "status" ]; then
  exit %d
fi
if [ "$1" = "extension" ] && [ "$2" = "upgrade" ]; then
  exit %d
fi
exit 0
`, authExit, upgradeExit)
	ghPath := filepath.Join(dir, "gh")
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRunGhNotAuthenticated(t *testing.T) {
	dir := writeFakeGh(t, 1, 0)
	t.Setenv("PATH", dir)

	err := Run()
	if err == nil {
		t.Fatal("expected error when gh is not authenticated")
	}
	if !strings.Contains(err.Error(), "not authenticated") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunSuccess(t *testing.T) {
	dir := writeFakeGh(t, 0, 0)
	t.Setenv("PATH", dir)

	err := Run()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunUpgradeFails(t *testing.T) {
	dir := writeFakeGh(t, 0, 1)
	t.Setenv("PATH", dir)

	err := Run()
	if err == nil {
		t.Fatal("expected error when upgrade command fails")
	}
	if !strings.Contains(err.Error(), "upgrade failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}
