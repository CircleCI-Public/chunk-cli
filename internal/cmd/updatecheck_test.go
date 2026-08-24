package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

func TestSkipUpdateCheck(t *testing.T) {
	root := NewRootCmd("v1.0.0")

	// Cobra adds the __complete commands during Execute rather than up
	// front, so they are checked by name. It runs root's persistent hooks
	// for them, meaning the check would otherwise fire on every completion
	// TAB press.
	for _, name := range []string{cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd} {
		if !skipUpdateCheck(&cobra.Command{Use: name}) {
			t.Errorf("expected update check to be skipped for %q", name)
		}
	}

	// receive-telemetry is re-execed by every chunk invocation, and upgrade
	// and watch report versions themselves.
	for _, name := range []string{cmdNameCompletion, cmdNameReceiveTelemetry, cmdNameUpgrade, cmdNameWatch} {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatalf("find %q: %v", name, err)
		}
		if cmd.Name() != name {
			t.Fatalf("Find(%q) resolved to %q — command missing from the tree", name, cmd.Name())
		}
		if !skipUpdateCheck(cmd) {
			t.Errorf("expected update check to be skipped for %q", name)
		}
	}

	// Subcommands of a skipped command are skipped too (completion has one
	// per shell).
	completion, _, err := root.Find([]string{cmdNameCompletion})
	if err != nil {
		t.Fatalf("find completion: %v", err)
	}
	if len(completion.Commands()) == 0 {
		t.Fatal("expected completion to have per-shell subcommands")
	}
	for _, sub := range completion.Commands() {
		if !skipUpdateCheck(sub) {
			t.Errorf("expected update check to be skipped for %q", sub.CommandPath())
		}
	}

	// Every other command keeps the check.
	for _, cmd := range root.Commands() {
		if noUpdateCheckCommands[cmd.Name()] {
			continue
		}
		if skipUpdateCheck(cmd) {
			t.Errorf("expected update check to run for %q", cmd.Name())
		}
	}
}
