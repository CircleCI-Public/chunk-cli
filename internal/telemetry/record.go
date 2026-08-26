package telemetry

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

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
// the outcome ("success" or "failure"), the wall-clock duration in
// milliseconds, and — on failure — the Go type and message of the error.
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

	outcome := "success"
	if err != nil {
		outcome = "failure"
	}

	props := map[string]any{
		"command":     cmd.CommandPath(),
		"flags":       strings.Join(flags, ","),
		"outcome":     outcome,
		"duration_ms": duration.Milliseconds(),
	}
	if err != nil {
		props["error_type"] = fmt.Sprintf("%T", err)
		props["error_message"] = err.Error()
	}

	_ = tc.Track("command_invocation", props)
}
