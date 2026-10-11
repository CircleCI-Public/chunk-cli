package sandboximage

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestDecideRepair(t *testing.T) {
	yes, no := true, false
	tests := []struct {
		name string
		s    RepairState
		want RepairAction
	}{
		{"not interactive", RepairState{HaveClaude: true}, RepairReport},
		{"no claude", RepairState{Interactive: true}, RepairReport},
		{"not asked yet", RepairState{Interactive: true, HaveClaude: true}, RepairAsk},
		{"declined", RepairState{Interactive: true, HaveClaude: true, Consent: &no}, RepairReport},
		{"consented", RepairState{Interactive: true, HaveClaude: true, Consent: &yes}, RepairAttempt},
		{"second attempt", RepairState{Interactive: true, HaveClaude: true, Consent: &yes, Attempts: 2}, RepairAttempt},
		{"used up", RepairState{Interactive: true, HaveClaude: true, Consent: &yes, Attempts: MaxRepairAttempts}, RepairGiveUp},
		{"never asks without a terminal even with consent", RepairState{HaveClaude: true, Consent: &yes}, RepairReport},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, DecideRepair(tt.s), tt.want)
		})
	}
}

func TestOnlyChanged(t *testing.T) {
	tests := []struct {
		name          string
		before, after string
		want          bool
	}{
		{"dockerfile edited", "", " M Dockerfile.chunk\n", true},
		{"dockerfile already dirty", " M Dockerfile.chunk\n", " M Dockerfile.chunk\n", true},
		{"other file touched", "", " M Dockerfile.chunk\n M main.go\n", false},
		{"other file only", "", "?? notes.txt\n", false},
		{"preexisting change kept", " M main.go\n", " M Dockerfile.chunk\n M main.go\n", true},
		{"preexisting change reverted", " M main.go\n", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, OnlyChanged(tt.before, tt.after, "Dockerfile.chunk"), tt.want)
		})
	}
}

// fakeFixer rewrites files the way a claude run would.
type fakeFixer struct {
	calls int
	edit  func(dir string, call int) error
}

func (f *fakeFixer) Fix(_ context.Context, dir, _ string) error {
	f.calls++
	return f.edit(dir, f.calls)
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "Dockerfile.chunk"), []byte("FROM x\n"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		assert.NilError(t, err, string(out))
	}
	return dir
}

func editDockerfile(dir string, call int) error {
	return os.WriteFile(filepath.Join(dir, "Dockerfile.chunk"), []byte("FROM y\n# attempt "+string(rune('0'+call))+"\n"), 0o644)
}

func TestRunRepairFixesOnSecondAttempt(t *testing.T) {
	dir := gitRepo(t)
	fixer := &fakeFixer{edit: editDockerfile}
	retries := 0
	res, err := RunRepair(context.Background(), RepairOptions{
		Dir: dir, Dockerfile: "Dockerfile.chunk", Failure: "bad package", Fixer: fixer,
		Retry: func(context.Context) (string, error) {
			retries++
			if retries < 2 {
				return "still broken", nil
			}
			return "", nil
		},
	})
	assert.NilError(t, err)
	assert.Assert(t, res.Fixed)
	assert.Equal(t, res.Attempts, 2)
}

func TestRunRepairGivesUpAfterThreeAttempts(t *testing.T) {
	dir := gitRepo(t)
	fixer := &fakeFixer{edit: editDockerfile}
	res, err := RunRepair(context.Background(), RepairOptions{
		Dir: dir, Dockerfile: "Dockerfile.chunk", Failure: "bad", Fixer: fixer,
		Retry: func(context.Context) (string, error) { return "nope", nil },
	})
	assert.NilError(t, err)
	assert.Assert(t, !res.Fixed)
	assert.Equal(t, res.Attempts, MaxRepairAttempts)
	assert.Equal(t, fixer.calls, MaxRepairAttempts)
	assert.Equal(t, res.Failure, "nope")
}

func TestRunRepairStopsWhenOtherFilesChange(t *testing.T) {
	dir := gitRepo(t)
	fixer := &fakeFixer{edit: func(dir string, _ int) error {
		return os.WriteFile(filepath.Join(dir, "main.go"), []byte("changed"), 0o644)
	}}
	_, err := RunRepair(context.Background(), RepairOptions{
		Dir: dir, Dockerfile: "Dockerfile.chunk", Fixer: fixer,
		Retry: func(context.Context) (string, error) { t.Fatal("must not rebuild"); return "", nil },
	})
	assert.Assert(t, errors.Is(err, ErrOtherFilesChanged), "%v", err)
	assert.Equal(t, fixer.calls, 1)
}

func TestRepairPromptStatesTheRules(t *testing.T) {
	p := RepairPrompt("Dockerfile.chunk", "E: Unable to locate package nodjs")
	assert.Assert(t, strings.Contains(p, "nodjs"))
	assert.Assert(t, strings.Contains(p, "Edit only Dockerfile.chunk"))
	assert.Assert(t, strings.Contains(p, "agent kit"))
}
