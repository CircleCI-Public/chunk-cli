package watchd

import (
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestConflictNoticeStaysSilentWithNothingToAdvise(t *testing.T) {
	// Every one of these is a case where an advisory has nothing to add, and
	// text in any of them trains the reader to skip the one that matters.
	cases := map[string]ConflictReport{
		"no daemon answer":  {Root: "/x"},
		"no check yet":      {Root: "/x", Known: true},
		"merges cleanly":    {Root: "/x", Known: true, Conflict: &ConflictState{Branch: "f", Target: "origin/main"}},
		"on default branch": {Root: "/x", Known: true, Conflict: &ConflictState{Branch: "main", Unavailable: "this branch is the merge target"}},
		"detached head":     {Root: "/x", Known: true, Conflict: &ConflictState{Unavailable: "HEAD is detached"}},
	}
	for name, rep := range cases {
		notice := ConflictNotice(rep)
		assert.Check(t, cmp.Equal(notice, ""), "case %q must produce no notice", name)
	}
}

func TestConflictNoticeNamesTheBranchTargetAndPaths(t *testing.T) {
	rep := ConflictReport{Root: "/x", Known: true, Conflict: &ConflictState{
		Branch:          "jesse/feature",
		Target:          "origin/main",
		Conflicted:      true,
		Paths:           []string{"internal/a.go", "internal/b.go"},
		TotalPaths:      2,
		TargetFetchedAt: time.Now().Add(-2 * time.Minute),
	}}
	notice := ConflictNotice(rep)
	assert.Check(t, cmp.Contains(notice, "jesse/feature"))
	assert.Check(t, cmp.Contains(notice, "origin/main"))
	assert.Check(t, cmp.Contains(notice, "internal/a.go"))
	assert.Check(t, cmp.Contains(notice, "internal/b.go"))
	// The caveat is unconditional: a developer holding the fix uncommitted
	// would otherwise read a contradiction and trust the notice less.
	assert.Check(t, cmp.Contains(notice, "committed history only"))
	// And it has to disclaim being a gate, or the agent reads it as work to do
	// before it may continue.
	assert.Check(t, cmp.Contains(notice, "does not block"))
	assert.Check(t, cmp.Contains(notice, "2m ago"))
}

func TestConflictNoticeReportsTruncationAndStaleness(t *testing.T) {
	rep := ConflictReport{Root: "/x", Known: true, Conflict: &ConflictState{
		Branch:      "f",
		Target:      "origin/main",
		Conflicted:  true,
		Paths:       []string{"a.go"},
		TotalPaths:  7,
		TargetStale: true,
	}}
	notice := ConflictNotice(rep)
	assert.Check(t, cmp.Contains(notice, "and 6 more"))
	assert.Check(t, cmp.Contains(notice, "could not be refreshed"))
}

func TestConflictStatusDistinguishesEveryQuietCase(t *testing.T) {
	// The counterpart to the silence above. A person who typed the command and
	// got nothing back cannot tell a clean merge from an absent daemon, and the
	// two change what they do next.
	unknown := ConflictStatus(ConflictReport{Root: "/x"})
	assert.Check(t, cmp.Contains(unknown, "chunk watch"))

	pending := ConflictStatus(ConflictReport{Root: "/x", Known: true})
	assert.Check(t, cmp.Contains(pending, "No conflict check has completed"))

	unavailable := ConflictStatus(ConflictReport{Root: "/x", Known: true, Conflict: &ConflictState{
		Unavailable: "this branch is the merge target",
	}})
	assert.Check(t, cmp.Contains(unavailable, "this branch is the merge target"))

	clean := ConflictStatus(ConflictReport{Root: "/x", Known: true, Conflict: &ConflictState{
		Branch: "f", Target: "origin/main",
	}})
	assert.Check(t, cmp.Contains(clean, "merges cleanly"))

	conflicted := ConflictStatus(ConflictReport{Root: "/x", Known: true, Conflict: &ConflictState{
		Branch: "f", Target: "origin/main", Conflicted: true, Paths: []string{"a.go"}, TotalPaths: 1,
	}})
	assert.Check(t, cmp.Contains(conflicted, "does not merge cleanly"))
	assert.Check(t, cmp.Contains(conflicted, "a.go"))
}

func TestRoundAgeUsesCoarseUnits(t *testing.T) {
	assert.Check(t, cmp.Equal(roundAge(20*time.Second), "less than a minute"))
	assert.Check(t, cmp.Equal(roundAge(90*time.Second), "1m"))
	assert.Check(t, cmp.Equal(roundAge(45*time.Minute), "45m"))
	assert.Check(t, cmp.Equal(roundAge(3*time.Hour+7*time.Minute), "3h7m"))
}
