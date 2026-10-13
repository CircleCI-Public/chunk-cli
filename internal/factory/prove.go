package factory

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/smeltdev/smelt/internal/config"
)

// editTools are the activities that change a file. A command that edits files
// through the shell is not recognised; the gates run by the real check are the
// backstop for it.
var editTools = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}

// claimPattern is how a summary says a check came out well.
var claimPattern = regexp.MustCompile(`(?i)\b(pass(?:es|ed|ing)?|green|clean|succe(?:ed|eds|eded|ssful(?:ly)?))\b`)

// subjectPattern is what a summary says passes: a gate, or what one checks.
var subjectPattern = regexp.MustCompile(`(?i)\b(tests?|lint\w*|gates?|checks?|builds?|vet|fmt|format\w*|suite)\b`)

// gatesHeading starts the section of a summary that reports the gates.
const gatesHeading = "## Gates"

// gateCommands are the commands the implementer is asked to run itself: the
// project's validation commands, without smelt's own markers check.
func gateCommands(cmds []config.Command) []config.Command {
	var out []config.Command
	for _, c := range cmds {
		if c.Name != MarkersGate.Name {
			out = append(out, c)
		}
	}
	return out
}

// claimsGatesPass reports whether a summary says its checks pass, or has the
// Gates section the implementer was asked to write. A line has to name what
// passes as well as say that it does, so "a pass-through wrapper" is no claim.
func claimsGatesPass(summary string) bool {
	if strings.Contains(summary, gatesHeading) {
		return true
	}
	for _, line := range strings.Split(summary, "\n") {
		if claimPattern.MatchString(line) && subjectPattern.MatchString(line) {
			return true
		}
	}
	return false
}

// ranGate reports whether a shell command ran gate: its command line, or the
// task target of its name that wraps it.
func ranGate(command string, gate config.Command) bool {
	if run := strings.TrimSpace(gate.Run); run != "" && strings.Contains(command, run) {
		return true
	}
	target := regexp.MustCompile(`(?:^|[\s;&|(])task\s+(?:-\S+\s+)*` + regexp.QuoteMeta(gate.Name) + `(?::\S*)?(?:$|[\s;&|)])`)
	return target.MatchString(command)
}

// unproven names the gates that did not run after the last edit in a turn's
// activity. A gate run before an edit proves nothing about the edited code.
func unproven(acts []Activity, gates []config.Command) []string {
	last := -1
	for i, a := range acts {
		if editTools[a.Tool] {
			last = i
		}
	}
	var out []string
	for _, g := range gates {
		ran := false
		for _, a := range acts[last+1:] {
			if a.Tool == "Bash" && ranGate(a.Detail, g) {
				ran = true
				break
			}
		}
		if !ran {
			out = append(out, g.Name)
		}
	}
	return out
}

// nudgePrompt asks the implementer to run the gates its summary claimed.
func nudgePrompt(missing []string) string {
	return fmt.Sprintf("Your summary says the gates pass but they weren't run after your last edit (not run: %s); "+
		"run them and report. Paste the last lines of each one's output under a %s heading, "+
		"and fix whatever fails.", strings.Join(missing, ", "), gatesHeading)
}

// mergeTurns is the turn that follows first, with what both spent. The later
// summary stands: it is the implementer's last word on the work.
func mergeTurns(first, next Turn) Turn {
	next.Duration += first.Duration
	next.CostUSD += first.CostUSD
	next.InputTokens += first.InputTokens
	next.CachedTokens += first.CachedTokens
	next.OutputTokens += first.OutputTokens
	next.Activity = append(first.Activity, next.Activity...)
	if next.RateLimit == nil {
		next.RateLimit = first.RateLimit
	}
	return next
}
