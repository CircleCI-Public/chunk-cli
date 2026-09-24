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
	tracks     []analytics.Track
	identifies []analytics.Identify
	closed     bool
}

func (f *fakeDestination) Enqueue(m analytics.Message) error {
	switch msg := m.(type) {
	case analytics.Track:
		f.tracks = append(f.tracks, msg)
	case analytics.Identify:
		f.identifies = append(f.identifies, msg)
	}
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
			Version:        "1.2.3",
			InstanceID:     instanceID,
			OSName:         "linux",
			PlatformFamily: "debian",
			OSVersion:      "24.04",
			KernelArch:     "x86_64",
			Extra:          map[string]any{"agent": agentClaudeCode},
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
	assert.Equal(t, tr.AnonymousId, instanceID.String())
	assert.Equal(t, tr.UserId, "")
	assert.Equal(t, tr.Properties["command"], "chunk config show")
	assert.Equal(t, tr.Properties["flags"], "json")
	assert.Equal(t, tr.Context.App.Name, "chunk-cli")
	assert.Equal(t, tr.Context.App.Version, "1.2.3")
	assert.Equal(t, tr.Context.Device.Id, instanceID.String())
	assert.Equal(t, tr.Context.Device.Model, "x86_64")
	assert.Equal(t, tr.Context.Device.Type, "debian")
	assert.Equal(t, tr.Context.OS.Name, "linux")
	assert.Equal(t, tr.Context.OS.Version, "24.04")
	assert.Equal(t, tr.Context.Traits["agent"], agentClaudeCode)
	assert.Assert(t, tr.Integrations["Amplitude"] == true)
}

func TestSender_Track_WithSessionTrackingID(t *testing.T) {
	fake := &fakeDestination{}
	instanceID := uuid.New()
	sessionID := uuid.New()

	s, err := NewSender(Config{
		TestDestination: fake,
		Metadata: Meta{
			Version:           "1.2.3",
			InstanceID:        instanceID,
			SessionTrackingID: sessionID,
		},
	})
	assert.NilError(t, err)

	assert.NilError(t, s.Track("command_invocation", nil))

	tr := fake.tracks[0]
	assert.Equal(t, tr.AnonymousId, sessionID.String(), "session ID should be used as AnonymousId")
	assert.Equal(t, tr.Context.Device.Id, instanceID.String(), "install ID should remain in device context")
}

func TestSender_Track_WithUserID(t *testing.T) {
	fake := &fakeDestination{}
	instanceID := uuid.New()
	userID := uuid.New()

	s, err := NewSender(Config{
		TestDestination: fake,
		Metadata: Meta{
			Version:    "1.2.3",
			InstanceID: instanceID,
			UserID:     userID,
		},
	})
	assert.NilError(t, err)

	assert.NilError(t, s.Track("command_invocation", nil))

	tr := fake.tracks[0]
	assert.Equal(t, tr.AnonymousId, instanceID.String())
	assert.Equal(t, tr.UserId, userID.String())
}

func TestMeta_ToContext_NilHostInfo(t *testing.T) {
	m := Meta{Version: "1.2.3"}
	ctx := m.toContext()

	assert.Equal(t, ctx.OS.Name, "")
	assert.Equal(t, ctx.OS.Version, "")
	assert.Equal(t, ctx.Device.Model, "")
	assert.Equal(t, ctx.Device.Type, "")
}

func TestMeta_ToContext_NilExtraOmitsTraits(t *testing.T) {
	m := Meta{Version: "1.2.3"}
	ctx := m.toContext()

	assert.Assert(t, ctx.Traits == nil)
}

func TestMeta_ToContext_EmptyAgentNotForwarded(t *testing.T) {
	m := Meta{Version: "1.2.3", Extra: map[string]any{"agent": ""}}
	ctx := m.toContext()

	_, ok := ctx.Traits["agent"]
	assert.Assert(t, !ok, "empty agent string should not appear in Traits")
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
	assert.Equal(t, fake.tracks[0].Event, "command_invocation")
	assert.Equal(t, fake.tracks[0].Context.App.Name, "chunk-cli")
	assert.Equal(t, fake.tracks[0].Properties["command"], "chunk show")
	assert.Equal(t, fake.tracks[0].Properties["flags"], "json")
	assert.Equal(t, fake.tracks[0].Properties["outcome"], "success")
	assert.Assert(t, fake.tracks[0].Properties["duration_ms"].(int64) >= 0)
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
	assert.Equal(t, fake.tracks[0].Properties["outcome"], "failure")
	assert.Assert(t, fake.tracks[0].Properties["duration_ms"].(int64) >= 0)
	assert.Equal(t, fake.tracks[0].Properties["error_type"], "*errors.errorString")
	assert.Equal(t, fake.tracks[0].Properties["error_message"], "something went wrong")
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

func TestSender_SetUserID_AppliesToLaterEvents(t *testing.T) {
	fake := &fakeDestination{}
	userID := uuid.New()

	s, err := NewSender(Config{
		TestDestination: fake,
		Metadata:        Meta{InstanceID: uuid.New()},
	})
	assert.NilError(t, err)

	assert.NilError(t, s.Track("before_auth", nil))
	s.SetUserID(userID)
	assert.NilError(t, s.Track("after_auth", nil))

	assert.Equal(t, len(fake.tracks), 2)
	assert.Equal(t, fake.tracks[0].UserId, "")
	assert.Equal(t, fake.tracks[1].UserId, userID.String())
}

func TestSender_Identify_JoinsInstanceIDToUser(t *testing.T) {
	fake := &fakeDestination{}
	instanceID := uuid.New()
	userID := uuid.New()

	s, err := NewSender(Config{
		TestDestination: fake,
		Metadata:        Meta{Version: "1.2.3", InstanceID: instanceID},
	})
	assert.NilError(t, err)

	assert.NilError(t, s.Identify(userID))

	assert.Equal(t, len(fake.identifies), 1)
	id := fake.identifies[0]
	assert.Equal(t, id.UserId, userID.String())
	assert.Equal(t, id.AnonymousId, instanceID.String())
	assert.Equal(t, id.Context.App.Name, "chunk-cli")
	assert.Assert(t, !id.Timestamp.IsZero())
}

func TestSender_Identify_JoinsSessionAndInstanceIDs(t *testing.T) {
	fake := &fakeDestination{}
	instanceID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()

	s, err := NewSender(Config{
		TestDestination: fake,
		Metadata: Meta{
			InstanceID:        instanceID,
			SessionTrackingID: sessionID,
		},
	})
	assert.NilError(t, err)

	assert.NilError(t, s.Identify(userID))

	assert.Equal(t, len(fake.identifies), 2, "both anonymous threads should join the user")
	assert.Equal(t, fake.identifies[0].AnonymousId, sessionID.String())
	assert.Equal(t, fake.identifies[1].AnonymousId, instanceID.String())
	for _, id := range fake.identifies {
		assert.Equal(t, id.UserId, userID.String())
	}
}

func TestSender_Identify_NoUserIDIsNoop(t *testing.T) {
	fake := &fakeDestination{}
	s, err := NewSender(Config{
		TestDestination: fake,
		Metadata:        Meta{InstanceID: uuid.New()},
	})
	assert.NilError(t, err)

	assert.NilError(t, s.Identify(uuid.Nil))
	assert.Equal(t, len(fake.identifies), 0)
}

func TestSender_Identify_NilSenderIsSafe(t *testing.T) {
	var s *Sender
	assert.NilError(t, s.Identify(uuid.New()))
	s.SetUserID(uuid.New())
}

func TestIdentifyUser_SetsUserIDOnCommandInvocation(t *testing.T) {
	fake := &fakeDestination{}
	instanceID := uuid.New()
	userID := uuid.New()

	s, err := NewSender(Config{
		TestDestination: fake,
		Metadata:        Meta{InstanceID: instanceID},
	})
	assert.NilError(t, err)

	root := &cobra.Command{Use: "chunk"}
	login := &cobra.Command{
		Use: "login",
		RunE: func(cmd *cobra.Command, _ []string) error {
			IdentifyUser(cmd.Context(), userID)
			return nil
		},
	}
	root.AddCommand(login)
	RecordForSubcommands(root)

	root.SetContext(WithSender(context.Background(), s))
	root.SetArgs([]string{"login"})
	assert.NilError(t, root.Execute())

	assert.Equal(t, len(fake.identifies), 1)
	assert.Equal(t, fake.identifies[0].AnonymousId, instanceID.String())
	assert.Equal(t, fake.identifies[0].UserId, userID.String())

	assert.Equal(t, len(fake.tracks), 1)
	assert.Equal(t, fake.tracks[0].Event, "command_invocation")
	assert.Equal(t, fake.tracks[0].UserId, userID.String(),
		"the invocation the user authenticated in should not report anonymously")
}

func TestIdentifyUser_NoSenderInContext(t *testing.T) {
	IdentifyUser(context.Background(), uuid.New())
}
