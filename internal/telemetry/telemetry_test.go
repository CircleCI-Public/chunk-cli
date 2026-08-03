package telemetry

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/segmentio/analytics-go/v3"
	"github.com/spf13/cobra"
	"gotest.tools/v3/assert"
)

// fakeDestination records Enqueue calls synchronously so tests can assert on
// them without spawning a subprocess or hitting the network.
type fakeDestination struct {
	tracks []analytics.Track
	closed bool
}

func (f *fakeDestination) Enqueue(t analytics.Track) error {
	f.tracks = append(f.tracks, t)
	return nil
}

func (f *fakeDestination) Close() error {
	f.closed = true
	return nil
}

func TestSender_Track(t *testing.T) {
	fake := &fakeDestination{}
	instanceID := uuid.New()

	s, err := NewSender(Config{
		TestDestination: fake,
		Metadata: Meta{
			Version:     "1.2.3",
			InstanceID:  instanceID,
			OS:          "linux",
			CodingAgent: agentClaudeCode,
		},
	})
	assert.NilError(t, err)

	assert.NilError(t, s.Track("command_invocation", map[string]any{
		"command": "chunk config show",
		"flags":   "json",
	}))

	assert.Equal(t, len(fake.tracks), 1)
	tr := fake.tracks[0]
	assert.Equal(t, tr.Event, "command_invocation")
	assert.Equal(t, tr.UserId, instanceID.String())
	assert.Equal(t, tr.Properties["command"], "chunk config show")
	assert.Equal(t, tr.Properties["flags"], "json")
	assert.Equal(t, tr.Context.App.Version, "1.2.3")
	assert.Equal(t, tr.Context.Device.Id, instanceID.String())
	assert.Equal(t, tr.Context.OS.Name, "linux")
	assert.Equal(t, tr.Context.Extra["codingAgent"], agentClaudeCode)
}

func TestMeta_ToContext_OmitsCodingAgentWhenUndetected(t *testing.T) {
	m := Meta{Version: "1.2.3", OS: "darwin"}
	ctx := m.toContext()

	assert.Equal(t, ctx.OS.Name, "darwin")
	_, ok := ctx.Extra["codingAgent"]
	assert.Assert(t, !ok)
}

func TestSender_CloseIsIdempotent(t *testing.T) {
	fake := &fakeDestination{}
	s, err := NewSender(Config{TestDestination: fake})
	assert.NilError(t, err)

	assert.NilError(t, s.Close())
	assert.NilError(t, s.Close())
	assert.Assert(t, fake.closed)
}

func TestSender_NilSenderIsSafe(t *testing.T) {
	var s *Sender
	assert.NilError(t, s.Track("command_invocation", nil))
	assert.NilError(t, s.Close())
}

func TestNewSender_SendWithoutBinaryErrors(t *testing.T) {
	_, err := NewSender(Config{Send: true})
	assert.ErrorContains(t, err, "binary is required")
}

// --- RecordForSubcommands ---

func TestRecordForSubcommands_TracksCommandInvocation(t *testing.T) {
	fake := &fakeDestination{}
	s, err := NewSender(Config{TestDestination: fake})
	assert.NilError(t, err)

	root := &cobra.Command{Use: "chunk"}
	child := &cobra.Command{
		Use: "show",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return nil
		},
	}
	child.Flags().Bool("json", false, "")
	root.AddCommand(child)
	RecordForSubcommands(root)

	root.SetContext(WithSender(context.Background(), s))
	root.SetArgs([]string{"show", "--json"})
	assert.NilError(t, root.Execute())

	assert.Equal(t, len(fake.tracks), 1)
	assert.Equal(t, fake.tracks[0].Event, "chunk_command_invocation")
	assert.Equal(t, fake.tracks[0].Properties["command"], "chunk show")
	assert.Equal(t, fake.tracks[0].Properties["flags"], "json")
	assert.Equal(t, fake.tracks[0].Properties["success"], true)
	_, hasErrType := fake.tracks[0].Properties["error_type"]
	assert.Assert(t, !hasErrType)
}

func TestRecordForSubcommands_TracksErrorOnFailure(t *testing.T) {
	fake := &fakeDestination{}
	s, err := NewSender(Config{TestDestination: fake})
	assert.NilError(t, err)

	root := &cobra.Command{Use: "chunk"}
	child := &cobra.Command{
		Use: "fail",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return errors.New("something went wrong")
		},
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.AddCommand(child)
	RecordForSubcommands(root)

	root.SetContext(WithSender(context.Background(), s))
	root.SetArgs([]string{"fail"})
	_ = root.Execute()

	assert.Equal(t, len(fake.tracks), 1)
	assert.Equal(t, fake.tracks[0].Properties["success"], false)
	assert.Equal(t, fake.tracks[0].Properties["error_type"], "*errors.errorString")
}

func TestRecordForSubcommands_SkipsDisabledCommand(t *testing.T) {
	fake := &fakeDestination{}
	s, err := NewSender(Config{TestDestination: fake})
	assert.NilError(t, err)

	root := &cobra.Command{Use: "chunk"}
	child := &cobra.Command{
		Use: "receive-telemetry",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return nil
		},
	}
	DisableTelemetry(child)
	root.AddCommand(child)
	RecordForSubcommands(root)

	root.SetContext(WithSender(context.Background(), s))
	root.SetArgs([]string{"receive-telemetry"})
	assert.NilError(t, root.Execute())

	assert.Equal(t, len(fake.tracks), 0)
}

func TestFromContext_NoSenderAttached(t *testing.T) {
	assert.Assert(t, FromContext(t.Context()) == nil)
}
