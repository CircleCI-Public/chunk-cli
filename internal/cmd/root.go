package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/spf13/cobra"

	"github.com/smeltdev/smelt/internal/brand"
	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/session"
	"github.com/smeltdev/smelt/internal/smeltd"
	"github.com/smeltdev/smelt/internal/telemetry"
	"github.com/smeltdev/smelt/internal/upgrade"
	"github.com/smeltdev/smelt/internal/version"
)

type updateCheckKey struct{}

func NewRootCmd(version string) *cobra.Command {
	cobra.EnableTraverseRunHooks = true

	rootCmd := &cobra.Command{
		Use:           brand.Command,
		Short:         "Generate AI review context and trigger AI coding tasks",
		Version:       version,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			if isSkippedCommitGate(cmd) {
				return nil // RunE sees the same payload and returns straight away
			}
			if id := session.IDFromEnv(); id != "" && session.IDFromCtx(cmd.Context()) == "" {
				cmd.SetContext(session.WithID(cmd.Context(), id))
			}
			if err := setupTelemetry(cmd, version); err != nil {
				return err
			}
			startUpdateCheck(cmd)
			return maybeAutoLaunchDaemon(cmd)
		},
		// Telemetry is flushed by ExecuteRoot, not here: cobra skips the
		// post-run hooks when RunE returns an error.
		PersistentPostRunE: func(cmd *cobra.Command, _ []string) error {
			printUpdateNotice(cmd)
			return nil
		},
	}
	rootCmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return newUserError(err.Error()).
			withCode("command.invalid_flags").
			withExitCode(ExitBadArgs).
			withoutDetail()
	})

	rootCmd.SetHelpTemplate(rootCmd.HelpTemplate() + `
Getting started:
  smelt init                    Initialize project configuration
  smelt auth login              Log in to CircleCI via browser (recommended)
  smelt auth set <provider>     Store a credential manually (circleci, anthropic, github)
  smelt build-prompt            Generate a review prompt from GitHub PR comments
  smelt task config             Set up CircleCI task configuration
  smelt task run --definition <name> --prompt "<task>"
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
  SMELT_SESSION_ID                Agent session identity; keeps parallel sessions on separate sidecar pools
                                  (read from CLAUDE_CODE_SESSION_ID when unset)
  NO_COLOR                        Disable colored output
  CI                              Disable interactive prompts (set by most CI systems); also disables telemetry
  SMELT_NO_TELEMETRY              Disable usage telemetry (any non-empty value)
  NO_ANALYTICS                    Disable usage telemetry (any non-empty value)
  DO_NOT_TRACK                    Disable usage telemetry (any non-empty value)

Configuration:
  ~/.config/smelt/config.json     User credentials and settings ($XDG_CONFIG_HOME/smelt/config.json)
  .smelt/config.json              Project settings (per repository)
  .smelt/run.json                 Task run configuration (smelt task config)
`)

	rootCmd.AddCommand(newInitCmd())
	rootCmd.AddCommand(newAuthCmd())
	rootCmd.AddCommand(newRefreshCmd("refresh [provider]"))
	rootCmd.AddCommand(newOrgCmd())
	rootCmd.AddCommand(newConfigCmd())
	rootCmd.AddCommand(newBuildPromptCmd())
	rootCmd.AddCommand(newSkillCmd())
	rootCmd.AddCommand(newCompletionCmd())
	rootCmd.AddCommand(newManCmd())
	rootCmd.AddCommand(newSidecarCmd())
	rootCmd.AddCommand(newPruneCmd())
	rootCmd.AddCommand(newTaskCmd())
	rootCmd.AddCommand(newValidateCmd())
	rootCmd.AddCommand(newMutateCmd())
	rootCmd.AddCommand(newReviewCmd())
	rootCmd.AddCommand(newFactoryCmd())
	rootCmd.AddCommand(newHarnessCmd())
	rootCmd.AddCommand(newHookCmd())
	rootCmd.AddCommand(newConflictsCmd())
	rootCmd.AddCommand(newUpgradeCmd())
	rootCmd.AddCommand(newReceiveTelemetryCmd())
	rootCmd.AddCommand(newWatchCmd())
	rootCmd.AddCommand(newDaemonCmd())

	rootCmd.AddCommand(newCommandsCmd())
	rootCmd.InitDefaultHelpCmd()
	rootCmd.SetHelpTemplate(rootCmd.HelpTemplate() + pluginsHelp(rootCmd, os.Getenv("PATH")))

	rootCmd.PersistentFlags().Bool("insecure-storage", false, "do not use the system's secure storage for storing tokens")
	_ = rootCmd.PersistentFlags().MarkHidden("insecure-storage")

	rootCmd.PersistentFlags().Bool("daemon", false, "auto-launch the smelt daemon for this run even if autoLaunchDaemon is disabled")
	rootCmd.PersistentFlags().Bool("no-daemon", false, "skip the smelt daemon for this run even if autoLaunchDaemon is enabled")

	telemetry.RecordForSubcommands(rootCmd)

	return rootCmd
}

// agentExtra returns a non-nil Extra map only when a coding agent is detected,
// so that no "agent" trait is forwarded for users not running inside one.
func agentExtra() map[string]any {
	agent := telemetry.DetectCodingAgent()
	if agent == "" {
		return nil
	}
	return map[string]any{"agent": agent}
}

// ExecuteRoot runs rootCmd and flushes buffered telemetry whether or not the
// command succeeds.
//
// The flush cannot live in PersistentPostRunE: cobra returns as soon as RunE
// reports an error and never reaches its post-run hooks, so every failed
// invocation would drop the events it buffered. A lost command_invocation is
// only a missing row, but an auth flow prompted mid-command also buffers the
// identify that joins the user's pre-auth events to them — and that is sent
// once, on the run that logs in. If the command then fails, nothing re-sends
// it and the anonymous half of the journey stays orphaned for good.
func ExecuteRoot(rootCmd *cobra.Command) error {
	args := os.Args[1:]
	if ran, err := dispatchPlugin(rootCmd, args, os.Getenv("PATH")); ran {
		return err
	}
	// A command that fails before its pre-run hook never started: cobra found
	// the command line wrong.
	started := false
	preRun := rootCmd.PersistentPreRunE
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, a []string) error {
		started = true
		return preRun(cmd, a)
	}
	// The sender lives on the context of the command cobra actually resolved,
	// which ExecuteC returns; rootCmd's own context does not carry it.
	executed, err := rootCmd.ExecuteC()
	if executed == nil {
		executed = rootCmd
	}
	if ctx := executed.Context(); ctx != nil {
		_ = telemetry.FromContext(ctx).Close()
	}
	var ue *userError
	if err != nil && !started && !errors.As(err, &ue) {
		err = usageError(err, args)
	}
	return err
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

	optedIn := config.IsTelemetry(cfg)
	// testing.Testing() guards against re-exec'ing os.Executable() as a
	// "receive-telemetry" subprocess when the running binary is a `go test`
	// binary: that binary has no such subcommand, so it silently re-runs its
	// entire test suite instead, which can itself trigger more sends and
	// spawn runaway recursive subprocesses.
	send := optedIn && telemetry.WriteKey != "" && !testing.Testing()

	var instanceID uuid.UUID
	var sessionTrackingID uuid.UUID
	if optedIn {
		instanceID, err = config.EnsureInstanceID()
		if err != nil {
			return err
		}
		if sid := session.IDFromEnv(); sid != "" {
			sessionTrackingID = config.SessionTrackingID(sid)
		}
	}

	executable, err := os.Executable()
	if err != nil {
		executable = brand.Command
	}

	// Only gather host info when telemetry will actually be sent. Skipping
	// it for telemetry-disabled commands (e.g. completion generation) avoids
	// gopsutil's ioreg lookup, which fails under a restricted PATH such as
	// Homebrew's sanitized completion-generation environment.
	var osName, osVersion, kernelArch, platformFamily string
	if optedIn && !telemetry.IsTelemetryDisabled(cmd) {
		if info, _ := host.InfoWithContext(cmd.Context()); info != nil {
			osName, osVersion, kernelArch, platformFamily = info.OS, info.PlatformVersion, info.KernelArch, info.PlatformFamily
		}
	}

	tc, err := telemetry.NewSender(telemetry.Config{
		Send:     send,
		Log:      optedIn && os.Getenv(brand.Env("TELEMETRY_LOG")) != "",
		WriteKey: telemetry.WriteKey,
		Binary:   executable,
		Metadata: telemetry.Meta{
			Version:           version,
			InstanceID:        instanceID,
			SessionTrackingID: sessionTrackingID,
			UserID:            config.GetUserID(),
			OSName:            osName,
			OSVersion:         osVersion,
			KernelArch:        kernelArch,
			PlatformFamily:    platformFamily,
			Extra:             agentExtra(),
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
// by every smelt invocation, so checking there would burn through GitHub's
// unauthenticated rate limit; upgrade would compare against the version it is
// replacing; watch renders its own notice in the TUI footer; and the daemon
// has no terminal to print one to.
var noUpdateCheckCommands = map[string]bool{
	cobra.ShellCompRequestCmd:       true,
	cobra.ShellCompNoDescRequestCmd: true,
	"completion":                    true,
	"receive-telemetry":             true,
	"upgrade":                       true,
	watchCmdName:                    true,
	smeltd.DaemonSubcommand:         true,
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

// noAutoLaunchCommands lists commands for which the auto-launch daemon check is
// skipped: completion helpers (called on every TAB press), the daemon itself,
// and commands that manage the daemon directly (watch starts it on its own).
var noAutoLaunchCommands = map[string]bool{
	cobra.ShellCompRequestCmd:       true,
	cobra.ShellCompNoDescRequestCmd: true,
	"completion":                    true,
	"receive-telemetry":             true,
	watchCmdName:                    true,
	smeltd.DaemonSubcommand:         true,
}

// shouldAutoLaunch reports whether the smelt daemon should be auto-launched
// for cmd. It checks (in order): the skip list, --no-daemon, --daemon, and
// finally the autoLaunchDaemon user setting.
func shouldAutoLaunch(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if noAutoLaunchCommands[c.Name()] {
			return false
		}
	}
	if noDaemon, err := cmd.Flags().GetBool("no-daemon"); err == nil && noDaemon {
		return false
	}
	if daemon, err := cmd.Flags().GetBool("daemon"); err == nil && daemon {
		return true
	}
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	return cfg.AutoLaunchDaemon
}

// maybeAutoLaunchDaemon starts the smelt daemon if autoLaunchDaemon is
// configured (or --daemon is passed) and the command is not excluded. When
// --daemon was passed explicitly, a startup failure is returned so the user
// sees it; otherwise errors are silently ignored because the daemon is an
// optimization and every command works correctly without it.
func maybeAutoLaunchDaemon(cmd *cobra.Command) error {
	if !shouldAutoLaunch(cmd) {
		return nil
	}
	err := smeltd.EnsureRunning()
	if err == nil {
		return nil
	}
	if daemon, flagErr := cmd.Flags().GetBool("daemon"); flagErr == nil && daemon {
		return fmt.Errorf("start smelt daemon: %w", err)
	}
	return nil
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

	// Release tags carry a "v" and the ldflags version does not, so the running
	// version gets one to read as the same kind of thing as the tag beside it.
	current := "v" + strings.TrimPrefix(version.Value, "v")
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "\nA new version of smelt is available: %s → %s\nRun: %s\nWhat's new: %s\n",
		current, latest, upgrade.SelfUpgradeCommand(), upgrade.ReleaseURL(latest))
}
