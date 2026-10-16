package ui

import (
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

// A turn that has gone quiet for long says so.
func TestTurnFactsSayWhenTheImplementerHasGoneQuiet(t *testing.T) {
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	turn := func(p chunkd.ImplementProgress) chunkd.Round {
		return chunkd.Round{StartedAt: start, Implement: &chunkd.RoundImplement{State: chunkd.ImplementRunning, Progress: p}}
	}
	r := turn(chunkd.ImplementProgress{Doing: "Run the tests", Actions: 2, At: start.Add(time.Minute)})

	assert.DeepEqual(t, TurnFacts(r, start.Add(3*time.Minute)), []string{"3m0s", "Run the tests", "2 actions"})
	assert.DeepEqual(t, TurnFacts(r, start.Add(7*time.Minute)), []string{"7m0s", "Run the tests", "2 actions", "quiet 6m"})

	// Before it has done anything, the quiet counts from the turn's start.
	assert.DeepEqual(t, TurnFacts(turn(chunkd.ImplementProgress{}), start.Add(5*time.Minute)), []string{"5m0s", "quiet 5m"})
}
