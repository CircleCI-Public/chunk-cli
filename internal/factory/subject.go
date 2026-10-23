package factory

import (
	"regexp"
	"strings"
)

// A run's prompt is either a request in the developer's words or a plan made
// in chunk factory plan, in factory's format: a "# name" line, then
// "## 1. Outcome" and the other numbered sections. A plan's first line is a
// slug and its first section heading says nothing about the change, so a run
// made from a plan is named for its Outcome: what is true once it is done.

var outcomeHeadingRe = regexp.MustCompile(`(?i)^##\s+1\.\s*outcome$`)

// subject is what a run is called in its pull request's title: a plan's
// Outcome, or else the prompt's first line with any heading marks taken off.
// It is "" for a prompt with no words in it.
func subject(prompt string) string {
	if s := planOutcome(prompt); s != "" {
		return s
	}
	for _, line := range strings.Split(prompt, "\n") {
		if s := oneLine(strings.TrimLeft(strings.TrimSpace(line), "# ")); s != "" {
			return s
		}
	}
	return ""
}

// commitParts is the subject and body of the commit for prompt. A plan's
// subject is its Outcome, cut to a line, with the whole plan below it; a
// request's is its first line, with the rest below.
func commitParts(prompt string) (subj, body string) {
	trimmed := strings.TrimSpace(prompt)
	if s := planOutcome(trimmed); s != "" {
		return cutWords(s, maxPRTitleRunes), trimmed
	}
	first, rest, _ := strings.Cut(trimmed, "\n")
	return oneLine(strings.TrimLeft(first, "# ")), strings.Trim(rest, "\r\n")
}

// planOutcome is the first paragraph under a plan's "## 1. Outcome" heading,
// on one line, without code marks or a closing full stop. It is "" when the
// prompt has no such heading, or nothing under it.
func planOutcome(prompt string) string {
	lines := strings.Split(prompt, "\n")
	for i, line := range lines {
		if !outcomeHeadingRe.MatchString(strings.TrimSpace(line)) {
			continue
		}
		var para []string
		for _, l := range lines[i+1:] {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "#") || (l == "" && len(para) > 0) {
				break
			}
			if l != "" {
				para = append(para, l)
			}
		}
		return strings.TrimRight(oneLine(strings.ReplaceAll(strings.Join(para, " "), "`", "")), ".")
	}
	return ""
}

// planTitle is a plan's "# name" line, without its mark, and whether the
// prompt is a plan that starts with one.
func planTitle(prompt string) (string, bool) {
	if planOutcome(prompt) == "" {
		return "", false
	}
	first, _, _ := strings.Cut(strings.TrimSpace(prompt), "\n")
	name, ok := strings.CutPrefix(strings.TrimSpace(first), "# ")
	return oneLine(name), ok && oneLine(name) != ""
}
