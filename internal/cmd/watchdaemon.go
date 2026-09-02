package cmd

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/authprompt"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
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
			// Resolved here, once, rather than on demand inside the daemon:
			// reading the keychain can be slow, and the daemon would otherwise
			// be doing it behind a mutex on the path a hook is blocked on.
			// Failing is not fatal — the daemon still records commands, and the
			// dashboard reports why their output is missing.
			//
			// This process is detached with no terminal, so resolution must not
			// prompt. ResolveCircleCIClient returns ErrNeedsAuth instead of
			// asking; prompting is `chunk watch`'s job, in the parent process.
			var client *circleci.Client
			rc, err := config.ResolveCircleCI(false)
			if err == nil {
				client, err = authprompt.ResolveCircleCIClient(rc, nil)
			}
			return watchd.RunDaemon(cmd.Context(), client, authMessage(err))
		},
	}
}

// authMessage renders a credential-resolution failure for the dashboard. An
// empty logs pane with no explanation sends people hunting the wrong fault.
//
// It lives here rather than in watchd because ErrNeedsAuth is authprompt's, and
// the daemon has no business importing the auth flow to tell one failure from
// another — this is the layer that already holds the error.
func authMessage(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, authprompt.ErrNeedsAuth) {
		return "not authenticated to CircleCI — command output unavailable (run: chunk auth login)"
	}
	return "could not authenticate to CircleCI — command output unavailable: " + err.Error()
}
