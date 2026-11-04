package onboarding_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/onboarding"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/onboarding/onboardingtest"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/plan"
)

// driver runs the model's commands, timers included, in a synctest bubble,
// and hands their messages back to it.
type driver struct {
	t      *testing.T
	m      onboarding.Model
	ctx    context.Context
	cancel context.CancelFunc
	events chan tea.Msg
	wg     sync.WaitGroup
	seen   []tea.Msg
}

func newDriver(ctx context.Context, t *testing.T, deps onboarding.Deps) *driver {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	d := &driver{t: t, ctx: ctx, cancel: cancel, events: make(chan tea.Msg, 100)}
	d.m = onboarding.New(ctx, deps)
	t.Cleanup(func() { cancel(); d.wg.Wait() })
	d.send(tea.WindowSizeMsg{Width: 100, Height: 40})
	d.run(d.m.Init())
	return d
}

func (d *driver) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, next := range batch {
				d.run(next)
			}
			return
		}
		if msg != nil {
			select {
			case d.events <- msg:
			case <-d.ctx.Done():
			}
		}
	}()
}

func (d *driver) send(msg tea.Msg) {
	d.seen = append(d.seen, msg)
	var cmd tea.Cmd
	d.m, cmd = d.m.Update(msg)
	d.run(cmd)
}

// drain hands the model every message ready now.
func (d *driver) drain() {
	for {
		synctest.Wait()
		select {
		case msg := <-d.events:
			d.send(msg)
		default:
			return
		}
	}
}

// until runs the model until cond holds, letting fake time pass.
func (d *driver) until(what string, cond func() bool) {
	d.t.Helper()
	for range 2000 {
		d.drain()
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	d.t.Fatalf("never %s; content:\n%s", what, d.content())
}

// idle runs the model for dur of fake time.
func (d *driver) idle(dur time.Duration) {
	end := time.Now().Add(dur)
	for time.Now().Before(end) {
		d.drain()
		time.Sleep(100 * time.Millisecond)
	}
	d.drain()
}

func (d *driver) shows(s string) func() bool {
	return func() bool { return strings.Contains(d.content(), s) }
}

func (d *driver) content() string { return ansi.Strip(d.m.Content()) }

// focused is the focused row's line.
func (d *driver) focused() string {
	for _, l := range strings.Split(d.content(), "\n") {
		if strings.HasPrefix(l, "▶ ") {
			return l
		}
	}
	return ""
}

func (d *driver) press(code rune) {
	msg := tea.KeyPressMsg{Code: code}
	if code >= ' ' && code <= '~' {
		msg.Text = string(code)
	}
	d.send(msg)
	d.drain()
}

func (d *driver) typeText(s string) {
	for _, r := range s {
		d.press(r)
	}
}

// focusOn moves focus down to the row reading text.
func (d *driver) focusOn(text string) {
	d.t.Helper()
	for range 3 {
		d.press(tea.KeyUp)
	}
	for range 10 {
		if strings.Contains(d.focused(), text) {
			return
		}
		d.press(tea.KeyDown)
	}
	d.t.Fatalf("no row %q to focus; content:\n%s", text, d.content())
}

func (d *driver) saw(want tea.Msg) bool { return slices.Contains(d.seen, want) }

// world is onboarding's fakes.
type world struct {
	setup   *onboardingtest.Setup
	account *onboardingtest.Account
	state   *onboardingtest.State
	agent   *onboardingtest.Agent
	store   *onboardingtest.Store
	opener  *onboardingtest.Opener
	checks  onboarding.PRChecks
	browser onboarding.Browser
}

func newWorld(st onboarding.State, fetches ...onboardingtest.Fetched) *world {
	w := &world{
		setup:   onboardingtest.NewSetup(fetches...),
		account: onboardingtest.NewAccount("org-1", nil, nil),
		state:   onboardingtest.NewState(st),
		agent:   onboardingtest.NewAgent(nil),
		store:   onboardingtest.NewStore(st.PlanID, "plan-1"),
	}
	w.opener = onboardingtest.NewOpener(w.agent, w.store)
	w.opener.Root = "/src/app"
	return w
}

func (w *world) deps() onboarding.Deps {
	return onboarding.Deps{
		Root: "/src/app", Setup: w.setup, Account: w.account, Open: w.opener.Open,
		Checks: w.checks, Browser: w.browser, State: w.state,
	}
}

func running(checks int) chunkd.SetupActivity {
	return chunkd.SetupActivity{
		State: chunkd.SetupRunning, Step: "config", Checks: checks,
		Lines: []chunkd.FeedLine{{Text: "detecting validation commands"}}, Total: 1,
	}
}

func ok(checks int) chunkd.SetupActivity {
	return chunkd.SetupActivity{State: chunkd.SetupOK, Step: "snapshot", Image: "img-1", Checks: checks, Total: 3}
}

const wantRequest = "Add dark mode\n\nAlso commit .chunk/config.json, which chunk wrote to set this repo up for chunk factory."

func TestQueuedChangeWaitsForChecks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(onboarding.State{}, onboardingtest.Found(running(-1)))
		d := newDriver(t.Context(), t, w.deps())
		d.until("shows setup running", d.shows("● Setup · agent · detecting validation commands"))

		d.focusOn("Add dark mode")
		d.press(tea.KeyEnter)
		d.until("queues the change", d.shows("Add dark mode · Start · waits for setup to detect commands"))
		assert.Check(t, cmp.Len(w.opener.IDs(), 0))

		w.setup.Then(onboardingtest.Found(chunkd.SetupActivity{
			State: chunkd.SetupFailed, Step: "config", Checks: 0, Total: 2,
			Summary: "Add a validation command with chunk config, or a review prompt in .chunk/reviews/, then press r",
		}))
		d.until("waits for a check", d.shows("Add dark mode · Start · waits for a validation command"))
		assert.Check(t, d.shows("Add a validation command with chunk config")())
		d.idle(10 * time.Second)
		assert.Check(t, cmp.Len(w.opener.IDs(), 0))
		assert.Check(t, cmp.Len(w.setup.Starts(), 0))

		w.setup.OnStart(running(-1), nil, onboardingtest.Found(running(1)))
		d.press('r')
		d.until("creates the plan", func() bool { return len(w.store.Created()) == 1 })
		d.until("requests the plan", func() bool { return len(w.agent.Requests()) == 1 })
		assert.Check(t, cmp.DeepEqual(w.setup.Starts(), []string{"org-1"}))
		assert.Check(t, cmp.DeepEqual(w.opener.IDs(), []string{""}))
		assert.Check(t, cmp.DeepEqual(w.store.Created(), []string{wantRequest}))
		assert.Check(t, cmp.DeepEqual(w.agent.Requests(), []string{wantRequest}))
		saved := w.state.Saved()
		assert.Check(t, cmp.Equal(saved.PlanID, "plan-1"))
		assert.Check(t, cmp.Equal(saved.Suggestion, "Add dark mode"))
		assert.Check(t, slices.Contains(saved.Suggestions, onboarding.Suggestion{Title: "Add dark mode", Used: true}))
	})
}

func TestReopenResumesTheSavedPlan(t *testing.T) {
	// noCheckout is the refusal watch's opener gives a plan it found no
	// checkout for, opening it with Root "".
	noCheckout := errors.New("plan builds in o/r, and chunk knows no checkout of it; run chunk watch once in a checkout of o/r")
	tests := []struct {
		name        string
		state       string
		root        string
		resumeErr   error
		wantResumes int
		want        string
		wantPlanErr string
	}{
		{
			name:        "refine",
			state:       "refine-proposal",
			root:        "/src/app",
			wantResumes: 1,
			want:        "● Plan · agent",
		},
		{
			name:        "build",
			state:       "build",
			root:        "/src/app",
			wantResumes: 0,
			want:        "● Build · agent",
		},
		{
			name:        "resume fails",
			state:       "refine-units",
			root:        "/src/app",
			resumeErr:   errors.New("boom"),
			wantResumes: 1,
			want:        "boom",
			wantPlanErr: "boom",
		},
		{
			name:        "no checkout",
			state:       "refine-units",
			root:        "",
			resumeErr:   noCheckout,
			wantResumes: 1,
			want:        "chunk knows no checkout of it",
			wantPlanErr: "chunk knows no checkout of it",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := newWorld(onboarding.State{PlanID: "plan-9"}, onboardingtest.Found(ok(1)))
				w.agent = onboardingtest.NewAgent(tt.resumeErr)
				w.opener.Agent = w.agent
				w.opener.Root = tt.root
				w.store.SetFlow(plan.Flow{State: tt.state, WaitingOn: "session"}, nil)
				d := newDriver(t.Context(), t, w.deps())
				d.until("shows the plan", d.shows(tt.want))
				d.idle(5 * time.Second)
				assert.Check(t, cmp.Equal(w.agent.Resumes(), tt.wantResumes))
				assert.Check(t, cmp.DeepEqual(w.opener.IDs(), []string{"plan-9"}))
				if tt.wantPlanErr == "" {
					return
				}
				content := d.content()
				lines := strings.Split(content, "\n")
				i := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "● Plan · agent") })
				assert.Assert(t, i >= 0 && i+1 < len(lines), "no detail line under Plan in:\n%s", content)
				assert.Check(t, cmp.Contains(lines[i+1], tt.wantPlanErr))
			})
		})
	}
}

func TestCIChecksFallsBack(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(onboarding.State{PlanID: "plan-9"}, onboardingtest.Found(ok(1)))
		w.checks = func(context.Context, string) (*onboarding.Checks, error) {
			return nil, errors.New("github is down")
		}
		w.store.SetFlow(plan.Flow{State: "integrate", WaitingOn: "session", PR: "https://github.com/o/r/pull/1"}, nil)
		d := newDriver(t.Context(), t, w.deps())
		d.until("shows the checks error", d.shows("github is down"))
		assert.Check(t, d.shows("● CI checks · agent · integrate")())
	})
}

func TestSignInCard(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "signed out",
			err:  onboarding.ErrSignedOut,
			want: "Run `chunk auth login` in another terminal",
		},
		{
			name: "no orgs",
			want: "create one at app.circleci.com",
		},
		{
			name: "error",
			err:  errors.New("boom"),
			want: "◐ Sign in to CircleCI · you · boom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := newWorld(onboarding.State{})
				w.account.Set("", nil, tt.err)
				d := newDriver(t.Context(), t, w.deps())
				d.until("shows the card", d.shows(tt.want))
				assert.Check(t, d.shows("[r] Retry")())
				d.idle(3 * time.Second)
				assert.Check(t, cmp.Len(w.setup.Starts(), 0))

				w.account.Set("org-2", nil, nil)
				d.press('r')
				d.until("starts setup", func() bool { return len(w.setup.Starts()) == 1 })
				assert.Check(t, cmp.DeepEqual(w.setup.Starts(), []string{"org-2"}))
			})
		})
	}
}

func TestChooseOrg(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(onboarding.State{})
		w.account.Set("", []onboarding.Org{{ID: "o1", Name: "Alpha"}, {ID: "o2", Name: "Beta"}}, nil)
		d := newDriver(t.Context(), t, w.deps())
		d.until("lists the orgs", d.shows("◐ Choose an organization · you"))
		assert.Check(t, d.shows("Alpha")())
		assert.Check(t, d.shows("Beta")())
		assert.Check(t, strings.Contains(d.focused(), "Alpha"), d.content())

		d.press(tea.KeyDown)
		d.press(tea.KeyEnter)
		d.until("starts setup", func() bool { return len(w.setup.Starts()) == 1 })
		assert.Check(t, cmp.DeepEqual(w.setup.Starts(), []string{"o2"}))
	})
}

func TestBlankOwnChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := newWorld(onboarding.State{}, onboardingtest.Found(ok(1)))
		d := newDriver(t.Context(), t, w.deps())
		d.until("shows setup done", d.shows("✓ Setup · agent · recorded img-1"))
		d.focusOn("describe your own")
		d.typeText("   ")
		d.press(tea.KeyEnter)
		d.idle(3 * time.Second)
		assert.Check(t, cmp.Len(w.opener.IDs(), 0))

		// Focus is still on the input: what is typed next goes into it.
		d.typeText("x")
		assert.Check(t, cmp.Equal(d.m.Unsent(), "   x"))
	})
}

func TestFlowEnds(t *testing.T) {
	used := []onboarding.Suggestion{{Title: "Add keyboard shortcuts"}, {Title: "Export a table to CSV"}, {Title: "Add dark mode", Used: true}}
	tests := []struct {
		name    string
		outcome string
		want    onboarding.State
		done    bool
	}{
		{
			name:    "merged",
			outcome: "merged",
			done:    true,
			want: onboarding.State{
				PlanID:      "plan-9",
				Suggestion:  "Add dark mode",
				Finished:    true,
				Suggestions: used,
			},
		},
		{
			name:    "closed",
			outcome: "closed",
			want:    onboarding.State{Suggestions: onboarding.Generic},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				w := newWorld(onboarding.State{PlanID: "plan-9", Suggestion: "Add dark mode", Suggestions: slices.Clone(used)}, onboardingtest.Found(ok(1)))
				w.store.SetFlow(plan.Flow{State: "ended", Ended: true, Outcome: tt.outcome}, nil)
				d := newDriver(t.Context(), t, w.deps())
				d.until("saves the end", func() bool { return w.state.Saved().Finished || w.state.Saved().PlanID == "" })
				d.idle(time.Second)
				assert.Check(t, cmp.DeepEqual(w.state.Saved(), tt.want))
				assert.Check(t, cmp.Equal(d.saw(onboarding.DoneMsg{}), tt.done))
				if !tt.done {
					assert.Check(t, d.shows("ended: closed")())
					assert.Check(t, d.shows("Add dark mode")())
				}
			})
		})
	}
}

func TestStuckSetup(t *testing.T) {
	stuck := chunkd.SetupActivity{State: chunkd.SetupStuck, Step: "sidecar", Checks: 1, Summary: "npm ci failed", Total: 4}

	t.Run("retry and continue", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := newWorld(onboarding.State{}, onboardingtest.Found(stuck))
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows the card", d.shows("◐ Fix the environment · you"))
			assert.Check(t, d.shows("npm ci failed")())
			assert.Check(t, d.shows("[r] Retry  [c] Continue without an image")())

			d.press('c')
			d.until("continues", func() bool { return w.setup.Continues() == 1 })

			w.setup.OnStart(running(1), nil)
			d.press('r')
			d.until("retries", func() bool { return len(w.setup.Starts()) == 1 })
			d.until("shows setup running", d.shows("● Setup · agent"))
		})
	})

	t.Run("reopened stuck", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := newWorld(onboarding.State{}, onboardingtest.Found(stuck))
			d := newDriver(t.Context(), t, w.deps())
			d.until("shows the summary", d.shows("npm ci failed"))
			d.idle(5 * time.Second)
			assert.Check(t, cmp.Len(w.setup.Starts(), 0))
		})
	})

	t.Run("reopened with no run", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			w := newWorld(onboarding.State{})
			w.setup.OnStart(running(-1), nil)
			d := newDriver(t.Context(), t, w.deps())
			d.until("starts setup", func() bool { return len(w.setup.Starts()) == 1 })
			d.idle(5 * time.Second)
			assert.Check(t, cmp.DeepEqual(w.setup.Starts(), []string{"org-1"}))
		})
	})
}

func TestUnsent(t *testing.T) {
	tests := []struct {
		name  string
		typed string
		// card, when set, is the card typed into, on a reopened plan whose
		// flow waits on that card's step.
		card string
	}{
		{
			name:  "own change",
			typed: "Add a CSV export",
		},
		{
			name: "nothing typed",
		},
		{
			name:  "question answer",
			typed: "Postgres",
			card:  "question",
		},
		{
			name:  "design comment",
			typed: "Say why",
			card:  "design",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				if tt.card == "" {
					w := newWorld(onboarding.State{}, onboardingtest.Found(running(-1)))
					d := newDriver(t.Context(), t, w.deps())
					d.until("shows setup", d.shows("● Setup"))
					d.focusOn("describe your own")
					d.typeText(tt.typed)
					assert.Check(t, cmp.Equal(d.m.Unsent(), tt.typed))
					return
				}
				event, want := answerOpenQuestions, "Which store?"
				if tt.card == "design" {
					event, want = approveForUnits, "Approve the design"
				}
				w := planWorld(steps(event), questions)
				d := newDriver(t.Context(), t, w.deps())
				d.until("shows the card", d.shows(want))
				if tt.card == "design" {
					d.press('c')
				}
				d.typeText(tt.typed)
				assert.Check(t, cmp.Equal(d.m.Unsent(), tt.typed))
			})
		})
	}
}

func TestOnboardingTelemetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, ev := fakes.Tracking(t.Context(), t)
		ctx = telemetry.WithProperties(ctx, map[string]any{"watch_session_id": "sitting-1"})
		w := newWorld(onboarding.State{}, onboardingtest.Found(running(-1)), onboardingtest.Found(running(-1)), onboardingtest.Found(ok(1)))
		d := newDriver(ctx, t, w.deps())
		d.until("shows setup", d.shows("● Setup"))
		d.focusOn("Add keyboard shortcuts")
		d.press(tea.KeyEnter)
		d.until("starts the change", func() bool { return len(w.agent.Requests()) == 1 })

		w.store.SetFlow(plan.Flow{State: "ended", Ended: true, Outcome: "merged"}, nil)
		d.until("ends", func() bool { return d.saw(onboarding.DoneMsg{}) })
		d.idle(5 * time.Second)

		assert.Check(t, cmp.DeepEqual(ev.Names(), []string{
			"onboarding_started", "onboarding_setup_finished", "onboarding_change_started", "onboarding_merged",
		}))
		for _, tr := range ev.Named("onboarding_started") {
			assert.Check(t, cmp.Equal(tr.Properties["watch_session_id"], "sitting-1"))
		}
		finished := ev.Named("onboarding_setup_finished")
		assert.Assert(t, cmp.Len(finished, 1))
		assert.Check(t, cmp.Equal(finished[0].Properties["outcome"], "ok"))
		_, numeric := finished[0].Properties["duration_s"].(int)
		assert.Check(t, numeric, "duration_s: %#v", finished[0].Properties["duration_s"])
		assert.Check(t, cmp.Equal(finished[0].Properties["watch_session_id"], "sitting-1"))
		started := ev.Named("onboarding_change_started")
		assert.Assert(t, cmp.Len(started, 1))
		assert.Check(t, cmp.Equal(started[0].Properties["source"], "suggested"))
		assert.Check(t, cmp.Equal(started[0].Properties["watch_session_id"], "sitting-1"))
		merged := ev.Named("onboarding_merged")
		assert.Assert(t, cmp.Len(merged, 1))
		assert.Check(t, cmp.Equal(merged[0].Properties["watch_session_id"], "sitting-1"))
	})
}
