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

// WorkerLine is the one line that says what w, the worker claiming a plan, is
// doing: "Worker <hostname> · <state>", or that it can't be read. It is "" for
// no worker.
func WorkerLine(w *Worker, holder string) string {
	switch {
	case w == nil:
		return ""
	case w.Unread != "":
		return "Worker " + clean(w.ID) + " can't be read"
	}
	return "Worker " + clean(w.Hostname) + " · " + workerState(w, holder)
}

// workerState is what a worker that could be read is doing for the plan: that
// it is not live, that a session holds the plan (holder, "session <id>") when
// it has no headless item, that it has no headless runner for it, or each
// runner's role and state, with why one is not healthy.
func workerState(w *Worker, holder string) string {
	switch {
	case !w.Live:
		return "not live"
	case len(w.Headless) == 0 && clean(holder) != "":
		return "session " + shortID(clean(holder))
	case len(w.Headless) == 0:
		return "no headless runner"
	}
	items := make([]string, len(w.Headless))
	for i, h := range w.Headless {
		items[i] = clean(h.Role) + " " + clean(h.State)
		if reason := clean(h.HealthReason); reason != "" && clean(h.Health) != "ok" {
			items[i] += " (" + reason + ")"
		}
	}
	return strings.Join(items, ", ")
}

// workerActivity is the lines that say what the worker claiming the plan is
// doing, in place of a planner's activity: the worker and its state, then its
// problem with the plan and the fix for it, each on a line of its own when
// there is one. A worker that can't be read says so and why.
func workerActivity(w *Worker, holder string) []string {
	switch {
	case w == nil:
		return nil
	case w.Unread != "":
		return []string{"Worker " + clean(w.ID) + " can't be read: " + clean(w.Unread)}
	}
	lines := []string{clean(w.Hostname) + " · " + workerState(w, holder)}
	if p := clean(w.Problem); p != "" {
		lines = append(lines, p)
	}
	if f := clean(w.Fix); f != "" {
		lines = append(lines, f)
	}
	return lines
}

// shortID is a session ID cut to the length session IDs are usually shown at.
func shortID(id string) string {
	if r := []rune(id); len(r) > 8 {
		return string(r[:8])
	}
	return id
}
