package ui

import (
	"fmt"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

// ImplementerQuiet is how long an implementer may go without doing or saying
// anything before its progress says so.
const ImplementerQuiet = 5 * time.Minute

// TurnFacts says how a round's running implementer turn is going: how long it
// has run, what it last did, how much it has done, and how long it has been
// quiet when that is long enough to be worth saying.
func TurnFacts(r chunkd.Round, now time.Time) []string {
	var p chunkd.ImplementProgress
	if r.Implement != nil {
		p = r.Implement.Progress
	}
	facts := []string{FormatDuration(now.Sub(r.StartedAt).Truncate(time.Second))}
	if p.Doing != "" {
		facts = append(facts, p.Doing)
	}
	switch {
	case p.Actions == 1:
		facts = append(facts, "1 action")
	case p.Actions > 1:
		facts = append(facts, fmt.Sprintf("%d actions", p.Actions))
	}
	last := p.At
	if last.IsZero() {
		last = r.StartedAt
	}
	if quiet := now.Sub(last); quiet >= ImplementerQuiet {
		facts = append(facts, fmt.Sprintf("quiet %dm", int(quiet.Minutes())))
	}
	return facts
}
