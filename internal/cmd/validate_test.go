package cmd

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/session"
)

// gitSetup initialises a minimal git repo at dir on the given branch name.
func gitSetup(t *testing.T, dir, branch string) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", branch)
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	_ = os.WriteFile(filepath.Join(dir, "README"), []byte("init"), 0o644)
	run("add", ".")
	run("commit", "-m", "init")
}

func hashFor(sessionID, branch string) string {
	sum := sha256.Sum256([]byte(sessionID + ":" + branch))
	return fmt.Sprintf("%x", sum[:4])
}

// Tests with a session ID: branch must be hashed, never appear raw.

func TestSidecarAutoNameWithSessionAndBranch(t *testing.T) {
	dir := t.TempDir()
	gitSetup(t, dir, "main")
	ctx := session.WithID(context.Background(), "sess-1")
	got := sidecarAutoName(ctx, dir)
	want := filepath.Base(dir) + "-sess-1-" + hashFor("sess-1", "main")
	assert.Equal(t, got, want)
}

func TestSidecarAutoNameWithSessionBranchWithSlashes(t *testing.T) {
	dir := t.TempDir()
	gitSetup(t, dir, "main")
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("checkout", "-b", "feature/my-branch")
	ctx := session.WithID(context.Background(), "sess-2")
	got := sidecarAutoName(ctx, dir)
	want := filepath.Base(dir) + "-sess-2-" + hashFor("sess-2", "feature/my-branch")
	assert.Equal(t, got, want)
	assert.Assert(t, !strings.Contains(got, "feature"), "raw branch must not appear in name, got %q", got)
	assert.Assert(t, !strings.Contains(got, "my-branch"), "raw branch must not appear in name, got %q", got)
}

func TestSidecarAutoNameWithSessionNoBranch(t *testing.T) {
	dir := t.TempDir()
	// No git repo → no branch.
	ctx := session.WithID(context.Background(), "sess-3")
	got := sidecarAutoName(ctx, dir)
	assert.Equal(t, got, filepath.Base(dir)+"-sess-3")
}

func TestSidecarAutoNameDifferentBranchesDifferentNames(t *testing.T) {
	dir := t.TempDir()
	gitSetup(t, dir, "main")
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	ctx := session.WithID(context.Background(), "sess-x")
	n1 := sidecarAutoName(ctx, dir)
	run("checkout", "-b", "other-branch")
	n2 := sidecarAutoName(ctx, dir)
	assert.Assert(t, n1 != n2, "different branches must produce different names: %q vs %q", n1, n2)
}

// Tests without a session ID: legacy sanitised-branch fallback.

func TestSidecarAutoNameNoSessionBranchPresent(t *testing.T) {
	dir := t.TempDir()
	gitSetup(t, dir, "main")
	got := sidecarAutoName(context.Background(), dir)
	assert.Equal(t, got, filepath.Base(dir)+"-main-validate")
}

func TestSidecarAutoNameNoSessionBranchAbsent(t *testing.T) {
	dir := t.TempDir()
	// No git repo → falls back to old format.
	got := sidecarAutoName(context.Background(), dir)
	assert.Equal(t, got, filepath.Base(dir)+"-validate")
}

func TestSidecarAutoNameNoSessionBranchWithSlashes(t *testing.T) {
	dir := t.TempDir()
	gitSetup(t, dir, "main")
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("checkout", "-b", "feature/my-branch")
	got := sidecarAutoName(context.Background(), dir)
	assert.Equal(t, got, filepath.Base(dir)+"-feature-my-branch-validate")
}

func TestSidecarAutoNameNoSessionLongBranch(t *testing.T) {
	dir := t.TempDir()
	long := "abcdefghijklmnopqrstuvwxyz012345" // 32 chars
	gitSetup(t, dir, "main")
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("checkout", "-b", long)
	got := sidecarAutoName(context.Background(), dir)
	// branch truncated to 30 chars
	assert.Equal(t, got, filepath.Base(dir)+"-"+long[:30]+"-validate")
}
