package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
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

// TestApprovePlan checks that approving is one ApproveAndBuild call for the
// plan in the project, and that the daemon's answers read as the screen shows
// them.
func TestApprovePlan(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		err     error
		want    string
		wantErr string
	}{
		{
			name: "built",
			id:   "b-1",
			want: "b-1",
		},
		{
			name:    "refused",
			err:     &chunkd.SessionRefused{Status: 409, Message: "the plan is not ready yet; answer its open questions first"},
			wantErr: "the plan is not ready yet; answer its open questions first",
		},
		{
			name:    "unreachable",
			err:     fmt.Errorf("%w: dial unix: no such file", chunkd.ErrDaemonUnavailable),
			wantErr: "the chunk daemon is not reachable; the plan was not approved",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type call struct {
				PlanID string
				Req    chunkd.PlanApproveRequest
			}
			var calls []call
			saved := approveAndBuild
			t.Cleanup(func() { approveAndBuild = saved })
			approveAndBuild = func(planID string, req chunkd.PlanApproveRequest) (string, error) {
				calls = append(calls, call{planID, req})
				return tt.id, tt.err
			}

			id, err := approvePlan("/src", "plan-1", "opus")
			if tt.wantErr != "" {
				assert.Check(t, cmp.Error(err, tt.wantErr))
			} else {
				assert.NilError(t, err)
			}
			assert.Check(t, cmp.Equal(id, tt.want))
			assert.Check(t, cmp.DeepEqual(calls, []call{{"plan-1", chunkd.PlanApproveRequest{ProjectRoot: "/src", Model: "opus"}}}))
		})
	}
}

// A plan works from this machine's files, so a remote daemon is refused, and
// the refusal names chunk factory plan.
func TestFactoryPlanRefusesARemoteDaemon(t *testing.T) {
	t.Setenv("CHUNK_DAEMON_REMOTE_ADDR", "127.0.0.1:9999")
	err := requireLocalDaemon()
	assert.Assert(t, cmp.ErrorContains(err, "chunk factory plan"))
	assert.Check(t, !strings.Contains(err.Error(), "factory build"))
}
