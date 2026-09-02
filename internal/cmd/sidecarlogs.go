package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// newSidecarLogsCmd prints a remote command's output.
//
// This is the non-TUI door onto the same data `chunk watch` shows, which matters
// because the most common reader here has no terminal at all: an agent that just
// ran validate and wants to know why it failed can pipe this, where it cannot
// drive a dashboard.
//
// It prefers the watch daemon's buffer and falls back to the API, so it works
// whether or not a daemon is running.
func newSidecarLogsCmd() *cobra.Command {
	var follow bool

	cmd := &cobra.Command{
		Use:   "logs <command-id>",
		Short: "Print the output of a command that ran on a sidecar",
		Long: "Print the output of a command that ran on a sidecar.\n\n" +
			"Command IDs appear in the chunk watch dashboard. Output is read from the\n" +
			"watch daemon's buffer when it has it, and from the CircleCI API otherwise.\n\n" +
			"Exits non-zero only when reading failed. A command that itself exited\n" +
			"non-zero is reported on stderr as \"exit status N\" and does not change this\n" +
			"command's own status, so a failing remote command is not a failing read.",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			commandID := args[0]
			io := iostream.FromCmd(cmd)

			// The daemon first: it needs no network round trip and it holds output
			// for commands whose submitting process has long since exited.
			if chunk, err := watchd.FetchOutput(commandID, 0); err == nil && chunk.Found {
				if chunk.Truncated {
					io.ErrPrintln("warning: earlier output was dropped from the buffer")
				}
				_, _ = io.Out.Write(chunk.Data)
				if chunk.Error != "" {
					// Output may be short because streaming broke, not because the
					// command was quiet. Say which.
					io.ErrPrintf("warning: output stream ended early: %s\n", chunk.Error)
				}
				if !chunk.Running || !follow {
					return reportExitCode(chunk.ExitCode, io)
				}
				return followFromDaemon(cmd, commandID, chunk.NextOffset, io)
			}

			// No daemon, or it has forgotten this command: stream from the API.
			insecureStorage := insecureStorageFlag(cmd)
			rc, err := config.Resolve("", "", insecureStorage)
			if err != nil {
				return fmt.Errorf("resolve config: %w", err)
			}
			client, err := ensureCircleCIClient(cmd.Context(), cmd, rc, io, tui.PromptHidden)
			if err != nil {
				return err
			}
			result, err := client.StreamOutput(cmd.Context(), commandID, "", func(_ string, data []byte) {
				_, _ = io.Out.Write(data)
			})
			if err != nil {
				if authErr := notAuthorized("read command output", rc.CircleCITokenSource, err); authErr != nil {
					return authErr
				}
				return &userError{
					msg:        fmt.Sprintf("Could not read output for command %s.", commandID),
					suggestion: "Check the command ID in the chunk watch dashboard.",
					err:        err,
				}
			}
			return reportExitCode(&result.ExitCode, io)
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Keep printing output until the command exits")
	return cmd
}

// followFromDaemon keeps reading a running command's output from the daemon until
// it exits or the context is cancelled.
func followFromDaemon(cmd *cobra.Command, commandID string, offset int64, io iostream.Streams) error {
	ctx := cmd.Context()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(followInterval):
		}
		chunk, err := watchd.FetchOutput(commandID, offset)
		if err != nil {
			return &userError{msg: "Lost contact with the watch daemon while following output.", err: err}
		}
		if !chunk.Found {
			// The daemon evicted the command mid-follow. Report what happened
			// rather than exiting 0 as though the command had passed.
			return &userError{
				msg:    fmt.Sprintf("The watch daemon dropped output for command %s while following it.", commandID),
				errMsg: "command output evicted",
			}
		}
		_, _ = io.Out.Write(chunk.Data)
		offset = chunk.NextOffset
		if !chunk.Running {
			// Same reasoning as the non-follow path: a stream that broke leaves no
			// exit code, so reporting only the code would end a truncated follow
			// silently at exit 0, as though the command had simply finished.
			if chunk.Error != "" {
				io.ErrPrintf("warning: output stream ended early: %s\n", chunk.Error)
			}
			return reportExitCode(chunk.ExitCode, io)
		}
	}
}

// followInterval is how often --follow asks the daemon for more output. It polls
// a local socket, so this can be brisk without costing anything remote.
const followInterval = 200 * time.Millisecond

// reportExitCode notes the remote command's status on stderr.
//
// It deliberately does not make `logs` exit non-zero: this command succeeded if
// it printed what it was asked for, and conflating "the command I am reading
// about failed" with "reading failed" leaves the exit status meaning nothing to a
// caller. stdout therefore stays pure output for piping, and the status goes to
// stderr where a human still sees it. A nil code means the command never
// terminated as far as the reader could tell, which is not a failure either.
func reportExitCode(code *int, io iostream.Streams) error {
	if code == nil || *code == 0 {
		return nil
	}
	io.ErrPrintf("exit status %d\n", *code)
	return nil
}
