package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/chat"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// watchProject is the git top level chunk watch was opened under, canonical
// and registered for the daemon to find, and nothing outside a repository.
func TestWatchProject(t *testing.T) {
	repo := gitRepo(t, "")
	sub := filepath.Join(repo, "sub")
	err := os.MkdirAll(sub, 0o755)
	assert.NilError(t, err)
	// Reached through a symlink, as every darwin temp dir is.
	link := filepath.Join(t.TempDir(), "link")
	err = os.Symlink(repo, link)
	assert.NilError(t, err)
	canonical := config.CanonicalProjectRoot(repo)

	tests := []struct {
		name     string
		cwd      string
		wantRoot string
		wantOK   bool
	}{
		{
			name:     "repo root",
			cwd:      repo,
			wantRoot: canonical,
			wantOK:   true,
		},
		{
			name:     "repo subdirectory",
			cwd:      sub,
			wantRoot: canonical,
			wantOK:   true,
		},
		{
			name:     "repo through a symlink",
			cwd:      link,
			wantRoot: canonical,
			wantOK:   true,
		},
		{
			name: "not a repo",
			cwd:  t.TempDir(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateConfig(t)
			t.Setenv(config.EnvXDGDataHome, t.TempDir())

			root, ok := watchProject(t.Context(), tt.cwd)
			assert.Check(t, cmp.Equal(ok, tt.wantOK))
			assert.Check(t, cmp.Equal(root, tt.wantRoot))

			known, err := sidecar.AllProjectRoots()
			assert.NilError(t, err)
			if !tt.wantOK {
				assert.Check(t, cmp.Len(known, 0), "registered %v outside a repository", known)
				return
			}
			dataDir, err := config.ProjectDataDir(root)
			assert.NilError(t, err)
			crumb, err := os.ReadFile(sidecar.ProjectRootPath(dataDir))
			assert.NilError(t, err, "no project-root breadcrumb")
			assert.Check(t, cmp.Equal(string(crumb), canonical))
			assert.Check(t, cmp.DeepEqual(known, []string{canonical}))
		})
	}
}

func TestWatchFlagsAreDeprecatedButAccepted(t *testing.T) {
	cmd := newWatchCmd()

	focus := cmd.Flags().Lookup("focus")
	assert.Assert(t, focus != nil, "--focus should still parse for existing invocations")
	assert.Equal(t, focus.DefValue, "false")
	assert.Assert(t, focus.Deprecated != "", "--focus should be marked deprecated")

	all := cmd.Flags().Lookup("all")
	assert.Assert(t, all != nil, "--all should still parse for existing invocations")
	assert.Assert(t, all.Deprecated != "", "--all should be marked deprecated")
}

func TestWatchTakesNoArgs(t *testing.T) {
	cmd := newWatchCmd()
	err := cmd.Args(cmd, nil)
	assert.NilError(t, err)

	err = cmd.Args(cmd, []string{"x"})
	ue, ok := errors.AsType[*userError](err)
	assert.Assert(t, ok, "not a user error: %v", err)
	assert.Check(t, cmp.Equal(ue.UserExitCode(), ExitBadArgs))
}

func TestOpenFlows(t *testing.T) {
	flows := []circleci.Flow{
		{ID: "a", Phase: circleci.PhaseStarted, SessionID: "s1", Task: &circleci.FlowTask{ID: "t", Category: "build-unit"}},
		{ID: "b", Phase: circleci.PhaseReceived},
		{ID: "ended", State: "Done", Phase: circleci.PhaseEnded},
		{ID: "old", State: "Done"}, // a conductor that serves no phase
	}

	got := openFlows(flows)

	assert.Check(t, cmp.DeepEqual(got, []circleci.Flow{flows[0], flows[1], flows[3]}))
}

// previewPlan sends both requests at once, and fails when either does.
func TestPreviewPlan(t *testing.T) {
	for _, tc := range []struct {
		name         string
		textStatus   int
		commentsCode int
		wantErr      string
	}{
		{
			name:         "both in parallel",
			textStatus:   http.StatusOK,
			commentsCode: http.StatusOK,
		},
		{
			name:         "text fails",
			textStatus:   http.StatusNotFound,
			commentsCode: http.StatusOK,
			wantErr:      "plan text",
		},
		{
			name:         "comments fail",
			textStatus:   http.StatusOK,
			commentsCode: http.StatusNotFound,
			wantErr:      "list comments",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Each handler waits for the other to start, so sequential calls
			// time out instead of passing.
			var started sync.WaitGroup
			started.Add(2)
			wait := func() {
				started.Done()
				done := make(chan struct{})
				go func() { started.Wait(); close(done) }()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("the other request never started: calls are sequential")
				}
			}
			reply := func(w http.ResponseWriter, status int, body string) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if status == http.StatusOK {
					_, _ = w.Write([]byte(body))
					return
				}
				_, _ = w.Write([]byte(`{"message":"not found"}`))
			}
			mux := http.NewServeMux()
			mux.HandleFunc("GET /api/v3/factory/plans/{id}/text", func(w http.ResponseWriter, _ *http.Request) {
				wait()
				reply(w, tc.textStatus, `{"data":{"attributes":{"text":"# plan"}}}`)
			})
			mux.HandleFunc("GET /api/v3/factory/comments", func(w http.ResponseWriter, _ *http.Request) {
				wait()
				reply(w, tc.commentsCode, `{"data":[{"id":"c1","attributes":{"quoted_text":"","body":"look","created_at":"2026-01-01T00:00:00Z","resolved_at":null},"references":{"factory_plan":{"id":"p1"}}}],"page":{"next":null}}`)
			})
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)
			client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
			assert.NilError(t, err)

			text, comments, err := previewPlan(t.Context(), client, "p1")
			if tc.wantErr != "" {
				assert.Check(t, cmp.ErrorContains(err, tc.wantErr))
				return
			}
			assert.NilError(t, err)
			assert.Check(t, cmp.Equal(text, "# plan"))
			assert.Check(t, cmp.Len(comments, 1))
		})
	}
}

func TestPlanParents(t *testing.T) {
	plans := []circleci.Plan{{ID: "p1"}, {ID: "c1", Parent: "p1"}, {ID: "c2", Parent: "p1"}}

	got := planParents(plans)

	assert.Check(t, cmp.DeepEqual(got, map[string]string{"c1": "p1", "c2": "p1"}))
}

func TestPlanBuildLevel(t *testing.T) {
	for _, tc := range []struct {
		in   chunkd.FeedLevel
		want plan.BuildLevel
	}{
		{
			in:   chunkd.FeedInfo,
			want: plan.BuildInfo,
		},
		{
			in:   chunkd.FeedStep,
			want: plan.BuildStep,
		},
		{
			in:   chunkd.FeedDone,
			want: plan.BuildStep,
		},
		{
			in:   chunkd.FeedWarn,
			want: plan.BuildWarn,
		},
		{
			in:   chunkd.FeedError,
			want: plan.BuildError,
		},
		{
			in:   chunkd.FeedLevel("shout"),
			want: plan.BuildInfo,
		},
	} {
		assert.Check(t, cmp.Equal(planBuildLevel(tc.in), tc.want), string(tc.in))
	}
}

func TestPlanBuildActivity(t *testing.T) {
	lines := []chunkd.FeedLine{
		{Level: chunkd.FeedStep, Text: "Building plan p in /src"},
		{Level: chunkd.FeedInfo, Text: "Read main.go"},
	}
	want := []plan.BuildLine{
		{Level: plan.BuildStep, Text: "Building plan p in /src"},
		{Level: plan.BuildInfo, Text: "Read main.go"},
	}
	for _, tc := range []struct {
		name  string
		in    chunkd.PlanBuildActivity
		found bool
		want  plan.BuildActivity
	}{
		{
			name:  "running",
			in:    chunkd.PlanBuildActivity{ID: "s1", State: chunkd.SessionRunning, First: 3, Lines: lines, Total: 5},
			found: true,
			want:  plan.BuildActivity{Found: true, ID: "s1", Running: true, First: 3, Lines: want, Total: 5},
		},
		{
			name:  "failed",
			in:    chunkd.PlanBuildActivity{ID: "s1", State: chunkd.SessionFailed, Error: "boom", Total: 2},
			found: true,
			want:  plan.BuildActivity{Found: true, ID: "s1", Failed: "boom", Total: 2},
		},
		{
			name:  "done",
			in:    chunkd.PlanBuildActivity{ID: "s1", State: chunkd.SessionDone, Lines: lines, Total: 2},
			found: true,
			want:  plan.BuildActivity{Found: true, ID: "s1", Lines: want, Total: 2},
		},
		{
			name:  "cancelled",
			in:    chunkd.PlanBuildActivity{ID: "s1", State: chunkd.SessionCancelled, Error: "stopped", Total: 2},
			found: true,
			want:  plan.BuildActivity{Found: true, ID: "s1", Total: 2},
		},
		{
			name: "not found",
			in:   chunkd.PlanBuildActivity{ID: "s1", State: chunkd.SessionRunning, Lines: lines, Total: 2},
			want: plan.BuildActivity{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Check(t, cmp.DeepEqual(planBuildFrom(tc.in, tc.found), tc.want))
		})
	}
}

// A chat session's log reaches the chat as it is, each event by its kind, and
// an event of a kind the chat has no place for is left out.
func TestChatUpdate(t *testing.T) {
	u := chatUpdate(chunkd.SessionLog{
		LogID: "log-1", Next: 5, Busy: true, Activity: "Reading plan",
		Events: []chunkd.SessionLogEvent{
			{Seq: 0, Kind: chunkd.SessionLogMessage, Text: "hi"},
			{Seq: 1, Kind: chunkd.SessionLogActivity, Text: "Read plan"},
			{Seq: 2, Kind: "later", Text: "unknown"},
			{Seq: 3, Kind: chunkd.SessionLogReply, Text: "hello"},
			{Seq: 4, Kind: chunkd.SessionLogProblem, Text: "claude exited"},
		},
	})
	assert.Check(t, cmp.DeepEqual(u, chat.Update{
		LogID: "log-1", Next: 5, Busy: true, Activity: "Reading plan",
		Entries: []chat.Entry{{Message: "hi"}, {Activity: "Read plan"}, {Reply: "hello"}, {Problem: "claude exited"}},
	}))
}

// daemonResume checks the project, then calls the daemon's route for the
// kind of resume, and gives the daemon's refusal as it gives it.
func TestDaemonResume(t *testing.T) {
	refused := &chunkd.SessionRefused{Status: 409, Message: "plan p is claimed by session s; release it with factory daemon release p to resume it"}
	for _, tc := range []struct {
		name string
		root string
		kind planstate.ResumeKind
		// fails is what the daemon answers.
		fails       error
		wantErr     string
		wantPlanner []string
		wantBuild   []chunkd.PlanBuildRequest
	}{
		{
			name:    "outside a repository",
			root:    "",
			kind:    planstate.ResumeBuild,
			wantErr: "open chunk watch in this plan's local repository to plan or build",
		},
		{
			name:    "nothing to resume",
			root:    "/src/app",
			kind:    planstate.ResumeNone,
			wantErr: "this plan has nothing for chunk to resume",
		},
		{
			name:        "planner",
			root:        "/src/app",
			kind:        planstate.ResumePlanner,
			wantPlanner: []string{"p /src/app"},
		},
		{
			name:      "build",
			root:      "/src/app",
			kind:      planstate.ResumeBuild,
			wantBuild: []chunkd.PlanBuildRequest{{ProjectRoot: "/src/app", PlanID: "p"}},
		},
		{
			name:      "refused",
			root:      "/src/app",
			kind:      planstate.ResumeBuild,
			fails:     refused,
			wantErr:   refused.Message,
			wantBuild: []chunkd.PlanBuildRequest{{ProjectRoot: "/src/app", PlanID: "p"}},
		},
		{
			name:        "daemon unavailable",
			root:        "/src/app",
			kind:        planstate.ResumePlanner,
			fails:       fmt.Errorf("resume: %w", chunkd.ErrDaemonUnavailable),
			wantErr:     "the chunk daemon is not reachable",
			wantPlanner: []string{"p /src/app"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			savedCheck, savedPlanner, savedBuild := checkWatchProject, resumePlanner, startPlanBuild
			t.Cleanup(func() { checkWatchProject, resumePlanner, startPlanBuild = savedCheck, savedPlanner, savedBuild })
			if tc.root != "" {
				// The real check needs a git origin and config; "" is
				// refused before it is reached.
				checkWatchProject = func(context.Context, string) error { return nil }
			}
			var planners []string
			var builds []chunkd.PlanBuildRequest
			resumePlanner = func(planID string, req chunkd.PlannerResume) (chunkd.SessionLog, error) {
				planners = append(planners, planID+" "+req.ProjectRoot)
				return chunkd.SessionLog{}, tc.fails
			}
			startPlanBuild = func(req chunkd.PlanBuildRequest) (string, error) {
				builds = append(builds, req)
				return "s-9", tc.fails
			}

			err := daemonResume{}.Resume(t.Context(), tc.root, "p", tc.kind)
			if tc.wantErr == "" {
				assert.Check(t, err)
			} else {
				assert.Check(t, cmp.Error(err, tc.wantErr))
			}
			assert.Check(t, cmp.DeepEqual(planners, tc.wantPlanner))
			assert.Check(t, cmp.DeepEqual(builds, tc.wantBuild))
		})
	}
}

// A blocker takes its state and outcome from its flow, which the blockers
// endpoint does not serve, and keeps its own when it has no flow listed.
func TestBlockerStates(t *testing.T) {
	for _, tc := range []struct {
		name     string
		blockers []circleci.Blocker
		flows    []circleci.Flow
		want     []planstate.Blocker
	}{
		{
			name:     "listed",
			blockers: []circleci.Blocker{{ID: "p-41", Title: "old title", State: "Integrating - Human"}},
			flows: []circleci.Flow{
				{ID: "p-40", Title: "Other", State: "Building - Agent"},
				{ID: "p-41", Title: "Add retry config", State: "Integrating - Human", Phase: circleci.PhaseEnded, Outcome: "merged"},
			},
			want: []planstate.Blocker{{Title: "Add retry config", State: "Integrating - Human", Outcome: "merged"}},
		},
		{
			name:     "listed without a title",
			blockers: []circleci.Blocker{{ID: "p-41", Title: "Add retry config", State: "Building - Agent"}},
			flows:    []circleci.Flow{{ID: "p-41", State: "Done"}},
			want:     []planstate.Blocker{{Title: "Add retry config", State: "Done"}},
		},
		{
			name:     "not listed",
			blockers: []circleci.Blocker{{ID: "p-41", Title: "Add retry config", State: "Building - Agent"}},
			want:     []planstate.Blocker{{Title: "Add retry config", State: "Building - Agent"}},
		},
		{
			name: "none",
			want: []planstate.Blocker{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Check(t, cmp.DeepEqual(blockerStates(tc.blockers, tc.flows), tc.want))
		})
	}
}
