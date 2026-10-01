package watchd

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// loopRig is a daemon wired to a stand-in sandbox that behaves like the real
// thing where it matters: "syncing" makes the sandbox a clone of the session's
// worktree, uncommitted work included, the scripts the daemon runs there are
// run for real, and validation commands run for real too. The only things
// faked are the Claude calls: what the implementer edits and what the
// reviewers say. The user's repository is real git in a temp dir.
type loopRig struct {
	t       *testing.T
	d       *daemon
	root    string
	sandbox string
	backend *fakeBackend

	// implement is what the implementer edits in its sandbox, given its prompt.
	implement func(sandbox, prompt string)
	// review is what the reviewers say, given the sandbox they are looking at.
	review func(sandbox string) string

	mu sync.Mutex
	// turns are the implementer's runs, in order: the prompt and the script.
	turns []turn
}

type turn struct{ prompt, script string }

var (
	scriptPromptRe  = regexp.MustCompile(`echo (\S+) \| base64 -d`)
	scriptSessionRe = regexp.MustCompile(`'--(session-id|resume)' '([^']+)'`)
)

func newLoopRig(t *testing.T) *loopRig {
	t.Helper()
	r := &loopRig{t: t, sandbox: filepath.Join(t.TempDir(), "sandbox")}
	r.implement = func(string, string) {}
	r.review = func(string) string { return findingsOutput(t) }
	r.backend = &fakeBackend{repoPath: r.sandbox}
	r.d, r.root = newSessionDaemon(t, r.backend)

	// The user's project: a committed app.go, plus the dirt of a developer
	// mid-task, none of which the session may touch.
	put(t, r.root, "app.go", "package app\n")
	put(t, r.root, "other.txt", "o0\n")
	git(t, r.root, "add", "app.go", "other.txt")
	git(t, r.root, "commit", "-q", "-m", "files")
	put(t, r.root, "other.txt", "o1\n")   // unstaged edit
	put(t, r.root, "notes.txt", "mine\n") // untracked

	r.backend.onPool = func(spec ReviewPoolSpec) {
		// Sync: the worktree's HEAD, then its files on top.
		assert.NilError(t, os.RemoveAll(r.sandbox))
		run(t, "", "git", "clone", "-q", spec.WorkDir, r.sandbox)
		run(t, "", "rsync", "-a", "--delete", "--exclude=/.git", spec.WorkDir+"/", r.sandbox+"/")
	}
	r.backend.respond = func(script string) (string, int) {
		switch {
		case strings.Contains(script, "git diff --binary"), strings.Contains(script, "git write-tree"):
			return sh(script)
		case strings.Contains(script, "--dangerously-skip-permissions"):
			prompt := decodePrompt(t, script)
			r.mu.Lock()
			r.turns = append(r.turns, turn{prompt: prompt, script: script})
			r.mu.Unlock()
			r.implement(r.sandbox, prompt)
			id := "fresh"
			if m := scriptSessionRe.FindStringSubmatch(script); m != nil {
				id = m[2]
			}
			return `{"type":"system","session_id":"` + id + `"}` + "\n" +
				`{"type":"result","is_error":false,"result":"Done.","total_cost_usd":0.25}` + "\n", 0
		case strings.Contains(script, "--json-schema"):
			return r.review(r.sandbox), 0
		}
		// A validation command.
		return sh(script)
	}
	return r
}

func sh(script string) (string, int) {
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}
	return string(out), 0
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	c := exec.Command(name, args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	assert.NilError(t, err, string(out))
}

func decodePrompt(t *testing.T, script string) string {
	t.Helper()
	m := scriptPromptRe.FindStringSubmatch(script)
	assert.Assert(t, m != nil, "no prompt in script: %s", script)
	b, err := base64.StdEncoding.DecodeString(strings.Trim(m[1], "'"))
	assert.NilError(t, err)
	return string(b)
}

func (r *loopRig) start(req SessionRequest) SessionDetail {
	r.t.Helper()
	req.ProjectRoot = r.root
	if req.Task == "" {
		req.Task = "make app.go say hello"
	}
	sess, err := r.d.startSession(req)
	assert.NilError(r.t, err)
	return waitForSession(r.t, r.d, sess.ID)
}

func (r *loopRig) turnsRun() []turn {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]turn(nil), r.turns...)
}

// writesHello is an implementer that writes the task, with a bug in it the
// first time.
func writesHello(t *testing.T) func(string, string) {
	return func(sandbox, prompt string) {
		if strings.Contains(prompt, "BUG") {
			put(t, sandbox, "app.go", "package app\n\nconst Hello = \"hello\"\n")
			return
		}
		put(t, sandbox, "app.go", "package app\n\nconst Hello = \"BUG\"\n")
		put(t, sandbox, "hello_test.go", "package app\n")
	}
}

// reportsBug is a reviewer that finds the bug while it is there.
func reportsBug(t *testing.T) func(string) string {
	return func(sandbox string) string {
		if !strings.Contains(read(t, sandbox, "app.go"), "BUG") {
			return findingsOutput(t)
		}
		return findingsOutput(t, review.Finding{File: "app.go", Line: 3, Severity: "high", Body: "BUG must go"})
	}
}

// userState captures everything of the user's a session must leave alone.
func userState(t *testing.T, root string) string {
	t.Helper()
	return git(t, root, "status", "--porcelain=v1", "--untracked-files=all") +
		git(t, root, "rev-parse", "HEAD") + git(t, root, "symbolic-ref", "HEAD") +
		read(t, root, "other.txt") + read(t, root, "app.go")
}

func TestSessionImplementsChecksFixesAndCommitsTheWorkToABranch(t *testing.T) {
	r := newLoopRig(t)
	r.implement, r.review = writesHello(t), reportsBug(t)
	before := userState(t, r.root)

	detail := r.start(SessionRequest{})

	assert.Equal(t, detail.State, SessionDone, detail.Error)
	assert.Equal(t, detail.Outcome, OutcomePassed)
	assert.Equal(t, detail.Implement.State, FixApplied)
	assert.Equal(t, detail.Implement.Summary, "Done.")
	assert.Equal(t, len(detail.Implement.Files), 2)
	assert.Equal(t, len(detail.Rounds), 2, "one round finds the bug and fixes it, the next passes")
	r1, r2 := detail.Rounds[0], detail.Rounds[1]
	assert.Equal(t, r1.Worth, 1)
	assert.Equal(t, r1.Fix.State, FixApplied)
	assert.DeepEqual(t, r1.Fix.Files, []FileChange{{Path: "app.go", Insertions: 1, Deletions: 1}})
	assert.Equal(t, r2.Worth, 0)
	assert.Assert(t, r2.Fix == nil)
	assert.Equal(t, r2.Note, "every check passed")
	var stages []string
	for _, s := range detail.Stages {
		stages = append(stages, string(s.ID)+"="+string(s.State))
	}
	assert.DeepEqual(t, stages, []string{
		"implement=done", "review_loop=done", "rebase=not_built", "ci=not_built", "approval=not_built", "pr=not_built",
	})

	// The work is committed on the session's branch, and the worktree is gone.
	assert.Equal(t, detail.WorkBranch, workBranch(detail.ID))
	assert.Equal(t, detail.WorkDir, "")
	assert.Equal(t, git(t, r.root, "rev-parse", detail.WorkBranch), detail.WorkCommit)
	assert.Equal(t, git(t, r.root, "show", detail.WorkBranch+":app.go"), "package app\n\nconst Hello = \"hello\"")
	msg := git(t, r.root, "log", "-1", "--format=%B", detail.WorkBranch)
	assert.Assert(t, strings.HasPrefix(msg, "make app.go say hello\n"), msg)
	assert.Equal(t, strings.Count(git(t, r.root, "worktree", "list"), "\n"), 0, "only the user's own checkout is left")

	// Nothing of the user's moved.
	assert.Equal(t, userState(t, r.root), before)
	assert.Equal(t, read(t, r.root, "notes.txt"), "mine\n")
}

// The work starts from the user's files as they are, uncommitted changes
// included, but those changes are the baseline, not part of the work.
func TestSessionStartsFromTheUsersUncommittedWorkAndKeepsItOutOfTheChange(t *testing.T) {
	r := newLoopRig(t)
	var sawDirt bool
	r.implement = func(sandbox, _ string) {
		sawDirt = read(t, sandbox, "other.txt") == "o1\n" && read(t, sandbox, "notes.txt") == "mine\n"
		put(t, sandbox, "app.go", "package app // done\n")
	}

	detail := r.start(SessionRequest{})

	assert.Equal(t, detail.Outcome, OutcomePassed, detail.Error)
	assert.Assert(t, sawDirt, "the implementer works on the user's files as they were")
	assert.DeepEqual(t, detail.Implement.Files, []FileChange{{Path: "app.go", Insertions: 1, Deletions: 1}})
	baseline := detail.WorkBranch + "~1"
	assert.Equal(t, git(t, r.root, "rev-parse", baseline+"~1"), git(t, r.root, "rev-parse", "HEAD"))
	assert.Equal(t, git(t, r.root, "show", baseline+":other.txt"), "o1")
	assert.Equal(t, git(t, r.root, "diff", "--name-only", baseline, detail.WorkBranch), "app.go",
		"the work commit holds the work and nothing of the user's")
}

// Every turn runs on the same sidecar and continues the first turn's Claude
// session, so feedback arrives with the context of the work so far. The task is
// restated anyway, for a turn that has to start over elsewhere.
func TestSessionKeepsTheImplementerOnOneSidecarAndResumesItsSession(t *testing.T) {
	r := newLoopRig(t)
	r.implement, r.review = writesHello(t), reportsBug(t)

	detail := r.start(SessionRequest{Task: "make app.go say hello"})

	assert.Equal(t, detail.Outcome, OutcomePassed, detail.Error)
	turns := r.turnsRun()
	assert.Equal(t, len(turns), 2)
	first := scriptSessionRe.FindStringSubmatch(turns[0].script)
	second := scriptSessionRe.FindStringSubmatch(turns[1].script)
	assert.Equal(t, first[1], "session-id")
	assert.Equal(t, second[1], "resume")
	assert.Equal(t, second[2], first[2])
	assert.Equal(t, detail.Implement.SidecarID, detail.Rounds[0].Fix.SidecarID)
	assert.Equal(t, turns[0].prompt, "make app.go say hello")
	assert.Assert(t, strings.Contains(turns[1].prompt, "> make app.go say hello"), turns[1].prompt)
	assert.Assert(t, strings.Contains(turns[1].prompt, "app.go:3"), turns[1].prompt)
}

func TestSessionWithAnImplementerThatChangesNothingKeepsNoBranch(t *testing.T) {
	r := newLoopRig(t)
	// Without uncommitted work, there is no baseline commit worth a branch.
	git(t, r.root, "add", "-A")
	git(t, r.root, "commit", "-q", "-m", "clean")

	detail := r.start(SessionRequest{})

	assert.Equal(t, detail.State, SessionDone, detail.Error)
	assert.Equal(t, detail.Outcome, OutcomeNoChange)
	assert.Equal(t, detail.Implement.State, FixEmpty)
	assert.Equal(t, len(detail.Rounds), 0)
	assert.Equal(t, detail.Stages[0].State, StageDone)
	assert.Equal(t, detail.Stages[1].State, StageSkipped)
	assert.Equal(t, detail.WorkBranch, "")
	assert.Equal(t, git(t, r.root, "branch", "--list", workBranchPrefix+"*"), "")
}

func TestSessionRunsAtMostTheRequestedRounds(t *testing.T) {
	counter := func(sandbox string) int {
		n, _ := strconv.Atoi(strings.TrimSpace(read(t, sandbox, "n.txt")))
		return n
	}
	for _, tc := range []struct{ requested, want int }{{0, DefaultRounds}, {2, 2}, {5, 5}} {
		r := newLoopRig(t)
		// Always something to fix: the reviewer never lets up, and the
		// implementer only ever bumps a counter.
		r.implement = func(sandbox, _ string) {
			if !exists(sandbox, "n.txt") {
				put(t, sandbox, "n.txt", "0\n")
				return
			}
			put(t, sandbox, "n.txt", strconv.Itoa(counter(sandbox)+1)+"\n")
		}
		r.review = func(string) string {
			return findingsOutput(t, review.Finding{File: "n.txt", Line: 1, Severity: "medium", Body: "still not done"})
		}

		detail := r.start(SessionRequest{MaxRounds: tc.requested})

		assert.Equal(t, detail.State, SessionDone, detail.Error)
		assert.Equal(t, detail.Outcome, OutcomeExhausted)
		assert.Equal(t, len(detail.Rounds), tc.want, "requested %d", tc.requested)
		assert.Equal(t, git(t, r.root, "show", detail.WorkBranch+":n.txt"), strconv.Itoa(tc.want))
		assert.Equal(t, detail.Stages[1].Note, "stopped after "+strconv.Itoa(tc.want)+" round(s)")
		r.d.sessions.stopAll()
	}
}

func TestSessionLeavesLowSeverityFindingsAlone(t *testing.T) {
	r := newLoopRig(t)
	r.implement = writesHello(t)
	r.review = func(string) string {
		return findingsOutput(t,
			review.Finding{File: "app.go", Line: 1, Severity: "low", Body: "naming"},
			review.Finding{File: "app.go", Line: 1, Severity: "info", Body: "fyi"})
	}

	detail := r.start(SessionRequest{})

	assert.Equal(t, detail.Outcome, OutcomePassed, detail.Error)
	assert.Equal(t, len(detail.Rounds), 1)
	assert.Equal(t, detail.Rounds[0].Findings, 2)
	assert.Equal(t, detail.Rounds[0].Worth, 0)
	assert.Equal(t, len(r.turnsRun()), 1, "the implementer is not sent back for style remarks")
}

// writeValidation configures the project's validation commands.
func writeValidation(t *testing.T, root string, cmds ...map[string]any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"commands": cmds})
	assert.NilError(t, err)
	put(t, root, ".chunk/config.json", string(raw))
}

func TestAFailedValidationCommandIsFedBackToTheImplementer(t *testing.T) {
	r := newLoopRig(t)
	writeValidation(t, r.root,
		map[string]any{"name": "test", "run": `grep -q hello app.go || { echo "FAIL: app.go does not say hello"; exit 1; }`},
		map[string]any{"name": "fmt", "run": "true", "role": "autofix"}, // fixes rather than checks: not run
	)
	r.implement = func(sandbox, prompt string) {
		if strings.Contains(prompt, "FAIL: app.go does not say hello") {
			put(t, sandbox, "app.go", "package app // hello\n")
			return
		}
		put(t, sandbox, "app.go", "package app // hi\n")
	}

	detail := r.start(SessionRequest{})

	assert.Equal(t, detail.Outcome, OutcomePassed, detail.Error)
	assert.Equal(t, len(detail.Rounds), 2)
	r1 := detail.Rounds[0]
	var names []string
	for _, c := range r1.Reviews {
		names = append(names, string(c.Kind)+":"+c.Name)
	}
	assert.DeepEqual(t, names, []string{":bugs", ":style", "validate:test"})
	check := r1.Reviews[2]
	assert.Equal(t, check.State, PromptDone)
	assert.Equal(t, check.Findings, 1)
	assert.Assert(t, check.CommandID != "", "the command's log is reachable")
	assert.Equal(t, r1.Worth, 1)
	assert.Equal(t, detail.Rounds[1].Reviews[2].Findings, 0)
	assert.Equal(t, git(t, r.root, "show", detail.WorkBranch+":app.go"), "package app // hello")
}

func TestSessionChecksWithValidationAloneWhenThereAreNoPrompts(t *testing.T) {
	r := newLoopRig(t)
	assert.NilError(t, os.RemoveAll(filepath.Join(r.root, ".chunk", "reviews")))
	writeValidation(t, r.root, map[string]any{"name": "test", "run": "true"})
	r.implement = writesHello(t)

	detail := r.start(SessionRequest{})

	assert.Equal(t, detail.Outcome, OutcomePassed, detail.Error)
	assert.Equal(t, len(detail.Rounds[0].Reviews), 1)
}

// A check that could not run has not shown the work is clean: the next round
// checks it again rather than calling it passed.
func TestACheckThatCouldNotRunIsNotAPass(t *testing.T) {
	r := newLoopRig(t)
	r.implement = writesHello(t)
	r.review = func(string) string { return "not a claude result" }
	writeValidation(t, r.root, map[string]any{"name": "test", "run": "true"})

	detail := r.start(SessionRequest{MaxRounds: 2})

	assert.Equal(t, detail.State, SessionDone, detail.Error)
	assert.Equal(t, detail.Outcome, OutcomeExhausted)
	assert.Equal(t, len(detail.Rounds), 2)
	assert.Assert(t, strings.Contains(detail.Rounds[0].Note, "2 of 3 checks could not run"), detail.Rounds[0].Note)
}

func TestSessionFailsWhenNoCheckCanRun(t *testing.T) {
	r := newLoopRig(t)
	r.implement = writesHello(t)
	r.review = func(string) string { return "not a claude result" }

	detail := r.start(SessionRequest{})

	assert.Equal(t, detail.State, SessionFailed)
	assert.Assert(t, strings.Contains(detail.Error, "every check failed to run"), detail.Error)
	assert.Equal(t, detail.Stages[1].State, StageFailed)
	// What the implementer wrote is kept even so.
	assert.Equal(t, git(t, r.root, "show", detail.WorkBranch+":hello_test.go"), "package app")
}

func TestStuckImplementerEndsTheLoop(t *testing.T) {
	r := newLoopRig(t)
	r.review = reportsBug(t)
	r.implement = func(sandbox, prompt string) {
		if !strings.Contains(prompt, "Findings:") {
			put(t, sandbox, "app.go", "BUG\n")
		}
	}

	detail := r.start(SessionRequest{})

	assert.Equal(t, detail.Outcome, OutcomeStuck)
	assert.Equal(t, detail.Rounds[0].Fix.State, FixEmpty)
	assert.Equal(t, len(detail.Rounds), 1)
}

// CI definitions are fair game, since the task may be about CI; the work is on
// a branch for the developer to read before it goes anywhere.
func TestTheImplementerMayEditCIConfig(t *testing.T) {
	r := newLoopRig(t)
	r.implement = func(sandbox, _ string) { put(t, sandbox, ".circleci/config.yml", "version: 2.1\n") }

	detail := r.start(SessionRequest{Task: "add a CircleCI config"})

	assert.Equal(t, detail.Outcome, OutcomePassed, detail.Error)
	assert.Equal(t, git(t, r.root, "show", detail.WorkBranch+":.circleci/config.yml"), "version: 2.1")
}

func TestSessionRefusesTooManyRounds(t *testing.T) {
	r := newLoopRig(t)
	_, err := r.d.startSession(SessionRequest{ProjectRoot: r.root, Task: "x", MaxRounds: MaxRounds + 1})
	var ae *apiError
	assert.Assert(t, errors.As(err, &ae) && ae.status == http.StatusBadRequest, "got %v", err)
}
