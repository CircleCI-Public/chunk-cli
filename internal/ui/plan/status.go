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

// status is the strip's text for flow and its family: the stage it is at,
// what is happening now, and what comes next, by who factory waits on.
// inReview is whether factory is working on the plan. Text from factory is
// stripped of escapes and kept to one line.
func status(f Flow, fam Family, inReview bool) (step, now, next string) {
	step = planstate.Stage(clean(f.State))
	if step == "" {
		step = clean(f.State)
	}
	if f.Ended {
		step = done
		if o := clean(f.Outcome); o != "" {
			step += " · " + o
		}
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

	return step, now, nextText(f, fam)
}

// nextText is the strip's Next for f: what the developer does, or who
// factory waits on, in factory's words when it gives them. A flow that
// waits on nobody has none.
func nextText(f Flow, fam Family) string {
	why := clean(f.WaitingOnWhy)
	orWhy := func(fallback string) string {
		if why != "" {
			return why
		}
		return fallback
	}
	switch clean(f.WaitingOn) {
	case planstate.Human:
		if ReadyToApprove(f) {
			return stageText(approval(f))
		}
		if i := slices.IndexFunc(f.Next, func(s Step) bool {
			return Actor(clean(string(s.Actor))) == ActorHuman && s.Required
		}); i >= 0 {
			return stepText(f.Next[i])
		}
		return orWhy("you")
	case planstate.Blockers:
		if i := slices.IndexFunc(fam.Blockers, func(b Relative) bool { return b.Active }); i >= 0 {
			return "Waiting on " + clean(fam.Blockers[i].Title)
		}
		return orWhy("waiting on blockers")
	case planstate.Session:
		return orWhy("waiting on chunk")
	case planstate.Daemon:
		return orWhy("waiting on factory")
	case planstate.SubPlans:
		return orWhy("waiting on sub-plans")
	}
	return ""
}

// stageText is what the developer does next for a plan ReadyToApprove
// whose approval is event.
func stageText(event string) string {
	switch event {
	case planstate.ApproveForUnits:
		return "you approve the design · ctrl+s"
	case planstate.ApproveForBuilding:
		return "you approve the plan · ctrl+s"
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
