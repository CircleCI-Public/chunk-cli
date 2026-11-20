package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// TestPlanRepo checks that a plan records its project's origin as owner/name
// when origin is on GitHub, and no repository otherwise.
func TestPlanRepo(t *testing.T) {
	tests := []struct {
		name   string
		origin string
		want   string
	}{
		{
			name:   "github ssh",
			origin: "git@github.com:o/r.git",
			want:   "o/r",
		},
		{
			name:   "github https",
			origin: "https://github.com/o/r.git",
			want:   "o/r",
		},
		{
			name:   "gitlab",
			origin: "https://gitlab.com/o/r.git",
			want:   "",
		},
		{
			name: "no origin",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				cmd.Env = gitrepo.GitEnv(dir)
				out, err := cmd.CombinedOutput()
				assert.NilError(t, err, string(out))
			}
			git("init")
			if tt.origin != "" {
				git("remote", "add", "origin", tt.origin)
			}
			got := planRepo(context.Background(), dir)
			assert.Check(t, cmp.Equal(got, tt.want))
		})
	}
}

// reportPlanLeft says where the plan was left: nothing for no plan, not
// approved, approved with a worker building it, and approved with none.
func TestReportPlanLeft(t *testing.T) {
	t.Setenv("CI", "")
	saved := onTerminal
	t.Cleanup(func() { onTerminal = saved })
	onTerminal = func(iostream.Streams) bool { return true }

	for _, tc := range []struct {
		name     string
		planID   string
		approved bool
		worker   string
		want     string
	}{
		{
			name: "no plan",
		},
		{
			name:   "not approved",
			planID: "p-42",
			want:   "The plan was not approved. It is plan p-42 in factory.\n",
		},
		{
			name:     "approved, a worker builds it",
			planID:   "p-42",
			approved: true,
			worker:   "scotts-mbp",
			want:     "Plan p-42 is approved; worker scotts-mbp is building it.\n" + watchStartHint + "\n",
		},
		{
			name:     "approved, no worker",
			planID:   "p-42",
			approved: true,
			want:     "Plan p-42 is approved; no worker is building it. Press R in chunk ui to claim it.\n",
		},
		{
			name:   "a worker with no approval is not mentioned",
			planID: "p-42",
			worker: "scotts-mbp",
			want:   "The plan was not approved. It is plan p-42 in factory.\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			streams, _, errOut := testStreams()
			reportPlanLeft(streams, tc.planID, tc.approved, tc.worker)
			assert.Check(t, cmp.Equal(errOut.String(), tc.want))
		})
	}
}

// chunk factory plan takes an optional request argument and has no --model
// flag.
func TestFactoryPlanFlagsHaveNoModel(t *testing.T) {
	cmd := newFactoryPlanCmd()
	assert.Check(t, cmd.Flags().Lookup("model") == nil)
	assert.Check(t, cmp.Equal(cmd.Use, "plan [request]"))
}

// chunk factory plan needs no Claude Code, factory daemon socket or local
// chunk daemon of its own: with no claude on PATH, no daemon, and even with a
// remote chunk daemon configured, its preflight passes in a project that can
// be built from.
func TestFactoryPlanNeedsNoClaude(t *testing.T) {
	repo := planProjectRepo(t, true, configWithCmds, "prompt")
	t.Chdir(repo)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_DATA_HOME", home)
	t.Setenv("CIRCLE_TOKEN", "test-token")
	// Nothing on PATH is claude or factory, only git, and there is no factory
	// socket.
	git, err := exec.LookPath("git")
	assert.NilError(t, err)
	bin := t.TempDir()
	err = os.Symlink(git, filepath.Join(bin, "git"))
	assert.NilError(t, err)
	t.Setenv("PATH", bin)
	_, err = exec.LookPath("claude")
	assert.Assert(t, err != nil, "claude is on PATH")
	t.Setenv("FACTORY_DAEMON_SOCKET", home+"/no-such.sock")

	for _, tc := range []struct {
		name   string
		remote string
	}{
		{name: "a local chunk daemon"},
		{name: "a remote chunk daemon", remote: "127.0.0.1:9999"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CHUNK_DAEMON_REMOTE_ADDR", tc.remote)
			root, api, err := factoryPlanPreflight(t.Context())
			assert.NilError(t, err)
			assert.Check(t, api != nil)
			assert.Check(t, cmp.Equal(root, config.CanonicalProjectRoot(repo)))
		})
	}
}
