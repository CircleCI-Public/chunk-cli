package watchd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
)

// auto is the policy a project with no configuration gets.
var auto = asyncPolicy{mode: config.AsyncValidateAuto, maxLines: DefaultAsyncMaxLines}

func sourceChange(lines int) gitutil.Changes {
	return gitutil.Changes{Paths: []string{"internal/cmd/validate.go"}, Lines: lines}
}

func TestASmallChangeIsBackgrounded(t *testing.T) {
	d := decideRisk(auto, false, sourceChange(42), nil)
	assert.Equal(t, d.async, true)
	assert.Assert(t, strings.Contains(d.reason, "42 lines"), "reason was %q", d.reason)
}

func TestALargeChangeBlocks(t *testing.T) {
	d := decideRisk(auto, false, sourceChange(912), nil)
	assert.Equal(t, d.async, false)
	assert.Assert(t, strings.Contains(d.reason, "912 lines"), "reason was %q", d.reason)
}

// The limit is a ceiling, not a target: a change exactly on it waits.
func TestAChangeOnTheLimitBlocks(t *testing.T) {
	assert.Equal(t, decideRisk(auto, false, sourceChange(DefaultAsyncMaxLines), nil).async, false)
	assert.Equal(t, decideRisk(auto, false, sourceChange(DefaultAsyncMaxLines-1), nil).async, true)
}

// Size is only consulted for changes that could plausibly fail a check. A docs
// rewrite is backgrounded however long it is.
func TestADocsOnlyChangeIsBackgroundedAtAnySize(t *testing.T) {
	ch := gitutil.Changes{Paths: []string{"README.md", "docs/CLI.md", "LICENSE"}, Lines: 5000}
	d := decideRisk(auto, false, ch, nil)
	assert.Equal(t, d.async, true)
	assert.Equal(t, d.reason, "only docs and text changed")
}

// One source file in among the docs is a source change.
func TestOneSourceFileAmongTheDocsIsJudgedOnSize(t *testing.T) {
	ch := gitutil.Changes{Paths: []string{"README.md", "internal/cmd/validate.go"}, Lines: 5000}
	assert.Equal(t, decideRisk(auto, false, ch, nil).async, false)
}

// A tree that cannot be measured says nothing about how large the change is,
// and guessing small is the one answer that could lose a failure.
func TestAnUnmeasurableChangeBlocks(t *testing.T) {
	d := decideRisk(auto, false, gitutil.Changes{}, errors.New("not a repo"))
	assert.Equal(t, d.async, false)
	assert.Assert(t, strings.Contains(d.reason, "not a repo"), "reason was %q", d.reason)
}

// Nothing to validate is nothing to wait for. Backgrounding it would spend a
// task and a later report on a run that does no work, so it stays here — and
// says nothing, because there is nothing a developer would want told.
func TestACleanTreeIsNotBackgrounded(t *testing.T) {
	d := decideRisk(auto, false, gitutil.Changes{}, nil)
	assert.Equal(t, d.async, false)
	assert.Equal(t, d.reason, "")
}

// The blocking safety net: a failure nobody was waiting for makes the next run
// one somebody is waiting for, however small the change.
func TestAProjectThatOwesABlockingRunGetsOne(t *testing.T) {
	d := decideRisk(auto, true, sourceChange(1), nil)
	assert.Equal(t, d.async, false)
	assert.Assert(t, strings.Contains(d.reason, "last run failed"), "reason was %q", d.reason)
}

func TestModeNeverBlocksEvenADocsChange(t *testing.T) {
	p := asyncPolicy{mode: config.AsyncValidateNever, maxLines: DefaultAsyncMaxLines}
	ch := gitutil.Changes{Paths: []string{"README.md"}, Lines: 2}
	assert.Equal(t, decideRisk(p, false, ch, nil).async, false)
}

func TestModeAlwaysBackgroundsALargeChange(t *testing.T) {
	p := asyncPolicy{mode: config.AsyncValidateAlways, maxLines: DefaultAsyncMaxLines}
	assert.Equal(t, decideRisk(p, false, sourceChange(100000), nil).async, true)
}

// An explicit setting beats the heuristic that would otherwise override it:
// a project that asked for every run to be backgrounded has opted out of the
// blocking safety net, and silently ignoring that would make the setting a
// suggestion.
func TestModeAlwaysOutranksTheFailureDebt(t *testing.T) {
	p := asyncPolicy{mode: config.AsyncValidateAlways, maxLines: DefaultAsyncMaxLines}
	assert.Equal(t, decideRisk(p, true, sourceChange(1), nil).async, true)
}

// Always means always, including for a tree whose size could not be read: the
// setting is about who waits, and a failed measurement does not change that
// answer for a project that has already given one.
func TestModeAlwaysBackgroundsAnUnmeasurableChange(t *testing.T) {
	p := asyncPolicy{mode: config.AsyncValidateAlways, maxLines: DefaultAsyncMaxLines}
	assert.Equal(t, decideRisk(p, false, gitutil.Changes{}, errors.New("nope")).async, true)
}

func TestAProjectCanRaiseItsOwnLimit(t *testing.T) {
	p := asyncPolicy{mode: config.AsyncValidateAuto, maxLines: 2000}
	assert.Equal(t, decideRisk(p, false, sourceChange(912), nil).async, true)
}

// A nonsensical limit falls back to the default rather than backgrounding
// nothing at all.
func TestANonPositiveLimitFallsBackToTheDefault(t *testing.T) {
	p := asyncPolicy{mode: config.AsyncValidateAuto, maxLines: -1}
	assert.Equal(t, decideRisk(p, false, sourceChange(42), nil).async, true)
}

func TestAllInert(t *testing.T) {
	for _, paths := range [][]string{
		{"README.md"},
		{"docs/CLI.md", "docs/HOOKS.markdown"},
		{"notes.TXT"},
		{"LICENSE", "CHANGELOG"},
	} {
		assert.Equal(t, allInert(paths), true, "expected inert: %v", paths)
	}
	for _, paths := range [][]string{
		{},
		{"go.sum"},
		{"Taskfile.yml"},
		{"Dockerfile.sandbox"},
		{"scripts/release.sh"},
		{"README.md", "main.go"},
		{"docs/gen.py"},
	} {
		assert.Equal(t, allInert(paths), false, "expected not inert: %v", paths)
	}
}

func TestPolicyForReadsTheProjectConfig(t *testing.T) {
	dir := t.TempDir()
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".chunk"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".chunk", "config.json"),
		[]byte(`{"asyncValidate":"never","asyncValidateMaxLines":50}`), 0o644))

	p := policyFor(dir)
	assert.Equal(t, p.mode, config.AsyncValidateNever)
	assert.Equal(t, p.maxLines, 50)
}

// A repo that has never configured anything is the normal case, not a reason to
// change how validation behaves.
func TestPolicyForFallsBackToAuto(t *testing.T) {
	p := policyFor(t.TempDir())
	assert.Equal(t, p.mode, config.AsyncValidateAuto)
	assert.Equal(t, p.maxLines, DefaultAsyncMaxLines)
}

func TestRiskMemoryRemembersAFailureUntilItPasses(t *testing.T) {
	m := newRiskMemory()
	root := t.TempDir()

	assert.Equal(t, m.owesBlockingRun(root), false)
	m.record(root, false)
	assert.Equal(t, m.owesBlockingRun(root), true)
	m.record(root, true)
	assert.Equal(t, m.owesBlockingRun(root), false)
}

func TestRiskMemoryIsPerProject(t *testing.T) {
	m := newRiskMemory()
	a, b := t.TempDir(), t.TempDir()

	m.record(a, false)
	assert.Equal(t, m.owesBlockingRun(a), true)
	assert.Equal(t, m.owesBlockingRun(b), false, "one project's failure leaked into another")
}

// writeSource dirties dir with an untracked Go file of n lines, so the daemon
// measures a change of a known size against a real tree.
func writeSource(t *testing.T, dir string, n int) {
	t.Helper()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "main.go"),
		[]byte(strings.Repeat("// line\n", n)), 0o644))
}

func validateReq(t *testing.T, req ValidateRequest) *http.Request {
	t.Helper()
	body, err := json.Marshal(req)
	assert.NilError(t, err)
	return httptest.NewRequest(http.MethodPost, "/validate", bytes.NewReader(body))
}

// decode reads a validate response, failing the test if the body is not one.
func decodeValidate(t *testing.T, rec *httptest.ResponseRecorder) ValidateResponse {
	t.Helper()
	assert.Equal(t, rec.Code, http.StatusOK)
	var resp ValidateResponse
	assert.NilError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

// riskDaemon returns a daemon and a repo, with a runner that reports exitCode
// and records how many times it ran.
func riskDaemon(t *testing.T, exitCode int) (*daemon, string, func() int) {
	t.Helper()
	root := gitRepo(t)
	d := newTestDaemon()
	t.Cleanup(d.tasks.stopAll)

	var mu sync.Mutex
	runs := 0
	d.runner = func(context.Context, []string, []string, io.Writer, io.Writer) int {
		mu.Lock()
		runs++
		mu.Unlock()
		return exitCode
	}
	return d, root, func() int {
		mu.Lock()
		defer mu.Unlock()
		return runs
	}
}

// The offer taken: the response carries a task ID, and no verdict, because
// nothing has run by the time the caller is let go.
func TestValidateReleasesACallerForASmallChange(t *testing.T) {
	d, root, _ := riskDaemon(t, 0)
	writeSource(t, root, 10)

	resp := decodeValidate(t, serve(d, validateReq(t, ValidateRequest{
		Args: []string{"validate"}, ProjectRoot: root, AllowAsync: true,
	})))
	assert.Assert(t, resp.TaskID != "", "the caller was not released")
	assert.Equal(t, resp.Stdout, "")
	assert.Assert(t, strings.Contains(resp.Reason, "small change"), "reason was %q", resp.Reason)

	waitFor(t, func() bool { return len(d.tasks.inFlight(root)) == 0 }, "run never finished")
	assert.Equal(t, len(d.tasks.collect(root)), 1, "the background result was not kept")
}

// The offer declined: a large change runs here, and the response says why the
// caller was kept waiting.
func TestValidateHoldsACallerForALargeChange(t *testing.T) {
	d, root, runs := riskDaemon(t, 0)
	writeSource(t, root, DefaultAsyncMaxLines+1)

	resp := decodeValidate(t, serve(d, validateReq(t, ValidateRequest{
		Args: []string{"validate"}, ProjectRoot: root, AllowAsync: true,
	})))
	assert.Equal(t, resp.TaskID, "", "a large change was backgrounded")
	assert.Equal(t, runs(), 1, "the run did not happen while the caller waited")
	assert.Assert(t, strings.Contains(resp.Reason, "large change"), "reason was %q", resp.Reason)
}

// No offer, no decision: a caller with nowhere to hear the answer later is
// never released, however small the change, and gets no explanation because
// nothing was decided on its behalf.
func TestValidateNeverReleasesACallerThatDidNotOffer(t *testing.T) {
	d, root, runs := riskDaemon(t, 0)
	writeSource(t, root, 1)

	resp := decodeValidate(t, serve(d, validateReq(t, ValidateRequest{
		Args: []string{"validate"}, ProjectRoot: root,
	})))
	assert.Equal(t, resp.TaskID, "")
	assert.Equal(t, resp.Reason, "")
	assert.Equal(t, runs(), 1)
}

// The loop that makes background validation safe: a failure found with nobody
// waiting for it forces the next run to be one somebody is waiting for.
func TestABackgroundFailureHoldsTheNextCaller(t *testing.T) {
	d, root, _ := riskDaemon(t, 1)
	writeSource(t, root, 10)

	first := decodeValidate(t, serve(d, validateReq(t, ValidateRequest{
		Args: []string{"validate"}, ProjectRoot: root, AllowAsync: true,
	})))
	assert.Assert(t, first.TaskID != "", "the first caller was not released")
	waitFor(t, func() bool { return d.risk.owesBlockingRun(root) }, "the failure was not remembered")

	second := decodeValidate(t, serve(d, validateReq(t, ValidateRequest{
		Args: []string{"validate"}, ProjectRoot: root, AllowAsync: true,
	})))
	assert.Equal(t, second.TaskID, "", "a project that owes a blocking run was released again")
	assert.Assert(t, strings.Contains(second.Reason, "last run failed"), "reason was %q", second.Reason)
}

// And the loop closes: the blocking run passes, so the next one is free to be
// backgrounded again.
func TestAPassingRunClearsTheDebt(t *testing.T) {
	d, root, _ := riskDaemon(t, 0)
	writeSource(t, root, 10)
	d.risk.record(root, false)

	held := decodeValidate(t, serve(d, validateReq(t, ValidateRequest{
		Args: []string{"validate"}, ProjectRoot: root, AllowAsync: true,
	})))
	assert.Equal(t, held.TaskID, "", "a project that owes a blocking run was released")
	assert.Equal(t, d.risk.owesBlockingRun(root), false, "a passing run left the debt in place")

	// A further edit, because the run above has just validated this tree: with
	// nothing changed since, the next request is decided as "no changes" and
	// correctly runs nothing.
	writeSource(t, root, 20)
	released := decodeValidate(t, serve(d, validateReq(t, ValidateRequest{
		Args: []string{"validate"}, ProjectRoot: root, AllowAsync: true,
	})))
	assert.Assert(t, released.TaskID != "", "the caller was not released once the debt was clear")
}

// A run with no project named cannot be measured, filed or remembered, so it is
// simply run.
func TestValidateWithoutAProjectRootIsRun(t *testing.T) {
	d, _, runs := riskDaemon(t, 0)

	resp := decodeValidate(t, serve(d, validateReq(t, ValidateRequest{
		Args: []string{"validate"}, AllowAsync: true,
	})))
	assert.Equal(t, resp.TaskID, "")
	assert.Equal(t, runs(), 1)
}

// The accumulation problem, and the whole reason the baseline exists: six
// turns of 100 lines each are six small changes, not one 600-line change.
// Measured against HEAD the fifth turn onwards would be held, having been
// released four turns running for work of exactly the same size.
func TestSmallTurnsDoNotAccumulateIntoALargeChange(t *testing.T) {
	d, root, _ := riskDaemon(t, 0)

	var last ValidateResponse
	for turn := 1; turn <= 6; turn++ {
		writeSource(t, root, turn*100)
		last = decodeValidate(t, serve(d, validateReq(t, ValidateRequest{
			Args: []string{"validate"}, ProjectRoot: root, AllowAsync: true,
		})))
		assert.Assert(t, last.TaskID != "", "turn %d was held: %s", turn, last.Reason)
		// Wait for the run to finish, so the state it validated is the baseline
		// the next turn measures from — which is what the next hook firing would
		// find in a real session.
		waitFor(t, func() bool { return len(d.tasks.inFlight(root)) == 0 }, "run never finished")
		d.tasks.collect(root)
	}

	// The sixth turn is measured from the fifth, not from the commit six turns
	// back: 100 lines, not 600.
	assert.Assert(t, strings.Contains(last.Reason, "100 lines"), "reason was %q", last.Reason)
	assert.Assert(t, strings.Contains(last.Reason, "since the last passing run"), "reason was %q", last.Reason)
}

// A failing run leaves the last known-good state alone. It is still the last
// state that passed, and the next measurement is still from there.
func TestAFailedRunDoesNotMoveTheBaseline(t *testing.T) {
	m := newRiskMemory()
	root := t.TempDir()

	m.recordGreen(root, "tree-a")
	m.record(root, false)
	assert.Equal(t, m.baseline(root), "tree-a")
}

func TestRiskMemoryHasNoBaselineUntilARunPasses(t *testing.T) {
	m := newRiskMemory()
	assert.Equal(t, m.baseline(t.TempDir()), "")
}

// A tree git has since collected is not a measurement error, just a worse
// baseline: the assessment falls back to HEAD and carries on.
func TestAnUnknownBaselineFallsBackToHead(t *testing.T) {
	d, root, _ := riskDaemon(t, 0)
	writeSource(t, root, 10)
	d.risk.recordGreen(root, "0000000000000000000000000000000000000000")

	resp := decodeValidate(t, serve(d, validateReq(t, ValidateRequest{
		Args: []string{"validate"}, ProjectRoot: root, AllowAsync: true,
	})))
	assert.Assert(t, resp.TaskID != "", "a collected baseline stopped the assessment")
	assert.Assert(t, !strings.Contains(resp.Reason, "since the last passing run"),
		"the fallback claimed a baseline it could not read: %q", resp.Reason)
}
