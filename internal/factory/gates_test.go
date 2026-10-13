package factory

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/config"
)

// seenReviews is how many reviews the fixture's fake reviewers have run.
func seenReviews(t *testing.T, home string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(home, "seen"))
	if os.IsNotExist(err) {
		return 0
	}
	assert.NilError(t, err)
	return len(entries)
}

func TestCheckRunsGatesBeforeReviews(t *testing.T) {
	tests := []struct {
		name        string
		gatesFirst  bool
		gate        string
		wantReviews int
		wantStatus  map[string]Status
	}{
		{"failing gate skips the reviews", true, "echo lint broke; exit 1", 0,
			map[string]Status{"bugs": StatusSkipped, "style": StatusSkipped, "lint": StatusFailed}},
		{"passing gate runs the reviews", true, "true", 2,
			map[string]Status{"bugs": StatusPassed, "style": StatusPassed, "lint": StatusPassed}},
		{"gates first off restores the parallel run", false, "exit 1", 2,
			map[string]Status{"bugs": StatusPassed, "style": StatusPassed, "lint": StatusFailed}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, impl, _, home := newSidecarsFixture(t, nil)
			s.GatesFirst = tt.gatesFirst
			s.Commands = []config.Command{{Name: "lint", Run: tt.gate}}
			writeFile(t, impl.RepoPath, "main.go", "package main\n\n// v1\n")
			_, err := s.Collect(ctx)
			assert.NilError(t, err)

			checks, err := s.Check(ctx, 1)
			assert.NilError(t, err)
			assert.Equal(t, seenReviews(t, home), tt.wantReviews)
			got := map[string]Status{}
			for _, c := range checks {
				got[c.Name] = c.Status
				if c.Status == StatusSkipped {
					assert.Equal(t, c.Kind, KindReview)
					assert.Equal(t, c.Reason, "gates failed")
				}
			}
			assert.DeepEqual(t, got, tt.wantStatus)
			assert.Equal(t, len(checks), 3, "the checks keep their shape")
		})
	}
}

func TestSkippedReviewsAreNotFedBackOrRemembered(t *testing.T) {
	ctx := context.Background()
	s, impl, _, _ := newSidecarsFixture(t, nil)
	s.GatesFirst = true
	s.Commands = []config.Command{{Name: "lint", Run: "echo boom; exit 1"}}
	writeFile(t, impl.RepoPath, "main.go", "package main\n\n// v1\n")
	_, err := s.Collect(ctx)
	assert.NilError(t, err)
	checks, err := s.Check(ctx, 1)
	assert.NilError(t, err)

	assert.Assert(t, !Passed(checks))
	fb := Feedback(checks)
	assert.Assert(t, contains(fb, "validate: lint"), fb)
	assert.Assert(t, !contains(fb, "review:"), fb)
	assert.Equal(t, len(s.history), 0, "a review that did not run has nothing to remember")
}

func TestErroredGatesDoNotSkipTheReviews(t *testing.T) {
	assert.Assert(t, !gatesFailed([]Check{{Kind: KindValidate, Status: StatusErrored}, {Kind: KindValidate, Status: StatusPassed}}))
	assert.Assert(t, gatesFailed([]Check{{Kind: KindValidate, Status: StatusPassed}, {Kind: KindValidate, Status: StatusFailed}}))
	assert.Assert(t, !gatesFailed([]Check{{Kind: KindReview, Status: StatusFailed}}))
}

func TestFactoryConfigPlanAndGatesFirstDefaultOn(t *testing.T) {
	off := false
	var unset *config.FactoryConfig
	assert.Assert(t, unset.PlanEnabled() && unset.GatesFirstEnabled())
	assert.Assert(t, (&config.FactoryConfig{}).PlanEnabled() && (&config.FactoryConfig{}).GatesFirstEnabled())
	assert.Assert(t, !(&config.FactoryConfig{Plan: &off}).PlanEnabled())
	assert.Assert(t, !(&config.FactoryConfig{GatesFirst: &off}).GatesFirstEnabled())
}
