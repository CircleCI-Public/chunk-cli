package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/version"
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
	// and ui report versions themselves.
	for _, name := range []string{"completion", "receive-telemetry", "upgrade", "ui"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatalf("find %q: %v", name, err)
		}
		// Find falls back to root for an unknown name, so this is what
		// catches a command being renamed out from under the skip map.
		if cmd.Name() != name {
			t.Fatalf("Find(%q) resolved to %q — command missing from the tree", name, cmd.Name())
		}
		if !skipUpdateCheck(cmd) {
			t.Errorf("expected update check to be skipped for %q", name)
		}
	}

	// The hidden "watch" alias must resolve to the same command and skip list
	// entry as "ui", so switching spelling does not lose the skip.
	if cmd, _, err := root.Find([]string{"watch"}); err != nil {
		t.Fatalf("find watch: %v", err)
	} else {
		if cmd.Name() != "ui" {
			t.Fatalf("Find(%q) resolved to %q, want the ui command", "watch", cmd.Name())
		}
		if !skipUpdateCheck(cmd) {
			t.Error("expected update check to be skipped for the watch alias")
		}
	}

	// Subcommands of a skipped command are skipped too (completion has one
	// per shell).
	completion, _, err := root.Find([]string{"completion"})
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

}

// TestShouldAutoLaunchSkipsUIAndItsWatchAlias pins the other skip list next to
// noUpdateCheckCommands: ui starts the daemon itself, so auto-launch must not
// also try to for it or its hidden watch alias. --daemon is set on each found
// command so a stray skip-list key (e.g. a literal "watch" instead of
// uiCmdName) would otherwise fall through to a true result here, rather than
// being masked by the default config's autoLaunchDaemon being false too.
func TestShouldAutoLaunchSkipsUIAndItsWatchAlias(t *testing.T) {
	root := NewRootCmd("v1.0.0")
	for _, name := range []string{"ui", "watch"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatalf("find %q: %v", name, err)
		}
		if cmd.Name() != "ui" {
			t.Fatalf("Find(%q) resolved to %q, want the ui command", name, cmd.Name())
		}
		if err := cmd.ParseFlags([]string{"--daemon"}); err != nil {
			t.Fatalf("parse flags for %q: %v", name, err)
		}
		if shouldAutoLaunch(cmd) {
			t.Errorf("expected auto-launch to be skipped for %q even with --daemon", name)
		}
	}
}

func TestPrintUpdateNotice(t *testing.T) {
	newCmd := func(ch chan string) (*cobra.Command, *bytes.Buffer) {
		cmd := &cobra.Command{Use: "test"}
		var stderr bytes.Buffer
		cmd.SetErr(&stderr)
		ctx := context.Background()
		if ch != nil {
			ctx = context.WithValue(ctx, updateCheckKey{}, ch)
		}
		cmd.SetContext(ctx)
		return cmd, &stderr
	}

	t.Run("prints when a newer version is already available", func(t *testing.T) {
		ch := make(chan string, 1)
		ch <- "v9.9.9"
		cmd, stderr := newCmd(ch)

		printUpdateNotice(cmd)

		if !strings.Contains(stderr.String(), "v9.9.9") {
			t.Errorf("expected notice mentioning v9.9.9, got %q", stderr.String())
		}
	})

	t.Run("shows the running version and links the release notes", func(t *testing.T) {
		orig := version.Value
		version.Value = "1.2.3" // the ldflags form: no leading "v"
		t.Cleanup(func() { version.Value = orig })

		ch := make(chan string, 1)
		ch <- "v9.9.9"
		cmd, stderr := newCmd(ch)

		printUpdateNotice(cmd)

		for _, want := range []string{
			"v1.2.3 → v9.9.9",
			"https://github.com/CircleCI-Public/chunk-cli/releases/tag/v9.9.9",
		} {
			if !strings.Contains(stderr.String(), want) {
				t.Errorf("expected notice to contain %q, got %q", want, stderr.String())
			}
		}
	})

	// A fetch still in flight must not hold up the command: the check claims
	// its cache window before the request, so the notice lands on a later run.
	t.Run("does not wait on a check still in flight", func(t *testing.T) {
		cmd, stderr := newCmd(make(chan string, 1))

		done := make(chan struct{})
		go func() {
			printUpdateNotice(cmd)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("printUpdateNotice blocked on a pending check")
		}

		if stderr.String() != "" {
			t.Errorf("expected no output, got %q", stderr.String())
		}
	})

	t.Run("prints nothing when up to date", func(t *testing.T) {
		ch := make(chan string, 1)
		ch <- ""
		cmd, stderr := newCmd(ch)

		printUpdateNotice(cmd)

		if stderr.String() != "" {
			t.Errorf("expected no output, got %q", stderr.String())
		}
	})

	t.Run("prints nothing when the check never started", func(t *testing.T) {
		cmd, stderr := newCmd(nil)

		printUpdateNotice(cmd)

		if stderr.String() != "" {
			t.Errorf("expected no output, got %q", stderr.String())
		}
	})
}
