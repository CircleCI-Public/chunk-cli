package cmd

import (
	"context"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
)

// writeKey is the Segment write key injected at build time via -ldflags.
// Empty by default; telemetry silently uses ModeNOOP when unset.
var writeKey string

func NewRootCmd(version string) *cobra.Command {
	cobra.EnableTraverseRunHooks = true

	telem := &delegatingTelemetry{}

	rootCmd := &cobra.Command{
		Use:           "chunk",
		Short:         "Generate AI review context and trigger AI coding tasks",
		Version:       version,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			noTelemetry, _ := cmd.Flags().GetBool("no-telemetry")
			tc, err := newTelemetryClient(cmd.Context(), version, noTelemetry)
			if err != nil {
				tc, _ = telemetry.New(cmd.Context(), telemetry.Config{Mode: telemetry.ModeNOOP})
			}
			telem.Client = tc
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
  CHUNK_NO_TELEMETRY              Disable telemetry (also: NO_ANALYTICS, DO_NOT_TRACK)

Telemetry:
  chunk collects anonymous usage statistics to help improve the tool.

  What we collect:
    - Which commands are used
    - Whether commands succeed or fail (no error messages)
    - chunk version, OS, and architecture

  What we do NOT collect:
    - Command arguments or flag values
    - File or directory names
    - IP addresses or hostnames
    - Any personally identifiable information

  To disable telemetry:
    Set CHUNK_NO_TELEMETRY=1 in your environment
    Pass --no-telemetry to disable for a single invocation

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

	recordTelemetryForSubcommands(rootCmd, telem)

	rootCmd.PersistentFlags().Bool("insecure-storage", false, "do not use the system's secure storage for storing tokens")
	_ = rootCmd.PersistentFlags().MarkHidden("insecure-storage")
	rootCmd.PersistentFlags().Bool("no-telemetry", false, "Disable telemetry for this invocation")

	return rootCmd
}

// delegatingTelemetry wraps a *telemetry.Client that is populated lazily in
// PersistentPreRunE, allowing subcommand RunE wrappers registered at startup
// to call through to the client once it is available.
type delegatingTelemetry struct {
	*telemetry.Client
}

type tracker interface {
	Track(eventName string, props map[string]any) error
	Close() error
}

// recordTelemetry wraps cmd.RunE to emit a command_invocation event and flush
// the telemetry client after each command. The 500 ms timeout prevents a slow
// Segment endpoint from stalling the CLI.
func recordTelemetry(cmd *cobra.Command, t tracker) {
	if cmd.Annotations["telemetry"] == "disabled" {
		return
	}
	if cmd.RunE == nil {
		return
	}
	original := cmd.RunE
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		runErr := original(cmd, args)

		var flags []string
		cmd.Flags().Visit(func(f *pflag.Flag) {
			flags = append(flags, f.Name)
		})
		slices.Sort(flags)

		_ = t.Track("command_invocation", map[string]any{
			"command": cmd.CommandPath(),
			"flags":   strings.Join(flags, ","),
			"success": runErr == nil,
			"action":  "invoked",
		})

		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = t.Close()
		}()
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
		}

		return runErr
	}
}

func recordTelemetryForSubcommands(cmd *cobra.Command, t tracker) {
	for _, c := range cmd.Commands() {
		recordTelemetry(c, t)
		recordTelemetryForSubcommands(c, t)
	}
}

// newTelemetryClient determines the appropriate mode and constructs the client.
func newTelemetryClient(ctx context.Context, version string, noTelemetryFlag bool) (*telemetry.Client, error) {
	mode := telemetry.ModeSend
	if noTelemetryFlag || telemetryOptedOut() || writeKey == "" {
		mode = telemetry.ModeNOOP
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if cfg.NoTelemetry {
		mode = telemetry.ModeNOOP
	}

	env, err := config.LoadEnv(ctx)
	if err != nil {
		return nil, err
	}

	instanceID := cfg.InstanceID
	freshID := instanceID == ""
	if freshID {
		instanceID = uuid.NewString()
		cfg.InstanceID = instanceID
		_ = config.Save(cfg)
	}

	tc, err := telemetry.New(ctx, telemetry.Config{
		Mode:     mode,
		WriteKey: writeKey,
		User: telemetry.User{
			InstanceID:     instanceID,
			OrganizationID: env.CircleCIOrgID,
			OS:             runtime.GOOS,
			Arch:           runtime.GOARCH,
			Version:        version,
		},
	})
	if err != nil {
		return nil, err
	}
	if freshID && mode == telemetry.ModeSend {
		_ = tc.Identify()
	}
	return tc, nil
}

// telemetryOptedOut returns true if any standard opt-out environment variable is set.
func telemetryOptedOut() bool {
	for _, env := range []string{
		config.EnvChunkNoTelemetry,
		config.EnvNoAnalytics,
		config.EnvDoNotTrack,
		config.EnvCI,
	} {
		if os.Getenv(env) != "" {
			return true
		}
	}
	return false
}
