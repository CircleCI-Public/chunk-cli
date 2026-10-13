package factory

import (
	"regexp"
	"strings"
)

// skipLine matches the line `go test -v` ("--- SKIP: TestX (0.00s)") and
// gotestsum's summary ("=== SKIP: pkg TestX (0.00s)") print for a skipped test.
var skipLine = regexp.MustCompile(`^\s*(?:---|===) SKIP: (.+?)(?: \(\d+(?:\.\d+)?s\))?\s*$`)

// SkippedTests lists the tests a test runner's output reports as skipped, once
// each, in the order they appear.
func SkippedTests(output string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		m := skipLine.FindStringSubmatch(line)
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		out = append(out, m[1])
	}
	return out
}

// Skipped is the tests the last round's validation commands skipped.
func (o Outcome) Skipped() []string {
	var out []string
	for _, c := range o.Checks {
		out = append(out, c.Skipped...)
	}
	return out
}
