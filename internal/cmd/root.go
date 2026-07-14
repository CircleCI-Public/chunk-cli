package cmd

import (
	"os"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
)

// writeKey is the Segment write key for chunk-cli, injected at build time via
// -ldflags in .goreleaser.yaml (see the SEGMENT_WRITE_KEY release env var).
// It is empty in dev/local builds, which disables sending telemetry
// regardless of the user's preference — so no events are ever sent from a
// `task build` or `go run .` binary.
var writeKey string

func NewRootCmd(version string) *cobra.Command {
	cobra.EnableTraverseRunHooks = true

	rootCmd := &cobra.Command{
		Use:           "chunk",
		Short:         "Generate AI review context and trigger AI coding tasks",
		Version:       version,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			return setupTelemetry(cmd, version)
		},
		PersistentPostRunE: func(cmd *cobra.Command, _ []string) error {
			return telemetry.FromContext(cmd.Context()).Close()
		},
	}

	rootCmd.SetHelpTemplate(rootCmd.HelpTemplate() + `
Getting started:
  chunk init                    Initialize project configuration
  chunk auth set <provider>     Store credentials (CircleCI token, Anthropic API key)
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

	rootCmd.AddCommand(newCommandsCmd())

	rootCmd.PersistentFlags().Bool("insecure-storage", false, "do not use the system's secure storage for storing tokens")
	_ = rootCmd.PersistentFlags().MarkHidden("insecure-storage")

	telemetry.RecordForSubcommands(rootCmd)

	return rootCmd
}

// setupTelemetry resolves the user's telemetry preference and attaches a
// telemetry.Sender to cmd's context so RecordNow can report a
// command_invocation event once the command finishes.
func setupTelemetry(cmd *cobra.Command, version string) error {
	if telemetry.IsTelemetryDisabled(cmd) {
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	optedIn := config.IsTelemetryEnabled(cfg)
	send := optedIn && writeKey != ""

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
			Version:    version,
			InstanceID: instanceID,
		},
	})
	if err != nil {
		return err
	}

	cmd.SetContext(telemetry.WithSender(cmd.Context(), tc))
	return nil
}
