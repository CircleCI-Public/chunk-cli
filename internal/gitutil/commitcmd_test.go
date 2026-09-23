package gitutil

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestIsCommitCommand(t *testing.T) {
	t.Parallel()
	cases := []struct {
		line string
		want bool
	}{
		{"git commit", true},
		{"git commit -m 'fix the thing'", true},
		{"git commit --amend --no-edit", true},
		{"cd sub && git commit -m x", true},
		{"git add . && git commit -m x", true},
		{"go test ./... ; git commit -am x", true},
		{"git -C ../other commit -m x", true},
		{"git -c user.name=x commit -m x", true},
		{"git --no-pager commit", true},
		{"GIT_AUTHOR_NAME=x git commit", true},
		{"env FOO=1 git commit", true},
		{"/usr/bin/git commit", true},
		{"(cd sub && git commit -m x)", true},

		{"", false},
		{"ls -la", false},
		{"git status", false},
		{"git log --grep commit", false},
		{"git commit-tree HEAD^{tree}", false},
		{"echo git commit", false},
		{"go test ./...", false},
	}
	for _, tc := range cases {
		assert.Equal(t, IsCommitCommand(tc.line), tc.want, "IsCommitCommand(%q)", tc.line)
	}
}
