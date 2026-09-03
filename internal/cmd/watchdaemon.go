package cmd

import (
	"context"
	"io"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/envctx"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
	"github.com/CircleCI-Public/chunk-cli/internal/version"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// newWatchDaemonCmd returns the hidden _daemon subcommand invoked by EnsureRunning
// to start the background watch daemon. It is intentionally hidden from help output.
func newWatchDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:    watchDaemonSubcmd,
		Short:  "Run the watch daemon (internal use only)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return watchd.RunDaemon(cmd.Context(), makeValidateRunner())
		},
	}
}

// makeValidateRunner returns a ValidateRunner that executes validate commands
// in-process by running a fresh cobra root command with the caller's env and
// session ID seeded into the context.
func makeValidateRunner() watchd.ValidateRunner {
	return func(ctx context.Context, args []string, env []string, stdout, stderr io.Writer) int {
		// Seed the context with the caller's session ID before cobra's
		// PersistentPreRunE runs, so IDFromEnv (which reads the daemon's own env)
		// does not overwrite it.
		if id := session.IDFromCtx(ctx); id == "" {
			if id := session.IDFromSlice(env); id != "" {
				ctx = session.WithID(ctx, id)
			}
		}
		ctx = envctx.WithEnv(ctx, env)

		rootCmd := NewRootCmd(version.Value)
		rootCmd.SetOut(stdout)
		rootCmd.SetErr(stderr)
		// Append --no-daemon so the in-process call skips daemon re-delegation.
		rootCmd.SetArgs(append(append([]string(nil), args...), "--no-daemon"))
		if err := rootCmd.ExecuteContext(ctx); err != nil {
			if ec, ok := err.(interface{ ExitCode() int }); ok {
				return ec.ExitCode()
			}
			return 1
		}
		return 0
	}
}
