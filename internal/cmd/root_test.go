package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/segmentio/analytics-go/v3"
	"github.com/spf13/cobra"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
)

// TestRootHelp_StartHereThenOtherCommands asserts the shape required by
// FACT-576: an ordered "Start here" onboarding flow (connect Claude,
// bootstrap, watch), ahead of every other command still listed (just under a
// different heading) so none of them disappear, with the Short line and
// reference sections intact.
func TestRootHelp_StartHereThenOtherCommands(t *testing.T) {
	root := newTestRootCmd()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"--help"})

	assert.NilError(t, root.Execute())
	help := out.String()

	wantShort := "Hand work to agents and review it before you push"
	assert.Equal(t, root.Short, wantShort)
	assert.Assert(t, strings.HasPrefix(help, wantShort), "help should open with the root Short text:\n%s", help)
	assert.Assert(t, !strings.Contains(help, "Generate AI review context and trigger AI coding tasks"),
		"help should no longer carry the old root Short text:\n%s", help)

	startHere := strings.Index(help, "Start here")
	otherCommands := strings.Index(help, "Other commands")
	assert.Assert(t, startHere >= 0, "help should contain a Start here section:\n%s", help)
	assert.Assert(t, otherCommands > startHere, "Other commands should come after Start here:\n%s", help)

	startHereSection := help[startHere:otherCommands]
	wantOrder := []string{
		"chunk auth set anthropic-oauth",
		"chunk auth set anthropic ",
		"chunk factory bootstrap",
		"chunk " + watchCmdName,
		"[n]",
		"[p]",
	}
	lastIdx := -1
	for _, want := range wantOrder {
		idx := strings.Index(startHereSection, want)
		assert.Assert(t, idx >= 0, "Start here should list %q:\n%s", want, startHereSection)
		assert.Assert(t, idx > lastIdx, "%q should come after the previous Start here entry:\n%s", want, startHereSection)
		lastIdx = idx
	}

	assert.Assert(t, !strings.Contains(startHereSection, "chunk init"), "chunk init should no longer be in Start here:\n%s", startHereSection)
	assert.Assert(t, !strings.Contains(startHereSection, "chunk factory build"), "chunk factory build should no longer be in Start here:\n%s", startHereSection)
	assert.Assert(t, !strings.Contains(startHereSection, "chunk factory start"), "chunk factory start should no longer be in Start here:\n%s", startHereSection)

	assert.Assert(t, !strings.Contains(startHereSection, "task config"), "task config should not be in Start here:\n%s", startHereSection)
	assert.Assert(t, !strings.Contains(startHereSection, "task run"), "task run should not be in Start here:\n%s", startHereSection)

	otherCommandsSection := help[otherCommands:]
	assert.Assert(t, strings.Contains(otherCommandsSection, "task"), "task should still be listed under Other commands:\n%s", otherCommandsSection)
	assert.Assert(t, strings.Contains(otherCommandsSection, "init"), "init should still be listed under Other commands:\n%s", otherCommandsSection)

	// The root template replaces cobra's own usage block, so the flags only a
	// reader of this help would ever find have to be put back by hand.
	assert.Assert(t, strings.Contains(help, "Usage:"), "help should say how to invoke chunk:\n%s", help)
	for _, flag := range []string{"-h, --help", "-v, --version", "--no-daemon"} {
		assert.Assert(t, strings.Contains(help, flag), "help should list %s:\n%s", flag, help)
	}

	assert.Assert(t, strings.Contains(help, "Environment Variables:"), "help should keep the Environment Variables section:\n%s", help)
	assert.Assert(t, strings.Contains(help, "Configuration:"), "help should keep the Configuration section:\n%s", help)

	envIdx := strings.Index(help, "Environment Variables:")
	configIdx := strings.Index(help, "Configuration:")
	assert.Assert(t, envIdx > otherCommands, "Environment Variables should come after Other commands:\n%s", help)
	assert.Assert(t, configIdx > envIdx, "Configuration should come after Environment Variables:\n%s", help)
}

// flushRecordingDestination records whether the Sender was flushed, and what
// it was holding at the time.
type flushRecordingDestination struct {
	messages []analytics.Message
	closed   bool
}

func (d *flushRecordingDestination) Enqueue(m analytics.Message) error {
	d.messages = append(d.messages, m)
	return nil
}

func (d *flushRecordingDestination) Close() error {
	d.closed = true
	return nil
}

// rootWithSender builds a command tree shaped like the real one: a root that
// attaches a telemetry sender in PersistentPreRunE, and a subcommand whose
// RunE reports an event and then returns runErr.
func rootWithSender(t *testing.T, dest *flushRecordingDestination, runErr error) *cobra.Command {
	t.Helper()

	root := &cobra.Command{Use: "chunk", SilenceErrors: true, SilenceUsage: true}
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		sender, err := telemetry.NewSender(telemetry.Config{TestDestination: dest})
		assert.NilError(t, err)
		cmd.SetContext(telemetry.WithSender(cmd.Context(), sender))
		return nil
	}
	root.PersistentPostRunE = func(*cobra.Command, []string) error { return nil }
	root.AddCommand(&cobra.Command{
		Use: "child",
		RunE: func(cmd *cobra.Command, _ []string) error {
			assert.NilError(t, telemetry.FromContext(cmd.Context()).Track("command_invocation", nil))
			return runErr
		},
	})
	root.SetArgs([]string{"child"})
	return root
}

func TestExecuteRoot_FlushesTelemetryOnSuccess(t *testing.T) {
	dest := &flushRecordingDestination{}
	assert.NilError(t, ExecuteRoot(rootWithSender(t, dest, nil)))

	assert.Equal(t, dest.closed, true)
	assert.Equal(t, len(dest.messages), 1)
}

// Cobra skips the post-run hooks once RunE reports an error, so a flush that
// lived there would drop everything a failed invocation buffered — including
// the one-shot identify that joins a mid-command login to the user.
func TestExecuteRoot_FlushesTelemetryWhenCommandFails(t *testing.T) {
	dest := &flushRecordingDestination{}
	wantErr := errors.New("boom")

	err := ExecuteRoot(rootWithSender(t, dest, wantErr))
	assert.Assert(t, errors.Is(err, wantErr), "ExecuteRoot should return the command's error, got: %v", err)

	assert.Equal(t, dest.closed, true, "buffered telemetry must be flushed even when the command fails")
	assert.Equal(t, len(dest.messages), 1)
}
