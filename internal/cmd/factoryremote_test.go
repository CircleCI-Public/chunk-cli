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

	"github.com/spf13/cobra"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
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

	// The script reaches the sidecar as one string inside a JSON request
	// body, so its quotes and newlines are JSON-escaped on the wire; compare
	// against the same escaping rather than the raw script text.
	want := review.InstallVersion("v" + version.Value)
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
