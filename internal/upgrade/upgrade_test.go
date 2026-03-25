package upgrade

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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

func TestRun(t *testing.T) {
	tests := []struct {
		name        string
		path        string // override PATH; empty means use writeFakeGh
		authExit    int
		upgradeExit int
		wantErr     bool
		errContains string
	}{
		{
			name:        "gh not found",
			path:        "/nonexistent",
			wantErr:     true,
			errContains: "gh CLI not found",
		},
		{
			name:        "not authenticated",
			authExit:    1,
			wantErr:     true,
			errContains: "not authenticated",
		},
		{
			name: "success",
		},
		{
			name:        "upgrade fails",
			upgradeExit: 1,
			wantErr:     true,
			errContains: "upgrade failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.path != "" {
				t.Setenv("PATH", tt.path)
			} else {
				dir := writeFakeGh(t, tt.authExit, tt.upgradeExit)
				t.Setenv("PATH", dir)
			}

			err := Run()
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("expected error containing %q, got: %v", tt.errContains, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
