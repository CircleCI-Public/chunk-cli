package watchd

import (
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestClaimStoreRegisterAndRelease(t *testing.T) {
	s := newClaimStore()
	s.register("sess-a", "/repo", []string{"foo.go"})
	claims := s.forProject("/repo")
	assert.Assert(t, len(claims) == 1, "expected one claim, got %d", len(claims))
	assert.Equal(t, claims[0].SessionID, "sess-a")

	s.release("sess-a", "/repo")
	claims = s.forProject("/repo")
	assert.Assert(t, len(claims) == 0, "expected no claims after release, got %d", len(claims))
}

func TestClaimStoreNoOpOnEmptySession(t *testing.T) {
	s := newClaimStore()
	s.register("", "/repo", []string{"foo.go"})
	assert.Assert(t, len(s.forProject("/repo")) == 0)
	s.release("", "/repo") // must not panic
}

func TestClaimStoreOverlapping(t *testing.T) {
	s := newClaimStore()
	s.register("sess-a", "/repo", []string{"internal/foo.go"})

	// Same file — should overlap.
	overlaps := s.overlapping("sess-b", "/repo", []string{"internal/foo.go"})
	assert.Assert(t, len(overlaps) == 1, "expected overlap, got %d", len(overlaps))

	// Different file — should not overlap.
	overlaps = s.overlapping("sess-b", "/repo", []string{"internal/bar.go"})
	assert.Assert(t, len(overlaps) == 0, "expected no overlap for different files, got %d", len(overlaps))
}

func TestClaimStoreNilPathsAreConservative(t *testing.T) {
	s := newClaimStore()
	// Register with nil paths (unknown files).
	s.register("sess-a", "/repo", nil)

	// Any query against a nil-paths claim should overlap.
	overlaps := s.overlapping("sess-b", "/repo", []string{"internal/foo.go"})
	assert.Assert(t, len(overlaps) == 1, "nil-path claim should overlap any query")

	// Querying with nil paths against a specific claim should also overlap.
	s.register("sess-c", "/repo", []string{"internal/bar.go"})
	overlaps = s.overlapping("sess-b", "/repo", nil)
	assert.Assert(t, len(overlaps) == 2, "nil-path query should overlap all claims in project")
}

func TestClaimStoreDoesNotReturnOwnSession(t *testing.T) {
	s := newClaimStore()
	s.register("sess-a", "/repo", []string{"foo.go"})
	overlaps := s.overlapping("sess-a", "/repo", []string{"foo.go"})
	assert.Assert(t, len(overlaps) == 0, "own session should never appear in overlaps")
}

func TestClaimStoreExpiry(t *testing.T) {
	s := newClaimStore()
	s.ttl = 50 * time.Millisecond
	s.register("sess-a", "/repo", []string{"foo.go"})

	// Still live.
	assert.Assert(t, len(s.forProject("/repo")) == 1)

	// Advance time past TTL.
	base := s.now
	s.now = func() time.Time { return base().Add(100 * time.Millisecond) }
	s.expireAll()

	assert.Assert(t, len(s.forProject("/repo")) == 0, "claim should be expired")
}

func TestClaimNoticeSilentWhenNoOverlap(t *testing.T) {
	assert.Check(t, cmp.Equal(ClaimNotice(nil), ""))
	assert.Check(t, cmp.Equal(ClaimNotice([]ClaimState{}), ""))
}

func TestClaimNoticeNamesSessionAndFiles(t *testing.T) {
	now := time.Now()
	overlaps := []ClaimState{
		{
			SessionID:   "abcdef1234567890",
			ProjectRoot: "/repo",
			Paths:       []string{"internal/foo.go", "internal/bar.go"},
			ClaimedAt:   now.Add(-2 * time.Minute),
		},
	}
	notice := ClaimNotice(overlaps)
	assert.Assert(t, notice != "", "expected non-empty notice")
	assert.Assert(t, cmp.Contains(notice, "abcdef12"), "notice should include truncated session ID")
	assert.Assert(t, cmp.Contains(notice, "internal/foo.go"), "notice should include file path")
	assert.Assert(t, cmp.Contains(notice, "Do not change course"), "notice should end with standard advisory")
}
