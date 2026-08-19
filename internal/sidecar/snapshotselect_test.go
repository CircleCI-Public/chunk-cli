package sidecar

import (
	"context"
	"net/http/httptest"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func TestSelectSnapshot(t *testing.T) {
	tests := []struct {
		name      string
		snapshots []circleci.Snapshot
		criteria  SnapshotCriteria
		wantID    string
	}{
		{
			name:      "no snapshots",
			snapshots: nil,
			criteria:  SnapshotCriteria{Repo: "chunk-cli", Stack: "go"},
			wantID:    "",
		},
		{
			name: "nothing relates to the repo",
			snapshots: []circleci.Snapshot{
				{ID: "s1", Name: "billing-service"},
				{ID: "s2", Name: "frontend-node"},
			},
			criteria: SnapshotCriteria{Repo: "chunk-cli", Stack: "go"},
			wantID:   "",
		},
		{
			name: "exact repo name wins over stack match",
			snapshots: []circleci.Snapshot{
				{ID: "s1", Name: "go-base"},
				{ID: "s2", Name: "chunk-cli"},
			},
			criteria: SnapshotCriteria{Repo: "chunk-cli", Stack: "go"},
			wantID:   "s2",
		},
		{
			name: "repo tokens match a decorated name",
			snapshots: []circleci.Snapshot{
				{ID: "s1", Name: "go-base"},
				{ID: "s2", Name: "chunk-cli-uv-lint"},
			},
			criteria: SnapshotCriteria{Repo: "chunk-cli", Stack: "go"},
			wantID:   "s2",
		},
		{
			name: "exact repo name beats a decorated one",
			snapshots: []circleci.Snapshot{
				{ID: "s1", Name: "chunk-cli-uv-lint"},
				{ID: "s2", Name: "chunk-cli"},
			},
			criteria: SnapshotCriteria{Repo: "chunk-cli", Stack: "go"},
			wantID:   "s2",
		},
		{
			name: "stack alias matches when the repo does not",
			snapshots: []circleci.Snapshot{
				{ID: "s1", Name: "python-base"},
				{ID: "s2", Name: "golang-base"},
			},
			criteria: SnapshotCriteria{Repo: "chunk-cli", Stack: "go"},
			wantID:   "s2",
		},
		{
			name: "node snapshot matches a typescript repo",
			snapshots: []circleci.Snapshot{
				{ID: "s1", Name: "node-20"},
			},
			criteria: SnapshotCriteria{Repo: "webapp", Stack: "typescript"},
			wantID:   "s1",
		},
		{
			name: "stack is matched on tokens, not substrings",
			snapshots: []circleci.Snapshot{
				{ID: "s1", Name: "mongo-api"},
				{ID: "s2", Name: "django-worker"},
			},
			criteria: SnapshotCriteria{Repo: "chunk-cli", Stack: "go"},
			wantID:   "",
		},
		{
			name: "the org's own snapshot beats an equivalent system one",
			snapshots: []circleci.Snapshot{
				{ID: "s1", Name: "go", IsSystem: true},
				{ID: "s2", Name: "go", IsSystem: false},
			},
			criteria: SnapshotCriteria{Repo: "chunk-cli", Stack: "go"},
			wantID:   "s2",
		},
		{
			name: "an owned but unrelated snapshot is still not selected",
			snapshots: []circleci.Snapshot{
				{ID: "s1", Name: "billing-service", IsSystem: false},
			},
			criteria: SnapshotCriteria{Repo: "chunk-cli", Stack: "go"},
			wantID:   "",
		},
		{
			name: "the tag is matched as well as the name",
			snapshots: []circleci.Snapshot{
				{ID: "s1", Name: "team-base", Tag: "go"},
			},
			criteria: SnapshotCriteria{Repo: "chunk-cli", Stack: "go"},
			wantID:   "s1",
		},
		{
			name: "unknown stack contributes nothing",
			snapshots: []circleci.Snapshot{
				{ID: "s1", Name: "unknown-base"},
			},
			criteria: SnapshotCriteria{Repo: "chunk-cli", Stack: "unknown"},
			wantID:   "",
		},
		{
			name: "names are compared case- and separator-insensitively",
			snapshots: []circleci.Snapshot{
				{ID: "s1", Name: "Chunk CLI"},
			},
			criteria: SnapshotCriteria{Repo: "chunk-cli"},
			wantID:   "s1",
		},
		{
			name: "ties resolve to the lowest ID regardless of order",
			snapshots: []circleci.Snapshot{
				{ID: "s2", Name: "go-base"},
				{ID: "s1", Name: "go-base"},
			},
			criteria: SnapshotCriteria{Repo: "chunk-cli", Stack: "go"},
			wantID:   "s1",
		},
		{
			name: "empty criteria match nothing",
			snapshots: []circleci.Snapshot{
				{ID: "s1", Name: "go-base"},
			},
			criteria: SnapshotCriteria{},
			wantID:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match, ok := SelectSnapshot(tt.snapshots, tt.criteria)
			if tt.wantID == "" {
				assert.Assert(t, !ok, "expected no match, got %q", match.Snapshot.ID)
				return
			}
			assert.Assert(t, ok, "expected a match")
			assert.Equal(t, match.Snapshot.ID, tt.wantID)
			assert.Assert(t, match.Reason != "", "a match must explain itself")
		})
	}
}

// A repo whose name is a superset of a snapshot's must not match it: "chunk"
// and "chunk-cli" are different projects.
func TestSelectSnapshotRequiresEveryRepoToken(t *testing.T) {
	snapshots := []circleci.Snapshot{{ID: "s1", Name: "chunk"}}
	_, ok := SelectSnapshot(snapshots, SnapshotCriteria{Repo: "chunk-cli"})
	assert.Assert(t, !ok)
}

func TestSelectSnapshotReasonNamesTheSignal(t *testing.T) {
	match, ok := SelectSnapshot(
		[]circleci.Snapshot{{ID: "s1", Name: "go-base"}},
		SnapshotCriteria{Repo: "chunk-cli", Stack: "go"},
	)
	assert.Assert(t, ok)
	assert.Equal(t, match.Reason, "built for go")

	match, ok = SelectSnapshot(
		[]circleci.Snapshot{{ID: "s1", Name: "chunk-cli"}},
		SnapshotCriteria{Repo: "chunk-cli", Stack: "go"},
	)
	assert.Assert(t, ok)
	assert.Equal(t, match.Reason, "named for repo chunk-cli")
}

func TestResolveSnapshot(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Snapshots = []fakes.Snapshot{
		{ID: "snap-1", OrgID: "org-abc", Name: "billing-service"},
		{ID: "snap-2", OrgID: "org-abc", Name: "chunk-cli"},
		// Belongs to a different org, so it must never be considered even
		// though it is the strongest possible name match.
		{ID: "snap-3", OrgID: "org-other", Name: "chunk-cli"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	cl, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	match, ok, err := ResolveSnapshot(context.Background(), cl, "org-abc",
		SnapshotCriteria{Repo: "chunk-cli", Stack: "go"})
	assert.NilError(t, err)
	assert.Assert(t, ok)
	assert.Equal(t, match.Snapshot.ID, "snap-2")
}

func TestResolveSnapshotNoMatch(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Snapshots = []fakes.Snapshot{
		{ID: "snap-1", OrgID: "org-abc", Name: "billing-service"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	cl, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	_, ok, err := ResolveSnapshot(context.Background(), cl, "org-abc",
		SnapshotCriteria{Repo: "chunk-cli", Stack: "go"})
	assert.NilError(t, err)
	assert.Assert(t, !ok)
}

func TestResolveSnapshotAPIError(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ListSnapshotsStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	cl, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	_, ok, err := ResolveSnapshot(context.Background(), cl, "org-abc",
		SnapshotCriteria{Repo: "chunk-cli", Stack: "go"})
	assert.Assert(t, err != nil)
	assert.Assert(t, !ok)
}

// IsSystem must survive the round trip through the list API, since selection
// uses it to prefer an org's own snapshot.
func TestResolveSnapshotPrefersOwnedOverSystem(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Snapshots = []fakes.Snapshot{
		{ID: "snap-1", OrgID: "org-abc", Name: "go-base", IsSystem: true},
		{ID: "snap-2", OrgID: "org-abc", Name: "go-base"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	cl, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	match, ok, err := ResolveSnapshot(context.Background(), cl, "org-abc",
		SnapshotCriteria{Repo: "webapp", Stack: "go"})
	assert.NilError(t, err)
	assert.Assert(t, ok)
	assert.Equal(t, match.Snapshot.ID, "snap-2")
}
