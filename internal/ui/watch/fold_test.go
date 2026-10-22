package watch

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestFoldHeader(t *testing.T) {
	for _, tc := range []struct {
		open           bool
		label, summary string
		want           string
	}{
		{open: false, label: "details", summary: "", want: "▸ details"},
		{open: false, label: "details", summary: "worktree, log", want: "▸ details · worktree, log"},
		{open: true, label: "details", summary: "", want: "▾ details"},
		// An open fold's summary is dropped: it names what is hidden, and
		// nothing is hidden once it is open.
		{open: true, label: "details", summary: "worktree, log", want: "▾ details"},
	} {
		got := foldHeader(tc.open, tc.label, tc.summary)
		assert.Equal(t, got, tc.want, "open=%v label=%q summary=%q", tc.open, tc.label, tc.summary)
	}
}

func TestFoldHint(t *testing.T) {
	assert.Equal(t, foldHint(false), "expand")
	assert.Equal(t, foldHint(true), "collapse")
}
