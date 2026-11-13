package cmd

import (
	"bytes"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestCommandsListsUIAndNotItsWatchAlias(t *testing.T) {
	root := newTestRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"commands"})

	err := root.Execute()
	assert.NilError(t, err)

	got := out.String()
	assert.Check(t, cmp.Contains(got, "chunk ui "))
	assert.Check(t, !strings.Contains(got, "chunk watch"), "hidden watch alias listed:\n%s", got)
}
