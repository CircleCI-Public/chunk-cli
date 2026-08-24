package cmd

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
	"github.com/CircleCI-Public/chunk-cli/internal/upgrade"
)

type updateCheckKey struct{}

// writeKey is the Segment write key for chunk-cli's anonymous usage
// telemetry. Segment write keys are not secret — they only allow sending
// events, not reading data — so checking it into git, as circleci-cli does
// (see internal/cmd/root/root.go there), is safe.
//
// Events sent with this key are tagged as chunk-cli invocations via
// Meta.toContext's App.Name ("chunk-cli") in internal/telemetry/telemetry.go,
// so they stay distinguishable from circleci-cli's own telemetry even if
// both ever land in the same Segment workspace.
const writeKey = "AbgkrgN4cbRhAVEwlzMkHbwvrXnxHh35"

func NewRootCmd(version string) *cobra.Command {
	cobra.EnableTraverseRunHooks = true

	rootCmd := &cobra.Command{
		Use:           "chunk",
		Short:         "Generate AI review context and trigger AI coding tasks",
		Version:       version,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			if err := setupTelemetry(cmd, version); err != nil {
				return err
			}
			startUpdateCheck(cmd)
			return nil
		},
		PersistentPostRunE: func(cmd *cobra.Command, _ []string) error {
			printUpdateNotice(cmd)
			return telemetry.FromContext(cmd.Context()).Close()
		},
	}

	rootCmd.SetHelpTemplate(rootCmd.HelpTemplate() + `
Getting started:
  chunk init                    Initialize project configuration
  chunk auth login              Log in to CircleCI via browser (recommended)
  chunk auth set <provider>     Store a credential manually (circleci, anthropic, github)
  chunk build-prompt            Generate a review prompt from GitHub PR comments
  chunk task config             Set up CircleCI task configuration
  chunk task run --definition <name> --prompt "<task>"
                                Trigger an AI coding task

Environment Variables:
  CIRCLECI_TOKEN                  CircleCI API token (also: CIRCLE_TOKEN)
  ANTHROPIC_API_KEY               Anthropic API key
  GITHUB_TOKEN                    GitHub personal access token
  CIRCLECI_ORG_ID                 CircleCI organization ID
  CODE_REVIEW_CLI_MODEL           Claude model override
  CIRCLECI_BASE_URL               CircleCI API URL [default: https://circleci.com]
  ANTHROPIC_BASE_URL              Anthropic API URL [default: https://api.anthropic.com]
  GITHUB_API_URL                  GitHub API URL [default: https://api.github.com]
  SSH_AUTH_SOCK                   SSH agent socket for sidecar key auth
  NO_COLOR                        Disable colored output
  CI                              Disable interactive prompts (set by most CI systems); also disables telemetry
  CHUNK_NO_TELEMETRY               Disable anonymous usage telemetry (any non-empty value)
  NO_ANALYTICS                    Disable anonymous usage telemetry (any non-empty value)
  DO_NOT_TRACK                    Disable anonymous usage telemetry (any non-empty value)

Configuration:
  ~/.config/chunk/config.json     User credentials and settings ($XDG_CONFIG_HOME/chunk/config.json)
  .chunk/config.json              Project settings (per repository)
  .chunk/run.json                 Task run configuration (chunk task config)
`)

	rootCmd.AddCommand(newInitCmd())
	rootCmd.AddCommand(newAuthCmd())
	rootCmd.AddCommand(newOrgCmd())
	rootCmd.AddCommand(newConfigCmd())
	rootCmd.AddCommand(newBuildPromptCmd())
	rootCmd.AddCommand(newSkillCmd())
	rootCmd.AddCommand(newCompletionCmd())
	rootCmd.AddCommand(newSidecarCmd())
	rootCmd.AddCommand(newTaskCmd())
	rootCmd.AddCommand(newValidateCmd())
	rootCmd.AddCommand(newHookCmd())
	rootCmd.AddCommand(newUpgradeCmd())
	rootCmd.AddCommand(newReceiveTelemetryCmd())
	rootCmd.AddCommand(newWatchCmd())

	rootCmd.AddCommand(newCommandsCmd())

	rootCmd.PersistentFlags().Bool("insecure-storage", false, "do not use the system's secure storage for storing tokens")
	_ = rootCmd.PersistentFlags().MarkHidden("insecure-storage")

	telemetry.RecordForSubcommands(rootCmd)

	return rootCmd
}

// setupTelemetry resolves the user's telemetry preference and attaches a
// telemetry.Sender to cmd's context so RecordNow can report a
// chunk_command_invocation event once the command finishes.
func setupTelemetry(cmd *cobra.Command, version string) error {
	if telemetry.IsTelemetryDisabled(cmd) {
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	optedIn := config.IsTelemetry(cfg)
	// testing.Testing() guards against re-exec'ing os.Executable() as a
	// "receive-telemetry" subprocess when the running binary is a `go test`
	// binary: that binary has no such subcommand, so it silently re-runs its
	// entire test suite instead, which can itself trigger more sends and
	// spawn runaway recursive subprocesses.
	send := optedIn && writeKey != "" && !testing.Testing()

	var instanceID uuid.UUID
	if optedIn {
		instanceID, err = config.EnsureInstanceID()
		if err != nil {
			return err
		}
	}

	executable, err := os.Executable()
	if err != nil {
		executable = "chunk"
	}

	tc, err := telemetry.NewSender(telemetry.Config{
		Send:     send,
		Log:      optedIn && os.Getenv("CHUNK_TELEMETRY_LOG") != "",
		WriteKey: writeKey,
		Binary:   executable,
		Metadata: telemetry.Meta{
			Version:     version,
			InstanceID:  instanceID,
			OS:          runtime.GOOS,
			CodingAgent: telemetry.DetectCodingAgent(),
		},
	})
	if err != nil {
		return err
	}

	cmd.SetContext(telemetry.WithSender(cmd.Context(), tc))
	return nil
}

// noUpdateCheckCommands are commands that must not run the update check.
// Completion helpers run on every TAB press and receive-telemetry is re-execed
// by every chunk invocation, so checking there would burn through GitHub's
// unauthenticated rate limit; upgrade would compare against the version it is
// replacing; and watch renders its own notice in the TUI footer.
var noUpdateCheckCommands = map[string]bool{
	cobra.ShellCompRequestCmd:       true,
	cobra.ShellCompNoDescRequestCmd: true,
	"completion":                    true,
	"receive-telemetry":             true,
	"upgrade":                       true,
	"watch":                         true,
}

// skipUpdateCheck reports whether cmd, or any command it is nested under, is
// in noUpdateCheckCommands.
func skipUpdateCheck(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if noUpdateCheckCommands[c.Name()] {
			return true
		}
	}
	return false
}

// startUpdateCheck launches a background goroutine to check for a newer
// version. The result is sent on a buffered channel stored in the context so
// printUpdateNotice can read it after the command completes.
func startUpdateCheck(cmd *cobra.Command) {
	if skipUpdateCheck(cmd) {
		return
	}
	ch := make(chan string, 1)
	cmd.SetContext(context.WithValue(cmd.Context(), updateCheckKey{}, ch))

	go func() { ch <- upgrade.Check() }()
}

// printUpdateNotice prints a notice to stderr if the background check has
// already found a newer version. It never waits: with a warm cache the check is
// a single file read and has long since finished, and on the once-a-day fetch
// the notice is worth less than the delay it would cost every command. A fetch
// still in flight is simply dropped — it claims the cache window before making
// its request, so the notice lands on a later invocation instead.
func printUpdateNotice(cmd *cobra.Command) {
	ch, ok := cmd.Context().Value(updateCheckKey{}).(chan string)
	if !ok {
		return
	}
	var latest string
	select {
	case latest = <-ch:
	default:
		return
	}
	if latest == "" {
		return
	}

	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "\nA new version of chunk is available: %s\nRun: %s\n", latest, upgrade.SelfUpgradeCommand())
}
