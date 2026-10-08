package chunkd

import (
	"fmt"
	"strings"
)

// Risk score bands. They exist so a caller can act on the score without
// hard-coding a number, and so the number itself stays arguable: a band is a
// claim about caution, not a probability of failure.
const (
	BandLow    = "low"
	BandMedium = "medium"
	BandHigh   = "high"
)

// RiskSummary is what the daemon made of a change, as reported to a caller.
//
// It is deliberately not just a number. A score on its own cannot be argued
// with — "risk 62" invites either trust or dismissal and supports neither — so
// the facts it was built from travel with it, and the advice that follows from
// them is spelled out rather than left to be inferred.
type RiskSummary struct {
	// Score is 0–100. Higher means more caution: bigger, broader, or already
	// failing. It does not decide anything on its own; see decideRisk.
	Score int `json:"score"`
	// Lines and Files are what was measured, kept apart from the score so a
	// caller can compare two changes without unpicking one.
	Lines int `json:"lines"`
	Files int `json:"files"`
	// Inert reports that every changed path was docs or text.
	Inert bool `json:"inert,omitempty"`
	// Band is Score bucketed into BandLow, BandMedium or BandHigh.
	Band string `json:"band"`
	// Parts are the facts behind the score, each with what it contributed.
	Parts []string `json:"parts,omitempty"`
	// Advice is what a developer or agent could do about it, or "" when there is
	// nothing useful to say.
	Advice string `json:"advice,omitempty"`
}

// String renders a summary as one line of terminal output.
func (r RiskSummary) String() string {
	if len(r.Parts) == 0 {
		return fmt.Sprintf("risk %d/100 %s", r.Score, r.Band)
	}
	return fmt.Sprintf("risk %d/100 %s — %s", r.Score, r.Band, strings.Join(r.Parts, ", "))
}
