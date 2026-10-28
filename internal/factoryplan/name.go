package factoryplan

import (
	"cmp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxTitleLen keeps a plan's title short enough to read in the plan list.
const maxTitleLen = 40

// maxNameWords caps the words kept from Claude's name for a plan.
const maxNameWords = 3

// slug is text as a slug: lowercase letters and digits, words joined by
// hyphens, stopping at the last whole word within maxTitleLen runes, and
// after maxWords words when maxWords > 0. A first word longer than
// maxTitleLen is cut. It is "" when text has no letters or digits.
func slug(text string, maxWords int) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) == 0 {
		return ""
	}
	if maxWords > 0 && len(words) > maxWords {
		words = words[:maxWords]
	}
	first := []rune(words[0])
	s := string(first[:min(len(first), maxTitleLen)])
	for _, w := range words[1:] {
		if utf8.RuneCountInString(s)+1+utf8.RuneCountInString(w) > maxTitleLen {
			break
		}
		s += "-" + w
	}
	return s
}

// fallbackTitle is the plan's title when Claude gives no name: a slug of
// message's first line, or "plan" when that has no words.
func fallbackTitle(message string) string {
	line, _, _ := strings.Cut(message, "\n")
	return cmp.Or(slug(line, 0), "plan")
}
