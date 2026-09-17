package watchd

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/CircleCI-Public/chunk-cli/internal/changeset"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/filestate"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
)

// DefaultAsyncMaxLines is how large a change may be, in lines, and still be
// validated in the background.
//
// The number is a judgement, not a measurement. What it is really choosing is
// how much of the inner loop runs without waiting: nearly every edit an agent
// makes in one turn lands under it, and the changes that do not are the ones
// where a developer is most likely to want the answer before doing anything
// else. Projects that disagree can say so — see config.AsyncValidateMaxLines.
const DefaultAsyncMaxLines = 500

// inertExtensions are extensions whose contents no validate command is expected
// to fail on, so a change confined to them can be validated in the background
// however large it is.
//
// The list is an allowlist rather than a list of source extensions, because the
// two fail in opposite directions. An allowlist meets an unfamiliar extension by
// running the checks and making the developer wait; a source list meets one by
// backgrounding the run and hoping. A repo that lints its markdown is the case
// this gets wrong, and it gets it wrong by a turn's delay rather than by missing
// a failure — the result still arrives, and a failure still blocks the next run.
var inertExtensions = map[string]bool{
	".md":       true,
	".markdown": true,
	".txt":      true,
	".rst":      true,
	".adoc":     true,
}

// inertNames are extensionless files that are prose by convention.
var inertNames = map[string]bool{
	"LICENSE":   true,
	"NOTICE":    true,
	"AUTHORS":   true,
	"CHANGELOG": true,
}

// allInert reports whether every path is one no check should care about. An
// empty set is not inert: it says nothing changed, which is a different answer.
func allInert(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		base := filepath.Base(p)
		if inertNames[base] {
			continue
		}
		if inertExtensions[strings.ToLower(filepath.Ext(base))] {
			continue
		}
		return false
	}
	return true
}

// asyncPolicy is what a project's configuration says about background
// validation.
type asyncPolicy struct {
	mode     string
	maxLines int
	// worktree runs background checks in a snapshot rather than the live tree.
	worktree bool
}

// policyFor reads root's async validation policy. A project with no config, or
// one that cannot be read, gets the defaults: a missing .chunk/config.json is
// the normal state of a repo that has never configured anything, not a reason to
// change how validation behaves.
func policyFor(root string) asyncPolicy {
	p := asyncPolicy{mode: config.AsyncValidateAuto, maxLines: DefaultAsyncMaxLines}
	cfg, err := config.LoadProjectConfig(root)
	if err != nil || cfg == nil {
		return p
	}
	if cfg.AsyncValidate != "" {
		p.mode = cfg.AsyncValidate
	}
	if cfg.AsyncValidateMaxLines > 0 {
		p.maxLines = cfg.AsyncValidateMaxLines
	}
	p.worktree = cfg.AsyncValidateWorktree
	return p
}

// riskDecision is the daemon's answer to whether a run can be released.
type riskDecision struct {
	// async is true when the caller may be let go and told the answer later.
	async bool
	// reason says why, in words meant for a developer reading one line of hook
	// output. It is filled either way: "why did that block" is asked at least as
	// often as "why did that not".
	reason string
	// risk summarises the change the decision was made about. It travels with
	// every decision, including the ones reached without consulting it.
	risk RiskSummary
}

// decideRisk judges whether a validate run against a measured tree can be
// released to the background.
//
// The judgement is about consequence, not correctness: every change is validated
// either way, and the only question is who waits for the answer. So it is
// deliberately lopsided. Anything it cannot measure, and anything that has
// recently failed, is made to block — the direction that costs a developer time
// rather than the direction that costs them a missed failure.
func decideRisk(p asyncPolicy, owesBlockingRun bool, ch changeset.Changes, chErr error, hist historyEvidence) riskDecision {
	since := ""
	if ch.Incremental() {
		since = " since the last passing run"
	}
	limit := p.maxLines
	if limit <= 0 {
		limit = DefaultAsyncMaxLines
	}

	risk := scoreChange(limit, owesBlockingRun, ch, chErr, hist)

	switch p.mode {
	case config.AsyncValidateNever:
		return riskDecision{reason: "background validation is off for this project", risk: risk}
	case config.AsyncValidateAlways:
		// Ahead of the failure debt below on purpose: a project that has asked for
		// every run to be backgrounded has opted out of the blocking safety net,
		// and quietly overriding that would make the setting a suggestion.
		return riskDecision{async: true, reason: "background validation is always on for this project", risk: risk}
	}

	// A failure the developer has not been shown the resolution of yet. The next
	// run blocks so it lands where a hook can act on it, and the debt clears as
	// soon as a run passes.
	if owesBlockingRun {
		return riskDecision{reason: "the last run failed, so this one blocks", risk: risk}
	}

	if chErr != nil {
		// The tree could not be measured, so nothing is known about how large this
		// change is. Guessing small is the one answer that could lose a failure.
		return riskDecision{reason: fmt.Sprintf("change size unavailable: %v", chErr), risk: risk}
	}
	if ch.Empty() {
		// Nothing to validate, so nothing to wait for: the run skips immediately,
		// and backgrounding it would spend a task and a later report on a run that
		// does no work. No reason either — nothing was held back that a developer
		// would want explained, and a line on every clean turn is just noise.
		return riskDecision{risk: risk}
	}
	if allInert(ch.Paths) {
		return tighten(riskDecision{async: true, reason: "only docs and text changed", risk: risk}, hist)
	}
	if ch.Lines < limit {
		return tighten(riskDecision{async: true, reason: fmt.Sprintf("small change%s, %s", since, lineCount(ch.Lines)), risk: risk}, hist)
	}
	return riskDecision{reason: fmt.Sprintf("large change%s, %s, over the %d-line limit", since, lineCount(ch.Lines), limit), risk: risk}
}

// tighten holds a run the rules would have released, when this repo's own
// history says changes like it usually fail.
//
// Applied to the release paths only, and only under the auto mode, so history
// is never able to loosen anything: a project whose changes are all enormous
// cannot teach the daemon that enormous is fine, and a project that set
// "always" is not quietly overridden. The worst a wrong tightening does is make
// somebody wait for a run that was going to pass.
func tighten(d riskDecision, hist historyEvidence) riskDecision {
	if !d.async || !hist.suggestsCaution() {
		return d
	}
	d.async = false
	d.reason = fmt.Sprintf("%d of the last %d changes this size failed here, so this one blocks",
		hist.Failed, hist.Similar)
	return d
}

// lineCount renders a line total for a human, singular included.
func lineCount(n int) string {
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

// riskMemory remembers which projects owe a blocking validate run.
//
// It exists because a background failure has nobody to report to. The run that
// found it finished after its caller was released, and the hook that could have
// blocked on it has already exited — so the failure is recorded here instead,
// and the next run for that project is made to block, where a hook can act on
// what it finds. A pass clears the debt.
//
// This is also what keeps a discarded result from being a lost one. A run whose
// tree moved while it was in flight is dropped rather than reported, since it
// describes code that is no longer on disk; if it dropped a failure, the debt it
// left behind still forces the next run to block.
//
// In memory, like every other store here: a daemon restart forgets the debt, and
// the following run is backgrounded when it should have blocked. The failure is
// not lost by that — the next run still finds it — it just arrives a turn later.
type riskMemory struct {
	mu     sync.Mutex
	failed map[string]bool // canonical project root → owes a blocking run
	// green is the state each project last passed its checks in, and what the
	// next change is measured from. See recordGreen.
	green map[string]treeState
}

func newRiskMemory() *riskMemory {
	return &riskMemory{
		failed: make(map[string]bool),
		green:  make(map[string]treeState),
	}
}

// treeState is what a project looked like at a point in time, kept so a later
// change can be measured from it. At most one of its fields is set: a git tree
// object where git could answer for the tree, and a hashed walk where it could
// not — a repository with no commits, or a directory that is not one.
type treeState struct {
	tree  string
	index filestate.Index
}

// known reports whether this is a state anything can be measured against.
func (s treeState) known() bool { return s.tree != "" || s.index != nil }

// record notes how a run for root ended.
func (m *riskMemory) record(root string, passed bool) {
	root = config.CanonicalProjectRoot(root)
	m.mu.Lock()
	defer m.mu.Unlock()
	if passed {
		delete(m.failed, root)
		return
	}
	m.failed[root] = true
}

// owesBlockingRun reports whether root's last known run failed.
func (m *riskMemory) owesBlockingRun(root string) bool {
	root = config.CanonicalProjectRoot(root)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.failed[root]
}

// recordGreen notes the tree a passing run validated, which becomes what the
// next change for root is measured against.
//
// This is what stops small changes accumulating into a large one. Measured
// against HEAD the number only grows until something commits: five separate
// 100-line turns read as a 500-line change by the fifth, and the agent is made
// to wait for work it was released for four turns running. Measured from the
// last tree that passed, each of those turns is 100 lines, which is what they
// each actually are.
//
// It also decouples the measurement from commits, in both directions. A commit
// no longer resets the count to nothing — which would otherwise call a large
// pile of unvalidated work "no change" the moment anything committed, validated
// or not — and a commit in the middle of a change no longer hides it.
//
// The tree passed here is the one that was snapshotted *before* the run, since
// that is the state the run actually validated. Whether the working tree has
// moved on since is a separate question, and the one the next measurement is
// about to answer.
func (m *riskMemory) recordGreen(root string, state treeState) {
	if !state.known() {
		return
	}
	root = config.CanonicalProjectRoot(root)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.green[root] = state
}

// baseline returns the state root's changes should be measured against. An
// unknown state means there is nothing to measure from yet.
func (m *riskMemory) baseline(root string) treeState {
	root = config.CanonicalProjectRoot(root)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.green[root]
}

// assessRisk measures root and decides whether a run against it can be
// released. Everything it needs comes from the project on disk and what the
// daemon remembers, so a caller only has to name the project.
func (d *daemon) assessRisk(root string) riskDecision {
	policy := policyFor(root)
	owes := d.risk.owesBlockingRun(root)

	ch, err := d.measure(root)
	// The evidence lookup needs the size, and the size is what the measurement
	// just produced, so the facts are read first and the judgement made once.
	hist := d.hist.evidence(root, RiskSummary{Lines: ch.Lines, Files: len(ch.Paths)})
	return decideRisk(policy, owes, ch, err, hist)
}

// measure reads how far root has moved from the last state that passed its
// checks, or from HEAD when there is no such state — a daemon that has just
// started, or a project whose runs have never passed. Falling back rather than
// refusing keeps the first run after a restart working; it measures a larger
// change than the incremental one, which errs towards making somebody wait.
func (d *daemon) measure(root string) (changeset.Changes, error) {
	base := d.risk.baseline(root)
	if base.tree != "" {
		ch, err := gitutil.ChangesBetween(root, base.tree)
		if err == nil {
			return ch, nil
		}
		// The snapshot is gone — collected by git's gc, or the repo has moved
		// under it. Measuring against HEAD is worse but still true.
	}
	if base.index != nil {
		if idx, err := filestate.Build(root); err == nil {
			return idx.Changes(base.index), nil
		}
	}

	ch, err := gitutil.WorkingChanges(root)
	if err == nil {
		return ch, nil
	}
	// Git cannot answer for this tree: no repository, or one with no commits
	// yet. Measured by hand instead, against nothing, so that three files in a
	// fresh repo read as three files rather than as an unmeasurable change that
	// blocks every run for as long as the repo stays fresh.
	if idx, buildErr := filestate.Build(root); buildErr == nil {
		return idx.Changes(nil), nil
	}
	return ch, err
}

// fingerprintTree identifies the state of a working tree for the staleness
// check, in a way that does not need git to be able to answer.
//
// It is what lets a repository with no commits be validated in the background
// at all. Staleness is only detectable against a recorded identity, and the git
// fingerprint has none to give for a tree with no HEAD — so the run would be
// refused and held, however small the change. Hashing the tree gives the same
// guarantee by other means: two equal digests are the same content.
//
// Git first where git can answer, because it reads only the files it reports as
// changed rather than walking everything.
func fingerprintTree(dir string) (gitutil.Worktree, error) {
	wt, err := gitutil.Fingerprint(dir)
	if err == nil {
		return wt, nil
	}
	idx, buildErr := filestate.Build(dir)
	if buildErr != nil {
		// Git's error names the condition — no repo, no commits, a dirty
		// submodule — and is the more useful of the two to report.
		return gitutil.Worktree{}, err
	}
	return gitutil.Worktree{Digest: idx.Digest(), Clean: len(idx) == 0}, nil
}

// snapshotState captures what root looks like now, to be recorded as the
// baseline if the run about to happen passes. An unknown state simply means the
// next change is measured against HEAD.
func (d *daemon) snapshotState(root string) treeState {
	if root == "" {
		return treeState{}
	}
	if tree, err := gitutil.SnapshotTree(root); err == nil {
		return treeState{tree: tree}
	}
	if idx, err := filestate.Build(root); err == nil {
		return treeState{index: idx}
	}
	return treeState{}
}
