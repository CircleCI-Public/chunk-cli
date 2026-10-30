package plan

import (
	"slices"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
)

// done is the last step, and the state of a flow that ended merged.
const done = "Done"

// steps are the phases a flow goes through, in order, as the strip draws them.
var steps = []string{"Planning", "Building", "Integrating", done}

// status is the strip's text for flow and its family: the step it is at,
// what is happening now, and what comes next. stage is what ctrl+s approves
// for a plan ReadyToApprove, and inReview whether factory is working on the
// plan. Text from factory is stripped of escapes and kept to one line.
func status(f Flow, fam Family, stage planstate.Stage, inReview bool) (step, now, next string) {
	state := clean(f.State)
	switch phase, _ := planstate.Parse(state); {
	case planstate.IsUnprioritized(state):
		step = "Not started"
	case f.Ended:
		step = done
		if o := clean(f.Outcome); o != "" {
			step += " · " + o
		}
	case slices.Contains(steps, phase):
		step = phase
	default:
		step = state
	}

	switch {
	case f.Task != "":
		now = taskLabel(clean(f.Task))
		if u := clean(f.Unit); u != "" {
			now += " · " + u
		}
	case inReview:
		now = "factory is reviewing the plan"
	}

	// A live flow with nothing in Next waits on its first blocker that is
	// not Done.
	blocker := -1
	if !f.Ended && len(f.Next) == 0 {
		blocker = slices.IndexFunc(fam.Blockers, func(b Relative) bool {
			return !planstate.BlockerDone(b.State, clean(b.Outcome))
		})
	}
	switch {
	case blocker >= 0:
		next = "Waiting on " + clean(fam.Blockers[blocker].Title)
	case ReadyToApprove(f):
		next = stageText(stage)
	default:
		if s, ok := forward(f.Next); ok {
			next = stepText(s)
		}
	}
	return step, now, next
}

// forward is the transition in next that moves the flow on: resolve-conflicts
// when it is offered, and otherwise the first that is not abandon or
// abandon-integrate, the escape hatches factory offers in every live state.
func forward(next []Step) (Step, bool) {
	if i := slices.IndexFunc(next, func(s Step) bool { return clean(s.Event) == resolveConflicts }); i >= 0 {
		return next[i], true
	}
	i := slices.IndexFunc(next, func(s Step) bool {
		e := clean(s.Event)
		return e != "abandon" && e != "abandon-integrate"
	})
	if i < 0 {
		return Step{}, false
	}
	return next[i], true
}

// stageText is what the developer does next for a plan ReadyToApprove.
func stageText(stage planstate.Stage) string {
	switch stage {
	case planstate.StageDesign:
		return "you approve the design · ctrl+s"
	case planstate.StagePlan:
		return "you approve the plan · ctrl+s"
	case planstate.StageQuestions:
		return "you answer 3. Open Questions in comments"
	}
	return ""
}

// stepText is s as the strip's Next names it: what the developer does, or
// who acts and on what.
func stepText(s Step) string {
	what := clean(s.Task)
	if what == "" {
		what = clean(s.Event)
	}
	if what == "" {
		what = clean(s.Action)
	}
	actor := Actor(clean(string(s.Actor)))
	who := string(actor)
	switch actor {
	case ActorHuman:
		switch what {
		case "resolve-conflicts":
			what = "resolve conflicts · ctrl+s"
		case "prioritize":
			what = "start work · ctrl+s"
		case "address-comments":
			what = "send your comments"
		case answerOpenQuestions:
			what = "answer 3. Open Questions in comments"
		case "merge-pr":
			what = "merge the PR"
		}
		return strings.TrimSpace("you " + what)
	case ActorSession:
		who = "chunk"
	case ActorDaemon:
		who = "factory"
	}
	if what == "" {
		return who
	}
	return who + ": " + what
}

// clean is s, text from factory, safe to draw on one line.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, ansi.Strip(s))
	return strings.Join(strings.Fields(s), " ")
}
