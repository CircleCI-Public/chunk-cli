package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

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
	for _, name := range []string{"completion", "receive-telemetry", "upgrade", "watch"} {
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
