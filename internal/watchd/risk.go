package watchd

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
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
}

// decideRisk judges whether a validate run against a measured tree can be
// released to the background.
//
// The judgement is about consequence, not correctness: every change is validated
// either way, and the only question is who waits for the answer. So it is
// deliberately lopsided. Anything it cannot measure, and anything that has
// recently failed, is made to block — the direction that costs a developer time
// rather than the direction that costs them a missed failure.
func decideRisk(p asyncPolicy, owesBlockingRun bool, ch gitutil.Changes, chErr error) riskDecision {
	since := ""
	if ch.Baseline != "" && ch.Baseline != gitutil.BaselineHead {
		since = " since the last passing run"
	}
	limit := p.maxLines
	if limit <= 0 {
		limit = DefaultAsyncMaxLines
	}

	switch p.mode {
	case config.AsyncValidateNever:
		return riskDecision{reason: "background validation is off for this project"}
	case config.AsyncValidateAlways:
		// Ahead of the failure debt below on purpose: a project that has asked for
		// every run to be backgrounded has opted out of the blocking safety net,
		// and quietly overriding that would make the setting a suggestion.
		return riskDecision{async: true, reason: "background validation is always on for this project"}
	}

	// A failure the developer has not been shown the resolution of yet. The next
	// run blocks so it lands where a hook can act on it, and the debt clears as
	// soon as a run passes.
	if owesBlockingRun {
		return riskDecision{reason: "the last run failed, so this one blocks"}
	}

	if chErr != nil {
		// The tree could not be measured, so nothing is known about how large this
		// change is. Guessing small is the one answer that could lose a failure.
		return riskDecision{reason: fmt.Sprintf("change size unavailable: %v", chErr)}
	}
	if ch.Empty() {
		// Nothing to validate, so nothing to wait for: the run skips immediately,
		// and backgrounding it would spend a task and a later report on a run that
		// does no work. No reason either — nothing was held back that a developer
		// would want explained, and a line on every clean turn is just noise.
		return riskDecision{}
	}
	if allInert(ch.Paths) {
		return riskDecision{async: true, reason: "only docs and text changed"}
	}
	if ch.Lines < limit {
		return riskDecision{async: true, reason: fmt.Sprintf("small change%s, %s", since, lineCount(ch.Lines))}
	}
	return riskDecision{reason: fmt.Sprintf("large change%s, %s, over the %d-line limit", since, lineCount(ch.Lines), limit)}
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
	// green is the tree each project last passed its checks against, and what
	// the next change is measured from. See recordGreen.
	green map[string]string
}

func newRiskMemory() *riskMemory {
	return &riskMemory{
		failed: make(map[string]bool),
		green:  make(map[string]string),
	}
}

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
func (m *riskMemory) recordGreen(root, tree string) {
	if tree == "" {
		return
	}
	root = config.CanonicalProjectRoot(root)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.green[root] = tree
}

// baseline returns the tree root's changes should be measured against, or "" to
// measure against HEAD.
func (m *riskMemory) baseline(root string) string {
	root = config.CanonicalProjectRoot(root)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.green[root]
}

// assessRisk measures root and decides whether a run against it can be
// released. Everything it needs comes from the project on disk and what the
// daemon remembers, so a caller only has to name the project.
//
// The measurement is taken from the last tree that passed its checks when there
// is one, and from HEAD when there is not — a daemon that has just started, or a
// project whose runs have never passed. Falling back rather than refusing keeps
// the first run after a restart working; it measures a larger change than the
// incremental one, which errs towards making somebody wait.
func (d *daemon) assessRisk(root string) riskDecision {
	policy := policyFor(root)
	owes := d.risk.owesBlockingRun(root)

	if base := d.risk.baseline(root); base != "" {
		ch, err := gitutil.ChangesBetween(root, base)
		if err == nil {
			return decideRisk(policy, owes, ch, nil)
		}
		// The snapshot is gone — collected by git's gc, or the repo has moved
		// under it. Measuring against HEAD is worse but still true.
	}
	ch, err := gitutil.WorkingChanges(root)
	return decideRisk(policy, owes, ch, err)
}
