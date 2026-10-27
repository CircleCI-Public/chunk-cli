package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// chunk commands walks the command tree on its own (walkCommands), separate
// from the root help template root_test.go exercises, so it needs its own
// check that it lists `chunk ui` and never the hidden `watch` alias.
func TestCommandsListsUIAndNotItsWatchAlias(t *testing.T) {
	root := newTestRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"commands"})

	if err := root.Execute(); err != nil {
		t.Fatalf("chunk commands: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "chunk ui ") {
		t.Errorf("expected %q to list %q, got:\n%s", "chunk commands", "chunk ui", got)
	}
	if strings.Contains(got, "chunk watch") {
		t.Errorf("expected %q not to list the hidden watch alias, got:\n%s", "chunk commands", got)
	}
}
