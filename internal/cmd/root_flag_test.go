package cmd

import (
	"bytes"
	"errors"
	"testing"

	"gotest.tools/v3/assert"
)

func TestUnknownFlagIsReportedAsBadArgument(t *testing.T) {
	root := newTestRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs([]string{"--not-a-real-flag"})

	_, err := root.ExecuteC()
	assert.Assert(t, err != nil)

	var userErr *userError
	assert.Assert(t, errors.As(err, &userErr))
	assert.Equal(t, userErr.UserMessage(), "unknown flag: --not-a-real-flag")
	assert.Equal(t, userErr.UserExitCode(), ExitBadArgs)
	assert.Assert(t, userErr.HideDetail())
}
