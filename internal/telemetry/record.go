package telemetry

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Outcomes a command_invocation event can report. Only success and the three
// failure kinds exist so a dashboard can tell a run that never got going
// (blocked) or whose checks did not pass (check_failed) from chunk itself
// going wrong (failure).
const (
	OutcomeSuccess     = "success"
	OutcomeBlocked     = "blocked"      // the user could not proceed: bad args, not authed, not configured, not found
	OutcomeCheckFailed = "check_failed" // chunk worked, but the project's own checks did not pass
	OutcomeFailure     = "failure"      // an actual error: API failure, internal error, anything unclassified
)

// Classified is implemented by errors that know which outcome they represent.
// Commands live above this package, so errors describe themselves rather than
// RecordNow importing their types.
type Classified interface {
	TelemetryOutcome() string
}

// outcomeOf returns the outcome to report for err: success for nil, the
// outcome of the first Classified error in its chain, else failure.
func outcomeOf(err error) string {
	if err == nil {
		return OutcomeSuccess
	}
	var c Classified
	if errors.As(err, &c) {
		return c.TelemetryOutcome()
	}
	return OutcomeFailure
}

type senderKey struct{}

// WithSender attaches s to ctx so descendant commands can record events via
// RecordNow.
func WithSender(ctx context.Context, s *Sender) context.Context {
	return context.WithValue(ctx, senderKey{}, s)
}

// FromContext returns the Sender attached to ctx, or nil if none was attached.
func FromContext(ctx context.Context) *Sender {
	v := ctx.Value(senderKey{})
	if v == nil {
		return nil
	}
	return v.(*Sender)
}

// DisableTelemetry marks cmd so RecordForSubcommands skips wrapping it. Used
// for commands that should never report their own invocation, such as the
// hidden receive-telemetry command.
func DisableTelemetry(cmd *cobra.Command) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations["telemetry"] = "disabled"
}

// IsTelemetryDisabled reports whether DisableTelemetry was called on cmd.
func IsTelemetryDisabled(cmd *cobra.Command) bool {
	return cmd.Annotations["telemetry"] == "disabled"
}

// RecordForSubcommands wraps every descendant command's RunE so it reports a
// command_invocation event after running, without requiring per-command
// changes.
func RecordForSubcommands(cmd *cobra.Command) {
	for _, c := range cmd.Commands() {
		record(c)
		RecordForSubcommands(c)
	}
}

func record(cmd *cobra.Command) {
	if IsTelemetryDisabled(cmd) || cmd.RunE == nil {
		return
	}

	next := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		start := time.Now()
		runErr := next(cmd, args)
		RecordNow(cmd, runErr, time.Since(start))
		return runErr
	}
}

// RecordNow reports a command_invocation event immediately: the full command
// path, the sorted comma-joined names (never values) of flags the user set,
// the outcome (see the Outcome constants), the wall-clock duration in
// milliseconds, and on failure the Go type of the error. The error's
// message is deliberately never sent: this codebase's error-wrapping
// convention (fmt.Errorf("...: %w", err)) means messages routinely embed
// absolute file paths, which would violate the no-PII guarantee below.
// chunk-cli events are distinguished from circleci-cli events via
// Context.App.Name ("chunk-cli").
func RecordNow(cmd *cobra.Command, err error, duration time.Duration) {
	tc := FromContext(cmd.Context())
	if tc == nil {
		return
	}

	var flags []string
	cmd.Flags().Visit(func(f *pflag.Flag) {
		flags = append(flags, f.Name)
	})
	slices.Sort(flags)

	props := map[string]any{
		"command":     cmd.CommandPath(),
		"flags":       strings.Join(flags, ","),
		"outcome":     outcomeOf(err),
		"duration_ms": duration.Milliseconds(),
	}
	if err != nil {
		props["error_type"] = fmt.Sprintf("%T", err)
	}

	_ = tc.Track("command_invocation", props)
}

// IdentifyUser de-anonymizes the current session: it attaches userID to every
// event the Sender in ctx reports from here on, and sends an identify call so
// Segment joins the anonymous identifiers this install was reporting under to
// that user. Call it right after an authentication flow persists a user ID.
//
// It is best-effort, like the rest of telemetry: a missing Sender or a failed
// enqueue is silently ignored rather than breaking the auth flow the user
// actually asked for.
func IdentifyUser(ctx context.Context, userID uuid.UUID) {
	s := FromContext(ctx)
	if s == nil || userID == uuid.Nil {
		return
	}
	s.SetUserID(userID)
	_ = s.Identify(userID)
}
