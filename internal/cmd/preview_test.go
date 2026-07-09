package cmd

import (
	"bytes"
	"testing"

	"gotest.tools/v3/assert"
)

func TestPreviewRequiresPortAndCommand(t *testing.T) {
	cmd := newPreviewCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})

	err := cmd.Execute()
	assert.ErrorContains(t, err, `required flag(s) "command", "port" not set`)
}

func TestPreviewRequiresCommandWhenPortSet(t *testing.T) {
	cmd := newPreviewCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--port", "3000"})

	err := cmd.Execute()
	assert.ErrorContains(t, err, `required flag(s) "command" not set`)
}
