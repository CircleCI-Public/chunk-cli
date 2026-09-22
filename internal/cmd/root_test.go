package cmd

import (
	"errors"
	"testing"

	"github.com/segmentio/analytics-go/v3"
	"github.com/spf13/cobra"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
)

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
