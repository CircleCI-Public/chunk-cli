package cmd

import (
	"context"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// submitAndStream submits a command to a sidecar, registers it with the watch
// daemon, and streams its output.
//
// The three steps live in one helper because their order is the whole point: the
// command ID has to exist and be registered before any output is consumed, or
// the daemon starts tailing partway through a run it cannot rewind. Splitting
// them across call sites is what lets a path forget the middle one.
//
// Every path that runs a command a developer might later want to read goes
// through here. A command submitted any other way still runs, but it is
// invisible to both `chunk watch` and `chunk sidecar logs` — which reads as
// output being lost rather than never captured.
func submitAndStream(
	ctx context.Context, client *circleci.Client,
	reg watchd.CommandReg, command string, args []string,
	env map[string]string, onOutput circleci.OutputFn,
) (*circleci.ExecResponse, error) {
	commandID, err := client.SubmitExec(ctx, reg.SidecarID, command, args, env)
	if err != nil {
		return nil, err
	}
	reg.CommandID = commandID
	if reg.SubmittedAt.IsZero() {
		reg.SubmittedAt = time.Now()
	}
	watchd.RegisterCommand(reg)
	return client.StreamOutput(ctx, commandID, "", onOutput)
}

// execCommandLabel titles a `sidecar exec` command with what the user typed,
// which is the only description of it that exists — unlike a validate run, there
// is no configured command name to fall back on.
func execCommandLabel(command string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, command)
	parts = append(parts, args...)
	return clampLabel(strings.Join(parts, " "))
}

// clampLabel bounds a pane title to one line of reasonable length.
func clampLabel(label string) string {
	label = strings.TrimSpace(label)
	// Multi-line scripts are titled by their first line; the pane shows the full
	// output anyway, so a header spanning the terminal earns nothing.
	if nl := strings.IndexByte(label, '\n'); nl >= 0 {
		label = strings.TrimSpace(label[:nl])
	}
	// Bounded by runes, not bytes: slicing a byte offset can land inside a
	// multi-byte character and leave the label invalid UTF-8, which renders as a
	// replacement char in the pane title.
	const maxLabel = 120
	if runes := []rune(label); len(runes) > maxLabel {
		label = string(runes[:maxLabel])
	}
	return label
}
