package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
	"github.com/CircleCI-Public/chunk-cli/internal/version"
)

func TestCheckFactoryRemoteFlagsRefusesEachUnsupportedFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts factoryRemoteOptions
		want string
	}{
		{name: "continue", opts: factoryRemoteOptions{continueRun: "20260101-000000-abcdef"}, want: "--continue is not supported with --remote yet."},
		{name: "log", opts: factoryRemoteOptions{logOn: true}, want: "--log is not supported with --remote yet."},
		{name: "log-file", opts: factoryRemoteOptions{logFile: "out.log"}, want: "--log-file is not supported with --remote yet."},
		{name: "verbose", opts: factoryRemoteOptions{verbose: true}, want: "--verbose is not supported with --remote yet."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkFactoryRemoteFlags(tc.opts)
			var ue *userError
			assert.Assert(t, errors.As(err, &ue), "got %v", err)
			assert.Equal(t, ue.UserMessage(), tc.want)
			assert.Equal(t, ue.UserExitCode(), ExitBadArgs)
		})
	}
}

func TestCheckFactoryRemoteFlagsAllowsPlainRemote(t *testing.T) {
	err := checkFactoryRemoteFlags(factoryRemoteOptions{})
	assert.NilError(t, err)
}

// factoryRemoteProject writes a project with .chunk/config.json and a reviews
// directory, the two things checkFactoryRemotePreflight requires.
func factoryRemoteProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	assert.NilError(t, os.MkdirAll(filepath.Join(root, ".chunk", "reviews"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(root, ".chunk", "config.json"), []byte(`{"commands":[{"name":"test","run":"true"}]}`), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(root, ".chunk", "reviews", "correctness.md"), []byte("review"), 0o644))
	return root
}

func TestCheckFactoryRemotePreflightNeedsProjectConfig(t *testing.T) {
	root := t.TempDir()
	assert.NilError(t, os.MkdirAll(filepath.Join(root, ".chunk", "reviews"), 0o755))

	_, err := checkFactoryRemotePreflight(root, "", "dist/chunk-linux")

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, ue.UserMessage(), msgValidateNotConfigured)
}

func TestCheckFactoryRemotePreflightNeedsReviewsDir(t *testing.T) {
	root := t.TempDir()
	assert.NilError(t, os.MkdirAll(filepath.Join(root, ".chunk"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(root, ".chunk", "config.json"), []byte(`{"commands":[{"name":"test","run":"true"}]}`), 0o644))

	_, err := checkFactoryRemotePreflight(root, "", "dist/chunk-linux")

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Assert(t, ue.UserMessage() != "", "expected a message")
	assert.Assert(t, ue.UserMessage() != msgValidateNotConfigured, "wrong failure: %s", ue.UserMessage())
	assert.Assert(t, strings.Contains(ue.suggestion, "Add one .md or .txt file per review"), "suggestion: %s", ue.suggestion)
	assert.Assert(t, !strings.Contains(ue.suggestion, "bootstrap"), "suggestion: %s", ue.suggestion)
}

// A dev build has no tagged release matching its version, so it is refused
// without --chunk-binary rather than failing later on the host.
func TestCheckFactoryRemotePreflightRefusesDevBuildWithoutChunkBinary(t *testing.T) {
	orig := version.Value
	version.Value = "dev"
	t.Cleanup(func() { version.Value = orig })
	root := factoryRemoteProject(t)

	_, err := checkFactoryRemotePreflight(root, "", "")

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Assert(t, ue.UserMessage() != "")
	assert.Assert(t, len(ue.Suggestion()) > 0)
}

func TestCheckFactoryRemotePreflightAllowsDevBuildWithChunkBinary(t *testing.T) {
	orig := version.Value
	version.Value = "dev"
	t.Cleanup(func() { version.Value = orig })
	root := factoryRemoteProject(t)

	cfg, err := checkFactoryRemotePreflight(root, "", "dist/chunk-linux")

	assert.NilError(t, err)
	assert.Assert(t, cfg != nil)
}

func TestCheckFactoryRemotePreflightAllowsATaggedBuild(t *testing.T) {
	orig := version.Value
	version.Value = "1.2.3"
	t.Cleanup(func() { version.Value = orig })
	root := factoryRemoteProject(t)

	cfg, err := checkFactoryRemotePreflight(root, "", "")

	assert.NilError(t, err)
	assert.Assert(t, cfg != nil)
}

// remoteRunKey's "r-" prefix must keep it from ever reading as a local run's
// ID, whatever factory.NewRunID returns.
func TestRemoteRunKeyShape(t *testing.T) {
	key := remoteRunKey()
	assert.Assert(t, regexp.MustCompile(`^r-\d{8}-\d{6}-[0-9a-f]{6}$`).MatchString(key), "key: %s", key)

	other := remoteRunKey()
	assert.Assert(t, key != other, "two keys should not collide: %s", key)
}

// factoryRemoteTestEnv wires a fake CircleCI API and a local SSH "sidecar",
// the same way acceptance tests exercise a real pool, but in process: that
// lets a test override package-level state such as version.Value, which a
// compiled acceptance binary cannot be made to see.
func factoryRemoteTestEnv(t *testing.T) (*fakes.FakeCircleCI, string) {
	t.Helper()
	for _, bin := range []string{"rsync", "ssh"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	t.Setenv(config.EnvXDGConfigHome, t.TempDir())
	homeDir := t.TempDir()
	t.Setenv(config.EnvHome, homeDir)
	pubKey := fakes.GenerateSSHKeypairAt(t, filepath.Join(homeDir, ".ssh", "chunk_ai"))
	sshSrv := fakes.NewSSHServer(t, pubKey)
	sshSrv.RunLocally()
	t.Setenv("CHUNK_SIDECAR_HOME", t.TempDir())

	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = sshSrv.Addr()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)
	t.Setenv(config.EnvCircleCIBaseURL, srv.URL)
	t.Setenv(config.EnvCircleToken, "fake-token")
	t.Setenv(config.EnvCircleCIOrgID, "org-aaa")
	t.Setenv(config.EnvAnthropicAPIKey, "sk-ant-fake-key")

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	assert.NilError(t, os.MkdirAll(filepath.Join(workDir, ".chunk", "reviews"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, ".chunk", "config.json"),
		[]byte(`{"commands":[{"name":"test","run":"true"}]}`), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, ".chunk", "reviews", "correctness.md"), []byte("review"), 0o644))
	t.Chdir(workDir)
	return cci, workDir
}

func newFactoryRemoteTestCmd() (*cobra.Command, *bytes.Buffer) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var errOut bytes.Buffer
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&errOut)
	return cmd, &errOut
}

// The host must install the exact release this build is, not the latest: a
// host on a different chunk than the laptop could behave differently.
func TestFactoryBuildRemoteInstallsThePinnedReleaseWithoutChunkBinary(t *testing.T) {
	cci, _ := factoryRemoteTestEnv(t)
	cci.ExecResponse = &fakes.ExecResponse{CommandID: "cmd-1", Stdout: "deadbeef\n"}

	orig := version.Value
	version.Value = "1.2.3"
	t.Cleanup(func() { version.Value = orig })

	cmd, errOut := newFactoryRemoteTestCmd()
	err := runFactoryBuildRemote(cmd, factoryRemoteOptions{prompt: "add a feature", jsonOut: true})
	assert.NilError(t, err, "stderr: %s", errOut.String())
	// --json and this test's non-terminal stderr both leave the run-start
	// message out.
	assert.Assert(t, !strings.Contains(errOut.String(), "Chunk will implement this change"), errOut.String())

	// The script reaches the sidecar as one string inside a JSON request
	// body, so its quotes and newlines are JSON-escaped on the wire; compare
	// against the same escaping rather than the raw script text.
	want := review.InstallVersion("v1.2.3")
	wantJSON, err := json.Marshal(want)
	assert.NilError(t, err)
	wantEscaped := string(wantJSON[1 : len(wantJSON)-1])

	var found bool
	for _, r := range cci.Recorder.AllRequests() {
		if !strings.HasSuffix(r.URL.Path, "/exec") {
			continue
		}
		body := string(r.Body)
		assert.Assert(t, !strings.Contains(body, "releases/latest/download"),
			"should install the pinned release, not the latest one: %s", body)
		if strings.Contains(body, wantEscaped) {
			found = true
		}
	}
	assert.Assert(t, found, "no exec installed %q; requests: %v", want, cci.Recorder.AllRequests())
}

// A failure after the host is provisioned must leave nothing behind: the pool
// destroyed and the local worktree removed.
func TestFactoryBuildRemoteCleansUpAfterAFailedBaselineCommit(t *testing.T) {
	cci, workDir := factoryRemoteTestEnv(t)
	// Installing succeeds (--chunk-binary uploads over the real SSH
	// connection), so the only exec call left is the baseline commit, which
	// this makes fail.
	cci.ExecResponse = &fakes.ExecResponse{CommandID: "cmd-1", ExitCode: 1, Stderr: "boom"}

	binPath := filepath.Join(t.TempDir(), "chunk-linux")
	assert.NilError(t, os.WriteFile(binPath, []byte("#!/bin/sh\necho fake chunk\n"), 0o755))

	cmd, _ := newFactoryRemoteTestCmd()
	err := runFactoryBuildRemote(cmd, factoryRemoteOptions{prompt: "add a feature", chunkBinary: binPath})
	assert.Assert(t, err != nil, "expected the baseline commit to fail")

	var deleted bool
	for _, r := range cci.Recorder.AllRequests() {
		if r.Method == "DELETE" && strings.HasPrefix(r.URL.Path, "/api/v3/sidecar/instances/") {
			deleted = true
		}
	}
	assert.Assert(t, deleted, "expected the host sidecar to be destroyed; requests: %v", cci.Recorder.AllRequests())

	root := config.CanonicalProjectRoot(workDir)
	dataDir, derr := config.RepoDataDir(root)
	assert.NilError(t, derr)
	entries, rerr := os.ReadDir(filepath.Join(dataDir, "factory"))
	assert.Assert(t, rerr != nil || len(entries) == 0, "expected no leftover worktree, found: %v", entries)
}

// Once the exec for the run itself has been submitted, a failure belongs to
// the run, not to provisioning: the sidecar and the worktree must be kept for
// inspection, not torn down, and the error must carry the host's stderr tail
// and point at 'chunk sidecar ssh'. This is the opposite case from
// TestFactoryBuildRemoteCleansUpAfterAFailedBaselineCommit, which fails
// before the run's own exec is ever submitted.
func TestFactoryBuildRemoteKeepsTheSidecarWhenTheRunItselfFails(t *testing.T) {
	cci, workDir := factoryRemoteTestEnv(t)
	// The baseline commit (the first exec, since --chunk-binary skips the
	// install exec) succeeds; the run's own exec (the second) fails with a
	// distinct exit code and stderr so the failure can only have come from it.
	cci.ExecResponses = []*fakes.ExecResponse{
		{CommandID: "cmd-baseline", Stdout: "deadbeef\n"},
		{CommandID: "cmd-run", ExitCode: 7, Stderr: "boom: something went wrong\n"},
	}

	binPath := filepath.Join(t.TempDir(), "chunk-linux")
	assert.NilError(t, os.WriteFile(binPath, []byte("#!/bin/sh\necho fake chunk\n"), 0o755))

	cmd, errOut := newFactoryRemoteTestCmd()
	err := runFactoryBuildRemote(cmd, factoryRemoteOptions{prompt: "add a feature", chunkBinary: binPath})
	assert.Assert(t, err != nil, "expected the run itself to fail")

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Assert(t, strings.Contains(ue.UserMessage(), "The run failed on sidecar"), "message: %s", ue.UserMessage())
	assert.Assert(t, strings.Contains(ue.Suggestion(), "chunk sidecar ssh --sidecar-id"), "suggestion: %s", ue.Suggestion())
	assert.Assert(t, strings.Contains(err.Error(), "exit 7"), "error: %v", err)
	assert.Assert(t, strings.Contains(err.Error(), "boom: something went wrong"), "error: %v", err)

	for _, r := range cci.Recorder.AllRequests() {
		assert.Assert(t, r.Method != "DELETE" || !strings.HasPrefix(r.URL.Path, "/api/v3/sidecar/instances/"),
			"a failure in the run itself must not delete the host sidecar: %v; stderr: %s", r, errOut.String())
	}

	root := config.CanonicalProjectRoot(workDir)
	dataDir, derr := config.RepoDataDir(root)
	assert.NilError(t, derr)
	entries, rerr := os.ReadDir(filepath.Join(dataDir, "factory"))
	assert.NilError(t, rerr)
	assert.Assert(t, len(entries) > 0, "expected the worktree to be kept for inspection")

	matches, gerr := filepath.Glob(filepath.Join(dataDir, "factory", "r-*.json"))
	assert.NilError(t, gerr)
	assert.Equal(t, len(matches), 1, "records: %v", matches)
	data, rerr2 := os.ReadFile(matches[0])
	assert.NilError(t, rerr2)
	var rec struct {
		Remote *struct {
			SidecarID string `json:"sidecar_id"`
			CommandID string `json:"command_id"`
		} `json:"remote"`
	}
	assert.NilError(t, json.Unmarshal(data, &rec))
	assert.Assert(t, rec.Remote != nil && rec.Remote.SidecarID != "", "record: %s", data)
	assert.Equal(t, rec.Remote.CommandID, "cmd-run")
}

// The host's --json result arrives on the exec's stdout, which must only be
// buffered, never written to this process's own stdout: a success prints
// exactly one JSON value (this command's own factoryRemoteResult), and a
// mistake that forwarded the host's stdout too would show up either as a
// second concatenated JSON value or as the host's text inside the printed
// one.
func TestFactoryBuildRemoteDoesNotLeakTheHostsStdout(t *testing.T) {
	cci, _ := factoryRemoteTestEnv(t)
	cci.ExecResponses = []*fakes.ExecResponse{
		{CommandID: "cmd-baseline", Stdout: "deadbeef\n"},
		{CommandID: "cmd-run", Stdout: `{"result":"host-finished"}` + "\n", Stderr: "host progress\n"},
	}

	binPath := filepath.Join(t.TempDir(), "chunk-linux")
	assert.NilError(t, os.WriteFile(binPath, []byte("#!/bin/sh\necho fake chunk\n"), 0o755))

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	err := runFactoryBuildRemote(cmd, factoryRemoteOptions{prompt: "add a feature", chunkBinary: binPath, jsonOut: true})
	assert.NilError(t, err, "stderr: %s", errOut.String())

	assert.Assert(t, strings.Contains(errOut.String(), "host progress"), "stderr: %s", errOut.String())
	assert.Assert(t, !strings.Contains(out.String(), "host-finished"),
		"the host's own --json result leaked onto this process's stdout: %s", out.String())

	var report factoryRemoteResult
	assert.NilError(t, json.Unmarshal(out.Bytes(), &report), "stdout was not exactly one JSON value: %s", out.String())
	assert.Assert(t, report.SidecarID != "", "stdout: %s", out.String())
}

// The script built for the host's exec names every build flag that means
// something there, keeps the prompt last, and starts the chunk on the host
// with 'exec' so it stays the tracked foreground process.
func TestRemoteBuildScriptIncludesPassThroughFlags(t *testing.T) {
	opts := factoryRemoteOptions{
		prompt:                  "add a feature",
		image:                   "snap-1",
		reviewers:               2,
		reviewsDir:              ".chunk/reviews",
		noValidate:              true,
		keepSidecars:            true,
		model:                   "claude-x",
		implementerInstructions: "be terse",
		implementTimeout:        5 * time.Minute,
		reviewTimeout:           3 * time.Minute,
		attempts:                4,
	}
	script := remoteBuildScript("/repo", "org-aaa", opts)

	for _, want := range []string{
		"--no-open-pr", "--org-id", "--image", "--reviewers", "--reviews", "--no-validate",
		"--keep-sidecars", "--model", "--implementer-instructions",
		"--implement-timeout", "--review-timeout", "--max-attempts",
	} {
		assert.Assert(t, strings.Contains(script, want), "missing %s in %q", want, script)
	}
	assert.Assert(t, strings.HasPrefix(script, "cd "+sidecar.ShellEscape("/repo")+" && exec \"$HOME/chunk\" "), "script: %q", script)
}

// An optional flag left at its zero value is left out, so the host falls
// back to its own default rather than being told to use an empty one.
func TestRemoteBuildScriptOmitsUnsetOptionalFlags(t *testing.T) {
	script := remoteBuildScript("/repo", "org-aaa", factoryRemoteOptions{prompt: "add a feature"})

	for _, unwanted := range []string{
		"--image", "--reviewers", "--reviews", "--no-validate",
		"--keep-sidecars", "--model", "--implementer-instructions",
	} {
		assert.Assert(t, !strings.Contains(script, unwanted), "unexpected %s in %q", unwanted, script)
	}
}

// Every value interpolated into the script - the host repo path, each flag's
// value and the prompt - is shell-escaped, so none of it is read as shell
// syntax of its own, and the prompt arrives after a literal --. This runs the
// script for real, through sh, rather than inspecting its text, since that is
// the only way to prove the escaping round-trips.
func TestRemoteBuildScriptEscapesEveryValueAndPassesThePromptAfterDashDash(t *testing.T) {
	for _, bin := range []string{"sh"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	home := t.TempDir()
	chunkPath := filepath.Join(home, "chunk")
	assert.NilError(t, os.WriteFile(chunkPath, []byte("#!/bin/sh\nfor a; do printf '%s\\n' \"$a\"; done\n"), 0o755))

	hostRepo := filepath.Join(t.TempDir(), "repo with spaces & 'quotes'")
	assert.NilError(t, os.MkdirAll(hostRepo, 0o755))

	opts := factoryRemoteOptions{
		prompt:                  "add a feature; rm -rf / # $(whoami) `id`",
		image:                   "it's a 'snapshot'",
		reviewers:               3,
		reviewsDir:              "reviews dir",
		noValidate:              true,
		keepSidecars:            true,
		model:                   "model $HOME",
		implementerInstructions: `don't "quote" me`,
		implementTimeout:        90 * time.Second,
		reviewTimeout:           2 * time.Minute,
		attempts:                5,
	}
	script := remoteBuildScript(hostRepo, "org's id", opts)

	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "HOME="+home)
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))

	got := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	want := []string{
		"factory", "build", "--json", "--no-open-pr",
		"--org-id", "org's id",
		"--max-attempts", "5",
		"--implement-timeout", "1m30s",
		"--review-timeout", "2m0s",
		"--image", "it's a 'snapshot'",
		"--reviewers", "3",
		"--reviews", "reviews dir",
		"--no-validate",
		"--keep-sidecars",
		"--model", "model $HOME",
		"--implementer-instructions", `don't "quote" me`,
		"--", opts.prompt,
	}
	assert.DeepEqual(t, got, want)
}

// tailWriter must keep the genuine tail of what is written to it - the bytes
// that explain a failure - rather than stopping at its cap and freezing on
// whatever arrived first, which a long run's stderr would otherwise do.
func TestTailWriterKeepsTheMostRecentBytes(t *testing.T) {
	w := newTailWriter(8)
	_, err := w.Write([]byte("0123456789"))
	assert.NilError(t, err)
	assert.Equal(t, w.String(), "23456789")

	_, err = w.Write([]byte("AB"))
	assert.NilError(t, err)
	assert.Equal(t, w.String(), "456789AB")
}

// The same eviction must hold across many small writes, which is how it is
// actually used: one call per chunk of streamed output, not one giant write.
func TestTailWriterKeepsTheTailAcrossManySmallWrites(t *testing.T) {
	w := newTailWriter(4)
	for _, b := range []byte("abcdefgh") {
		_, err := w.Write([]byte{b})
		assert.NilError(t, err)
	}
	assert.Equal(t, w.String(), "efgh")
}

func TestRemoteReleaseTagAddsOneV(t *testing.T) {
	orig := version.Value
	t.Cleanup(func() { version.Value = orig })
	for _, v := range []string{"1.2.3", "v1.2.3"} {
		version.Value = v
		assert.Equal(t, remoteReleaseTag(), "v1.2.3")
	}
}
