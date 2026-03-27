package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/hook"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

func newHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "hook",
		Short:  "Internal hook plumbing (use 'chunk validate' instead)",
		Hidden: true,
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			initHookLog()
		},
	}

	cmd.AddCommand(newHookEnvCmd())
	cmd.AddCommand(newHookScopeCmd())
	cmd.AddCommand(newHookStateCmd())

	wrapRunE(cmd)

	return cmd
}

// --- env ---

func newHookEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Environment configuration",
	}
	cmd.AddCommand(newHookEnvUpdateCmd())
	return cmd
}

func newHookEnvUpdateCmd() *cobra.Command {
	var (
		profile     string
		envFile     string
		logDir      string
		verbose     bool
		projectRoot string
	)
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update hook environment configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return hook.RunEnvUpdate(hook.EnvUpdateOptions{
				Profile:     profile,
				EnvFile:     envFile,
				LogDir:      logDir,
				Verbose:     verbose,
				ProjectRoot: projectRoot,
			}, iostream.FromCmd(cmd))
		},
	}
	cmd.Flags().StringVar(&profile, "profile", "enable", "Environment profile")
	cmd.Flags().StringVar(&envFile, "env-file", "", "Custom env file path")
	cmd.Flags().StringVar(&logDir, "set-log-dir", "", "Set log directory")
	cmd.Flags().BoolVar(&verbose, "set-verbose", false, "Enable verbose logging")
	cmd.Flags().StringVar(&projectRoot, "set-project-root", "", "Set project root")
	return cmd
}

// --- scope ---

func newHookScopeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scope",
		Short: "Scope management for multi-repo workspaces",
	}
	cmd.AddCommand(newHookScopeActivateCmd())
	cmd.AddCommand(newHookScopeDeactivateCmd())
	return cmd
}

func newHookScopeActivateCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "activate",
		Short: "Activate scope for a project",
		RunE: func(_ *cobra.Command, _ []string) error {
			projectDir := hook.ResolveProject(project)
			return hook.ActivateScope(projectDir, os.Stdin)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project directory")
	return cmd
}

func newHookScopeDeactivateCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "deactivate",
		Short: "Deactivate scope for a project",
		RunE: func(_ *cobra.Command, _ []string) error {
			projectDir := hook.ResolveProject(project)
			return hook.DeactivateScope(projectDir, os.Stdin)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project directory")
	return cmd
}

// --- state ---

func newHookStateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "state",
		Short: "Per-project state management",
	}
	cmd.AddCommand(newHookStateSaveCmd())
	cmd.AddCommand(newHookStateAppendCmd())
	cmd.AddCommand(newHookStateLoadCmd())
	cmd.AddCommand(newHookStateClearCmd())
	return cmd
}

func newHookStateSaveCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "save",
		Short: "Save event state",
		RunE: func(_ *cobra.Command, _ []string) error {
			projectDir := hook.ResolveProject(project)
			cfg := hook.LoadConfig(projectDir)
			return hook.StateSave(cfg.SentinelDir, projectDir, os.Stdin)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project directory")
	return cmd
}

func newHookStateAppendCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "append",
		Short: "Append event state",
		RunE: func(_ *cobra.Command, _ []string) error {
			projectDir := hook.ResolveProject(project)
			cfg := hook.LoadConfig(projectDir)
			return hook.StateAppend(cfg.SentinelDir, projectDir, os.Stdin)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project directory")
	return cmd
}

func newHookStateLoadCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "load [field]",
		Short: "Load state",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir := hook.ResolveProject(project)
			cfg := hook.LoadConfig(projectDir)
			field := ""
			if len(args) > 0 {
				field = args[0]
			}
			return hook.StateLoad(cfg.SentinelDir, projectDir, field, iostream.FromCmd(cmd))
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project directory")
	return cmd
}

func newHookStateClearCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Clear state",
		RunE: func(_ *cobra.Command, _ []string) error {
			projectDir := hook.ResolveProject(project)
			cfg := hook.LoadConfig(projectDir)
			return hook.StateClear(cfg.SentinelDir, projectDir, os.Stdin)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "Project directory")
	return cmd
}
