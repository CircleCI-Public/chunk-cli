package cmd

import (
	"errors"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// approval records the order approveAndBuild calls its steps in.
type approval struct {
	steps                []string
	shown                []string
	approveErr, buildErr error
}

func (a *approval) run() (string, error) {
	return approveAndBuild(
		func(text string) { a.shown = append(a.shown, text) },
		func() error { a.steps = append(a.steps, "approve"); return a.approveErr },
		func() error { a.steps = append(a.steps, "stop"); return errors.New("claude did not exit cleanly") },
		func() (string, error) {
			a.steps = append(a.steps, "build")
			if a.buildErr != nil {
				return "", a.buildErr
			}
			return "build-1", nil
		},
	)
}

func TestApproveAndBuildStopsThePlanningSessionBeforeTheBuild(t *testing.T) {
	a := &approval{}
	id, err := a.run()
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(id, "build-1"))
	assert.Check(t, cmp.DeepEqual(a.steps, []string{"approve", "stop", "build"}))
	assert.Check(t, cmp.DeepEqual(a.shown, []string{
		"Approving the plan in factory…",
		"Approved. Stopping the planning session…",
		"Starting the build on the chunk daemon…",
	}))
}

// A build that does not start leaves the plan approved with its claim
// released, so the planning session is stopped all the same: it would claim
// the plan again, and be handed build tasks it cannot do.
func TestApproveAndBuildStopsThePlanningSessionWhenTheBuildDoesNotStart(t *testing.T) {
	a := &approval{buildErr: errors.New("the chunk daemon is not reachable")}
	_, err := a.run()
	assert.Check(t, cmp.ErrorContains(err, "not reachable"))
	assert.Check(t, cmp.DeepEqual(a.steps, []string{"approve", "stop", "build"}))
}

// A plan that was not approved keeps its planning session, to plan on.
func TestApproveAndBuildKeepsThePlanningSessionWhenApprovalFails(t *testing.T) {
	a := &approval{approveErr: errors.New("the plan is not ready yet")}
	_, err := a.run()
	assert.Check(t, cmp.ErrorContains(err, "not ready"))
	assert.Check(t, cmp.DeepEqual(a.steps, []string{"approve"}))
	assert.Check(t, cmp.DeepEqual(a.shown, []string{"Approving the plan in factory…"}))
}

// A plan works from this machine's files, so a remote daemon is refused, in
// words that name the command still there.
func TestFactoryPlanRefusesARemoteDaemon(t *testing.T) {
	t.Setenv("CHUNK_DAEMON_REMOTE_ADDR", "127.0.0.1:9999")
	err := requireLocalDaemon()
	assert.Assert(t, cmp.ErrorContains(err, "chunk factory plan"))
	assert.Check(t, !strings.Contains(err.Error(), "factory build"))
}
