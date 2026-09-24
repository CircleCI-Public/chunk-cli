package cmd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/segmentio/analytics-go/v3"
	gokeyring "github.com/zalando/go-keyring"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/keyring"
	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func TestAuthSignupAlreadyAuthenticated(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "test-token-abc")

	cmd := insecureStorageCmd(newAuthSignupCmd())
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true

	err := cmd.Execute()
	assert.Assert(t, err != nil)

	var ue *userError
	assert.Assert(t, errors.As(err, &ue))
	assert.Assert(t, strings.Contains(ue.UserMessage(), "Already authenticated."),
		"expected 'Already authenticated.' in message, got: %s", ue.UserMessage())
	assert.Assert(t, strings.Contains(ue.Suggestion(), "chunk auth remove"),
		"expected suggestion to mention 'chunk auth remove', got: %s", ue.Suggestion())
}

// trackingDestination records the telemetry messages a command reports, so
// tests can assert on de-anonymization without a network round trip. The
// Sender's destination interface is unexported, but a composite literal can
// still be assigned a value implementing it.
type trackingDestination struct {
	tracks     []analytics.Track
	identifies []analytics.Identify
}

func (d *trackingDestination) Enqueue(m analytics.Message) error {
	switch msg := m.(type) {
	case analytics.Track:
		d.tracks = append(d.tracks, msg)
	case analytics.Identify:
		d.identifies = append(d.identifies, msg)
	}
	return nil
}

func (d *trackingDestination) Close() error { return nil }

func TestSaveCircleCIToken_IdentifiesUser(t *testing.T) {
	isolateConfig(t)

	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()
	t.Setenv(config.EnvCircleCIBaseURL, srv.URL)

	dest := &trackingDestination{}
	instanceID := uuid.New()
	sender, err := telemetry.NewSender(telemetry.Config{
		TestDestination: dest,
		Metadata:        telemetry.Meta{InstanceID: instanceID},
	})
	assert.NilError(t, err)

	ctx := telemetry.WithSender(context.Background(), sender)
	err = saveCircleCIToken(ctx, "cci-test-token", discardStreams(), srv.URL, true)
	assert.NilError(t, err)

	const wantUserID = "00000000-0000-0000-0000-000000000123"

	// Persisted, so later invocations report the user without logging in again.
	assert.Equal(t, config.GetUserID().String(), wantUserID)

	// And identified now, so the anonymous events from before the login join
	// the same profile.
	assert.Equal(t, len(dest.identifies), 1)
	assert.Equal(t, dest.identifies[0].UserId, wantUserID)
	assert.Equal(t, dest.identifies[0].AnonymousId, instanceID.String())

	// The in-flight invocation is no longer anonymous either.
	assert.NilError(t, sender.Track("command_invocation", nil))
	assert.Equal(t, len(dest.tracks), 1)
	assert.Equal(t, dest.tracks[0].UserId, wantUserID)
}

func TestSaveCircleCIToken_WithoutTelemetrySender(t *testing.T) {
	isolateConfig(t)

	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()
	t.Setenv(config.EnvCircleCIBaseURL, srv.URL)

	err := saveCircleCIToken(context.Background(), "cci-test-token", discardStreams(), srv.URL, true)
	assert.NilError(t, err)
	assert.Equal(t, config.GetUserID().String(), "00000000-0000-0000-0000-000000000123")
}

func TestAuthRemoveCircleCI_ClearsUserID(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	cfg, err := config.Load()
	assert.NilError(t, err)
	cfg.CircleCIToken = "cci-test-token"
	assert.NilError(t, config.Save(cfg))
	assert.NilError(t, config.SaveUserID(uuid.New()))

	assert.NilError(t, authRemoveCircleCI(discardStreams(), false, true, true))

	assert.Equal(t, config.GetUserID(), uuid.Nil, "a logged-out user should report anonymously again")
}

func TestAuthRemoveCircleCI_KeepsUserIDWhileEnvTokenAuthenticates(t *testing.T) {
	isolateConfig(t)
	t.Setenv(config.EnvCircleToken, "cci-env-token")
	t.Setenv(config.EnvCircleCIToken, "")

	cfg, err := config.Load()
	assert.NilError(t, err)
	cfg.CircleCIToken = "cci-test-token"
	assert.NilError(t, config.Save(cfg))

	userID := uuid.New()
	assert.NilError(t, config.SaveUserID(userID))

	assert.NilError(t, authRemoveCircleCI(discardStreams(), true, true, true))

	// The env token still authenticates every later command, and nothing
	// short of an explicit login would re-persist the ID, so clearing it here
	// would leave an authenticated user reporting anonymously for good.
	assert.Equal(t, config.GetUserID(), userID,
		"a user the environment still authenticates should keep reporting as themselves")
}

func TestAuthRemoveCircleCI_InsecureStorageKeepsUserIDWhileKeychainTokenAuthenticates(t *testing.T) {
	isolateConfig(t)
	gokeyring.MockInit()
	t.Setenv(config.EnvCircleToken, "")
	t.Setenv(config.EnvCircleCIToken, "")

	cfg, err := config.Load()
	assert.NilError(t, err)
	cfg.CircleCIToken = "cci-config-token"
	assert.NilError(t, config.Save(cfg))
	service := keyring.ServiceCircleCI(unroutableBaseURL)
	assert.NilError(t, keyring.Set(service, "cci-keychain-token"))
	t.Cleanup(func() { _ = keyring.Delete(service) })

	userID := uuid.New()
	assert.NilError(t, config.SaveUserID(userID))

	// --insecure-storage removes only the config-file token; the keychain
	// token still authenticates every normal run.
	assert.NilError(t, authRemoveCircleCI(discardStreams(), false, true, true))

	assert.Equal(t, config.GetUserID(), userID,
		"a user the keychain still authenticates should keep reporting as themselves")
}

// backfillFixture signs an install in with a token but no saved user ID, as an
// install that logged in before chunk persisted the ID does, and attaches a
// telemetry Sender recording what it reports.
func backfillFixture(t *testing.T) (*fakes.FakeCircleCI, config.ResolvedConfig, context.Context, *trackingDestination) {
	t.Helper()
	isolateConfig(t)
	// Telemetry is off under CI; these tests exercise the opted-in path.
	for _, env := range []string{config.EnvChunkNoTelemetry, config.EnvNoAnalytics, config.EnvDoNotTrack, config.EnvCI} {
		t.Setenv(env, "")
	}

	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)
	t.Setenv(config.EnvCircleCIBaseURL, srv.URL)
	t.Setenv(config.EnvCircleToken, "cci-env-token")
	t.Setenv(config.EnvCircleCIToken, "")

	rc, err := config.Resolve("", "", true)
	assert.NilError(t, err)

	dest := &trackingDestination{}
	sender, err := telemetry.NewSender(telemetry.Config{
		TestDestination: dest,
		Metadata:        telemetry.Meta{InstanceID: uuid.New()},
	})
	assert.NilError(t, err)
	return cci, rc, telemetry.WithSender(context.Background(), sender), dest
}

func currentUserRequests(cci *fakes.FakeCircleCI) int {
	n := 0
	for _, r := range cci.Recorder.AllRequests() {
		if r.URL.Path == "/api/v2/me" {
			n++
		}
	}
	return n
}

func TestEnsureCircleCIClient_BackfillsUserID(t *testing.T) {
	_, rc, ctx, dest := backfillFixture(t)

	_, err := ensureCircleCIClient(ctx, testCmd(), rc, discardStreams(), noTTYPrompter)
	assert.NilError(t, err)

	const wantUserID = "00000000-0000-0000-0000-000000000123"
	assert.Equal(t, config.GetUserID().String(), wantUserID)
	assert.Equal(t, len(dest.identifies), 1)
	assert.Equal(t, dest.identifies[0].UserId, wantUserID)

	assert.NilError(t, telemetry.FromContext(ctx).Track("command_invocation", nil))
	assert.Equal(t, dest.tracks[0].UserId, wantUserID)
}

func TestEnsureCircleCIClient_KeepsSavedUserID(t *testing.T) {
	cci, rc, ctx, dest := backfillFixture(t)
	saved := uuid.New()
	assert.NilError(t, config.SaveUserID(saved))

	_, err := ensureCircleCIClient(ctx, testCmd(), rc, discardStreams(), noTTYPrompter)
	assert.NilError(t, err)

	assert.Equal(t, config.GetUserID(), saved)
	assert.Equal(t, currentUserRequests(cci), 0, "a saved user ID should not be looked up again")
	assert.Equal(t, len(dest.identifies), 0)
}

func TestEnsureCircleCIClient_NoBackfillWhenTelemetryOff(t *testing.T) {
	cci, rc, ctx, dest := backfillFixture(t)
	t.Setenv(config.EnvDoNotTrack, "1")

	_, err := ensureCircleCIClient(ctx, testCmd(), rc, discardStreams(), noTTYPrompter)
	assert.NilError(t, err)

	assert.Equal(t, config.GetUserID(), uuid.Nil)
	assert.Equal(t, currentUserRequests(cci), 0)
	assert.Equal(t, len(dest.identifies), 0)
}

func TestEnsureCircleCIClient_BackfillFailureStaysAnonymous(t *testing.T) {
	cci, rc, ctx, dest := backfillFixture(t)
	cci.CurrentUserStatusCode = http.StatusTooManyRequests

	_, err := ensureCircleCIClient(ctx, testCmd(), rc, discardStreams(), noTTYPrompter)
	assert.NilError(t, err, "a failed lookup must not fail the command")

	assert.Equal(t, config.GetUserID(), uuid.Nil, "nothing saved, so the next command retries")
	assert.Equal(t, len(dest.identifies), 0)
}
