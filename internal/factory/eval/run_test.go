package eval

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// fakeReviewer records the change it is shown and reports one high finding
// on the first file the change touches.
const fakeReviewer = `mkdir -p "$HOME/seen"
git diff HEAD --binary > "$HOME/seen/$$"
file=$(git diff HEAD --name-only | head -1)
printf '{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":{"review":"ok","findings":[{"file":"%s","line":1,"severity":"high","body":"bad"}]}}\n' "$file"`

// localExec runs scripts with sh on this machine, as a sidecar would, with
// HOME pointed at home, where a fake claude is installed.
func localExec(home string) sidecar.Execer {
	return func(ctx context.Context, _ *sidecar.PoolEntry, script string, env map[string]string, onOutput circleci.OutputFn, _ func(string)) (int, error) {
		cmd := exec.CommandContext(ctx, "sh", "-c", script)
		cmd.Env = append(os.Environ(), "HOME="+home)
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		cmd.WaitDelay = 100 * time.Millisecond
		err := cmd.Run()
		onOutput(circleci.StreamStdout, []byte(stdout.String()))
		onOutput(circleci.StreamStderr, []byte(stderr.String()))
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), nil
		}
		return 0, err
	}
}

// withoutPkill skips the sweep for reviews left running from an earlier case.
// The fake sidecars are this machine, where it would kill any real
// `claude -p` the developer has running.
func withoutPkill(run sidecar.Execer) sidecar.Execer {
	return func(ctx context.Context, e *sidecar.PoolEntry, script string, env map[string]string, onOutput circleci.OutputFn, onSubmitted func(string)) (int, error) {
		if strings.Contains(script, "pkill") {
			return 0, nil
		}
		return run(ctx, e, script, env, onOutput, onSubmitted)
	}
}

func installFakeClaude(t *testing.T, home, body string) {
	t.Helper()
	bin := filepath.Join(home, ".local", "bin")
	assert.NilError(t, os.MkdirAll(bin, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncat > /dev/null\n"+body+"\n"), 0o755))
}

// mirror stands in for rsync: it makes the workspace's files dir's, leaving
// the workspace's .git alone.
func mirror(_ context.Context, e *sidecar.PoolEntry, dir string) error {
	entries, err := os.ReadDir(e.RepoPath)
	if err != nil {
		return err
	}
	for _, de := range entries {
		if de.Name() != ".git" {
			if err := os.RemoveAll(filepath.Join(e.RepoPath, de.Name())); err != nil {
				return err
			}
		}
	}
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if d.Name() == ".git" {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(e.RepoPath, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

type evalFixture struct {
	opts    Options
	home    string
	entries []*sidecar.PoolEntry
	patches map[string]string
}

// newEvalFixture returns a suite of two cases on a fresh repository, a bug
// that adds a file and a clean change to main.go, and two sidecars that are
// clones on this machine, as a pool's sync leaves them.
func newEvalFixture(t *testing.T) *evalFixture {
	t.Helper()
	root, base := newRepo(t)
	patches := map[string]string{
		"bug":   diffOf(t, root, func() { writeFile(t, root, "pkg/new.go", "package pkg\n\nvar X = 1 / 0\n") }),
		"clean": diffOf(t, root, func() { writeFile(t, root, "main.go", "package main\n\nfunc main() { println() }\n") }),
	}
	s, err := LoadSuite(writeSuite(t, []Case{
		{Name: "bug", Revision: base, Request: "add X", Patch: "patches/bug.patch",
			Expect: []Expected{{File: "pkg/new.go", Lines: []int{3, 3}, About: "divides by zero"}}},
		{Name: "clean", Revision: base, Request: "print a line", Patch: "patches/clean.patch"},
	}, patches))
	assert.NilError(t, err)

	var entries []*sidecar.PoolEntry
	for _, id := range []string{"sc-1", "sc-2"} {
		dir := filepath.Join(t.TempDir(), "repo")
		git(t, root, "clone", "-q", root, dir)
		entries = append(entries, &sidecar.PoolEntry{ID: id, RepoPath: dir})
	}
	home := t.TempDir()
	installFakeClaude(t, home, fakeReviewer)
	return &evalFixture{
		home: home, entries: entries, patches: patches,
		opts: Options{
			Root:    root,
			Suite:   s,
			Prompts: []review.Prompt{{Name: "bugs", Body: "find bugs"}, {Name: "tests", Body: "check tests"}},
			Entries: entries,
			Exec:    withoutPkill(localExec(home)),
			Sync:    mirror,
			Review:  review.Options{Credential: claudecode.Credential{EnvVar: "ANTHROPIC_API_KEY", Value: "k"}},
		},
	}
}

func TestRunReviewsEachCaseAsUncommittedWork(t *testing.T) {
	f := newEvalFixture(t)
	f.opts.Runs = 2
	var mu sync.Mutex
	var seenCases []string
	f.opts.OnCase = func(r CaseResult) {
		mu.Lock()
		defer mu.Unlock()
		seenCases = append(seenCases, r.Case)
	}

	results, err := Run(context.Background(), f.opts)
	assert.NilError(t, err)

	assert.DeepEqual(t, seenCases, []string{"bug", "clean", "bug", "clean"})
	assert.Equal(t, len(results), 4)
	for _, r := range results {
		assert.Assert(t, !r.Errored(), "%s: %+v", r.Case, r.Errors)
		assert.Equal(t, len(r.Findings), 2, r.Case)
	}
	bug, clean := results[0], results[1]
	assert.DeepEqual(t, where(bug.Problems), []string{"expected pkg/new.go:1 bugs tests"})
	assert.DeepEqual(t, where(clean.Problems), []string{"main.go:1 bugs tests"})
	assert.Equal(t, results[2].Run, 2)

	// Every review saw exactly its case's patch as the change, new files
	// included.
	seen, err := os.ReadDir(filepath.Join(f.home, "seen"))
	assert.NilError(t, err)
	assert.Equal(t, len(seen), 8)
	counts := map[string]int{}
	for _, e := range seen {
		data, err := os.ReadFile(filepath.Join(f.home, "seen", e.Name()))
		assert.NilError(t, err)
		for name, patch := range f.patches {
			if strings.TrimSpace(string(data)) == strings.TrimSpace(patch) {
				counts[name]++
			}
		}
	}
	assert.DeepEqual(t, counts, map[string]int{"bug": 4, "clean": 4})

	// The suite's repository is left as it was, with no worktrees behind.
	assert.Equal(t, git(t, f.opts.Root, "status", "--porcelain"), "")
	assert.Equal(t, len(strings.Split(git(t, f.opts.Root, "worktree", "list"), "\n")), 1)
}

func TestRunRecordsACaseThatCannotBeSetUpAndGoesOn(t *testing.T) {
	f := newEvalFixture(t)
	calls := 0
	f.opts.Sync = func(ctx context.Context, e *sidecar.PoolEntry, dir string) error {
		calls++
		if calls == 1 {
			return errors.New("connection reset")
		}
		return mirror(ctx, e, dir)
	}
	f.opts.Entries = f.entries[:1]

	results, err := Run(context.Background(), f.opts)
	assert.NilError(t, err)

	assert.Equal(t, len(results), 2)
	assert.Assert(t, results[0].Errored())
	assert.Equal(t, results[0].Errors[0].Prompt, setupPrompt)
	assert.ErrorContains(t, errors.New(results[0].Errors[0].Error), "sc-1: sync: connection reset")
	assert.Assert(t, !results[1].Errored(), "%+v", results[1].Errors)
	assert.Equal(t, results[1].Unexpected(), 1)
}

// TestRunStopsLeftoverReviewsBeforeEachCase guards against a review that timed
// out on one case still running while the next case replaces its tree.
func TestRunStopsLeftoverReviewsBeforeEachCase(t *testing.T) {
	f := newEvalFixture(t)
	f.opts.Entries = f.entries[:1]
	var mu sync.Mutex
	var events []string
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	exec := f.opts.Exec
	f.opts.Exec = func(ctx context.Context, e *sidecar.PoolEntry, script string, env map[string]string, onOutput circleci.OutputFn, onSubmitted func(string)) (int, error) {
		if strings.Contains(script, "pkill") {
			record("pkill")
		}
		return exec(ctx, e, script, env, onOutput, onSubmitted)
	}
	f.opts.Sync = func(ctx context.Context, e *sidecar.PoolEntry, dir string) error {
		record("sync")
		return mirror(ctx, e, dir)
	}

	_, err := Run(context.Background(), f.opts)
	assert.NilError(t, err)

	assert.DeepEqual(t, events, []string{"pkill", "sync", "sync", "pkill", "sync", "sync"})
}

func TestRunStopsWhenClaudeIsMissing(t *testing.T) {
	f := newEvalFixture(t)
	installFakeClaude(t, f.home, fmt.Sprintf("exit %d", claudecode.ExitMissing))

	results, err := Run(context.Background(), f.opts)
	assert.Assert(t, errors.Is(err, claudecode.ErrMissing), "%v", err)
	assert.Equal(t, len(results), 0)
}
