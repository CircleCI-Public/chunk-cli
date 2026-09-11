package watchd

import (
	"fmt"
	"strings"
	"time"
)

// ConflictNotice renders the advisory an agent should be told about, or "" when
// there is nothing to advise.
//
// Empty is the common case and the important one: no conflict, no answer yet,
// no daemon, a branch that is itself the merge target — all of them produce no
// text at all. An advisory that speaks when it has nothing to say trains the
// reader to skip it, which costs it the one time it matters.
//
// The wording states plainly that this is not a gate. The agent reading it is
// the same one that treats a failed `chunk validate` as work to do before it
// can continue, and a notice that reads like a check would divert it into a
// rebase in the middle of an unrelated task.
func ConflictNotice(rep ConflictReport) string {
	c := rep.Conflict
	if c == nil || !c.Conflicted {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Merge conflict advisory (informational — this does not block anything):\n")
	fmt.Fprintf(&b, "branch %s does not currently merge cleanly into %s.\n", c.Branch, c.Target)

	if len(c.Paths) > 0 {
		b.WriteString("\nConflicting files:\n")
		for _, p := range c.Paths {
			fmt.Fprintf(&b, "  %s\n", p)
		}
		if c.TotalPaths > len(c.Paths) {
			fmt.Fprintf(&b, "  ... and %d more\n", c.TotalPaths-len(c.Paths))
		}
	}

	// Said every time, not only when it might matter. The check merges HEAD, so
	// a developer with the conflicting fix sitting uncommitted in their tree
	// would otherwise read a contradiction and trust the notice less.
	b.WriteString("\nThis compares committed history only — uncommitted changes in the working " +
		"tree were not considered.\n")

	if c.TargetStale {
		fmt.Fprintf(&b, "%s could not be refreshed, so it may be out of date.\n", c.Target)
	} else if !c.TargetFetchedAt.IsZero() {
		fmt.Fprintf(&b, "%s was last fetched %s ago.\n", c.Target, roundAge(time.Since(c.TargetFetchedAt)))
	}

	b.WriteString("\nDo not change course to fix this unless the user asks. " +
		"Mention it to them, and continue with the current task.\n")
	return b.String()
}

// ConflictStatus renders the full state for someone who ran the command by
// hand, including every reason ConflictNotice stays silent about.
//
// The two differ on purpose. A person typing `chunk conflicts` and getting no
// output cannot tell a clean merge from a daemon that is not running, and both
// answers change what they do next; an agent being handed the same distinction
// mid-task gains nothing from it.
func ConflictStatus(rep ConflictReport) string {
	if !rep.Known {
		// "Tracked" is not the same as "has a sidecar right now": the daemon
		// discovers projects from the breadcrumbs under the app data dir, which
		// `chunk watch` writes and which outlive the sidecar that prompted
		// them. Saying "active sidecar" would send someone looking for a
		// sidecar when what they need is to point the daemon at the project.
		return "No conflict information for this project.\n" +
			"The watch daemon only tracks projects it has been pointed at — run `chunk watch` here to register it.\n"
	}
	c := rep.Conflict
	if c == nil {
		return "No conflict check has completed for this project yet.\n"
	}
	if c.Unavailable != "" {
		return fmt.Sprintf("No merge comparison: %s.\n", c.Unavailable)
	}

	var b strings.Builder
	if c.Conflicted {
		fmt.Fprintf(&b, "%s does not merge cleanly into %s.\n", c.Branch, c.Target)
		if len(c.Paths) > 0 {
			b.WriteString("\nConflicting files:\n")
			for _, p := range c.Paths {
				fmt.Fprintf(&b, "  %s\n", p)
			}
			if c.TotalPaths > len(c.Paths) {
				fmt.Fprintf(&b, "  ... and %d more\n", c.TotalPaths-len(c.Paths))
			}
		}
	} else {
		fmt.Fprintf(&b, "%s merges cleanly into %s.\n", c.Branch, c.Target)
	}

	b.WriteString("\nCommitted history only; uncommitted changes were not considered.\n")
	if c.TargetStale {
		fmt.Fprintf(&b, "%s could not be refreshed, so this may be out of date.\n", c.Target)
	} else if !c.TargetFetchedAt.IsZero() {
		fmt.Fprintf(&b, "%s last fetched %s ago", c.Target, roundAge(time.Since(c.TargetFetchedAt)))
		if !c.CheckedAt.IsZero() {
			fmt.Fprintf(&b, "; compared %s ago", roundAge(time.Since(c.CheckedAt)))
		}
		b.WriteString(".\n")
	}
	return b.String()
}

// roundAge renders a duration at the coarsest unit that still says something.
// A notice reporting "3m14.882s" invites the reader to weigh a precision the
// underlying poll interval does not have.
func roundAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "less than a minute"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
