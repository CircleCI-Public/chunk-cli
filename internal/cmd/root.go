package cmd

import (
	"context"
	"runtime"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
)

// writeKey is the Segment write key injected at build time via -ldflags.
// Empty by default; telemetry is silently disabled when unset.
var writeKey string

type telemetryKey struct{}

func withTelemetryClient(ctx context.Context, c *telemetry.Client) context.Context {
	return context.WithValue(ctx, telemetryKey{}, c)
}

func telemetryClientFromContext(ctx context.Context) *telemetry.Client {
	c, _ := ctx.Value(telemetryKey{}).(*telemetry.Client)
	return c
}

func NewRootCmd(version string) *cobra.Command {
	cobra.EnableTraverseRunHooks = true

	rootCmd := &cobra.Command{
		Use:           "chunk",
		Short:         "Generate AI review context and trigger AI coding tasks",
		Version:       version,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			tc, err := newTelemetryClient(version)
			if err == nil {
				_ = tc.Identify()
				cmd.SetContext(withTelemetryClient(cmd.Context(), tc))
			}
			return nil
		},
		PersistentPostRunE: func(cmd *cobra.Command, _ []string) error {
			if tc := telemetryClientFromContext(cmd.Context()); tc != nil {
				_ = tc.Close()
			}
			return nil
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
  CI                              Disable interactive prompts (set by most CI systems)

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

	rootCmd.AddCommand(newCommandsCmd())

	rootCmd.PersistentFlags().Bool("insecure-storage", false, "do not use the system's secure storage for storing tokens")
	_ = rootCmd.PersistentFlags().MarkHidden("insecure-storage")

	return rootCmd
}

// newTelemetryClient loads or generates a stable instance ID, then constructs
// the Segment-backed client. Returns nil (not an error) when telemetry is disabled.
func newTelemetryClient(version string) (*telemetry.Client, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	instanceID, parseErr := uuid.Parse(cfg.InstanceID)
	if parseErr != nil {
		instanceID = uuid.New()
		cfg.InstanceID = instanceID.String()
		_ = config.Save(cfg) // best-effort; failures are non-fatal
	}

	return telemetry.New(telemetry.Config{
		WriteKey: writeKey,
		User: telemetry.User{
			InstanceID: instanceID,
			OS:         runtime.GOOS,
			Version:    version,
		},
	})
}

// trackRunE wraps a cobra RunE to emit a "command" telemetry event on completion.
func trackRunE(name string, fn func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		err := fn(cmd, args)
		_ = telemetryClientFromContext(cmd.Context()).Track("command", map[string]any{
			"command": name,
			"success": err == nil,
		})
		return err
	}
}
