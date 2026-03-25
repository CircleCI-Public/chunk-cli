package cmd

import (
	"os"

	"github.com/CircleCI-Public/chunk-cli/internal/hook"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/spf13/cobra"
)

// readStdinEvent reads and parses the stdin JSON event for hook commands.
func readStdinEvent() map[string]interface{} {
	event, err := hook.ReadStdinJSON(os.Stdin)
	if err != nil {
		return map[string]interface{}{}
	}
	return event
}

func newHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Configure AI coding agent lifecycle hooks",
	}

	cmd.AddCommand(newHookRepoCmd())
	cmd.AddCommand(newHookSetupCmd())
	cmd.AddCommand(newHookEnvCmd())
	cmd.AddCommand(newHookScopeCmd())
	cmd.AddCommand(newHookStateCmd())
	cmd.AddCommand(newHookExecCmd())
	cmd.AddCommand(newHookTaskCmd())
	cmd.AddCommand(newHookSyncCmd())

	return cmd
}

// --- repo ---

func newHookRepoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Repository configuration",
	}
	cmd.AddCommand(newHookRepoInitCmd())
	return cmd
}

func newHookRepoInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Initialize hook configuration in a repository",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			return hook.RunRepoInit(dir, force, iostream.FromCmd(cmd))
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing files")
	return cmd
}

// --- setup ---

func newHookSetupCmd() *cobra.Command {
	var (
		profile string
		skipEnv bool
		force   bool
		envFile string
	)
	cmd := &cobra.Command{
		Use:   "setup [dir]",
		Short: "One-shot hook setup (env + repo init)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			return hook.RunSetup(dir, profile, force, skipEnv, envFile, iostream.FromCmd(cmd))
		},
	}
	cmd.Flags().StringVar(&profile, "profile", "enable", "Environment profile")
	cmd.Flags().BoolVar(&skipEnv, "skip-env", false, "Skip env file creation")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing files")
	cmd.Flags().StringVar(&envFile, "env-file", "", "Custom env file path")
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

// --- exec ---

func newHookExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec",
		Short: "Execute and check commands",
	}
	cmd.AddCommand(newHookExecRunCmd())
	cmd.AddCommand(newHookExecCheckCmd())
	return cmd
}

func newHookExecRunCmd() *cobra.Command {
	var flags hook.ExecRunFlags
	cmd := &cobra.Command{
		Use:   "run <name>",
		Short: "Run a configured command",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags.Name = args[0]
			project, _ := cmd.Flags().GetString("project")
			projectDir := hook.ResolveProject(project)
			cfg := hook.LoadConfig(projectDir)
			return hook.RunExecRun(cfg, flags)
		},
	}
	cmd.Flags().StringVar(&flags.Cmd, "cmd", "", "Command override")
	cmd.Flags().IntVar(&flags.Timeout, "timeout", 0, "Timeout in seconds")
	cmd.Flags().StringVar(&flags.FileExt, "file-ext", "", "File extension filter")
	cmd.Flags().BoolVar(&flags.Staged, "staged", false, "Only staged files")
	cmd.Flags().BoolVar(&flags.Always, "always", false, "Run even without changes")
	cmd.Flags().BoolVar(&flags.NoCheck, "no-check", false, "Save result, skip check")
	cmd.Flags().StringVar(&flags.On, "on", "", "Trigger group name")
	cmd.Flags().StringVar(&flags.Trigger, "trigger", "", "Inline trigger pattern")
	cmd.Flags().IntVar(&flags.Limit, "limit", 0, "Max consecutive blocks")
	cmd.Flags().StringVar(&flags.Matcher, "matcher", "", "Tool-name regex filter")
	cmd.Flags().String("project", "", "Project directory")
	return cmd
}

func newHookExecCheckCmd() *cobra.Command {
	var flags hook.ExecCheckFlags
	cmd := &cobra.Command{
		Use:   "check <name>",
		Short: "Check a saved command result",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags.Name = args[0]
			project, _ := cmd.Flags().GetString("project")
			projectDir := hook.ResolveProject(project)
			cfg := hook.LoadConfig(projectDir)
			event := readStdinEvent()
			return hook.RunExecCheck(cfg, flags, event)
		},
	}
	cmd.Flags().IntVar(&flags.Timeout, "timeout", 0, "Timeout in seconds")
	cmd.Flags().StringVar(&flags.FileExt, "file-ext", "", "File extension filter")
	cmd.Flags().BoolVar(&flags.Staged, "staged", false, "Only staged files")
	cmd.Flags().BoolVar(&flags.Always, "always", false, "Run even without changes")
	cmd.Flags().StringVar(&flags.On, "on", "", "Trigger group name")
	cmd.Flags().StringVar(&flags.Trigger, "trigger", "", "Inline trigger pattern")
	cmd.Flags().IntVar(&flags.Limit, "limit", 0, "Max consecutive blocks")
	cmd.Flags().StringVar(&flags.Matcher, "matcher", "", "Tool-name regex filter")
	cmd.Flags().StringVar(&flags.Cmd, "cmd", "", "Command override")
	cmd.Flags().String("project", "", "Project directory")
	return cmd
}

// --- task ---

func newHookTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Task management",
	}
	cmd.AddCommand(newHookTaskCheckCmd())
	return cmd
}

func newHookTaskCheckCmd() *cobra.Command {
	var flags hook.TaskCheckFlags
	cmd := &cobra.Command{
		Use:   "check <name>",
		Short: "Check a task result",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags.Name = args[0]
			project, _ := cmd.Flags().GetString("project")
			projectDir := hook.ResolveProject(project)
			cfg := hook.LoadConfig(projectDir)
			event := readStdinEvent()
			return hook.RunTaskCheck(cfg, flags, event)
		},
	}
	cmd.Flags().StringVar(&flags.Instructions, "instructions", "", "Instructions file")
	cmd.Flags().StringVar(&flags.Schema, "schema", "", "Schema file")
	cmd.Flags().BoolVar(&flags.Always, "always", false, "Run even without changes")
	cmd.Flags().BoolVar(&flags.Staged, "staged", false, "Only staged files")
	cmd.Flags().StringVar(&flags.On, "on", "", "Trigger group name")
	cmd.Flags().StringVar(&flags.Trigger, "trigger", "", "Inline trigger pattern")
	cmd.Flags().StringVar(&flags.Matcher, "matcher", "", "Tool-name regex filter")
	cmd.Flags().IntVar(&flags.Limit, "limit", 0, "Max consecutive blocks")
	cmd.Flags().String("project", "", "Project directory")
	return cmd
}

// --- sync ---

func newHookSyncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Grouped sequential checks",
	}
	cmd.AddCommand(newHookSyncCheckCmd())
	return cmd
}

func newHookSyncCheckCmd() *cobra.Command {
	var (
		on      string
		trigger string
		matcher string
		limit   int
		staged  bool
		always  bool
		onFail  string
		bail    bool
		project string
	)
	cmd := &cobra.Command{
		Use:   "check <specs...>",
		Short: "Check a group of commands",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			specs, err := hook.ParseSpecs(args)
			if err != nil {
				return err
			}
			projectDir := hook.ResolveProject(project)
			cfg := hook.LoadConfig(projectDir)
			event := readStdinEvent()
			return hook.RunSyncCheck(cfg, hook.SyncCheckFlags{
				Specs:   specs,
				On:      on,
				Trigger: trigger,
				Matcher: matcher,
				Limit:   limit,
				Staged:  staged,
				Always:  always,
				OnFail:  onFail,
				Bail:    bail,
			}, event)
		},
	}
	cmd.Flags().StringVar(&on, "on", "", "Trigger group name")
	cmd.Flags().StringVar(&trigger, "trigger", "", "Inline trigger pattern")
	cmd.Flags().StringVar(&matcher, "matcher", "", "Tool-name regex filter")
	cmd.Flags().IntVar(&limit, "limit", 0, "Max consecutive blocks")
	cmd.Flags().BoolVar(&staged, "staged", false, "Only staged files")
	cmd.Flags().BoolVar(&always, "always", false, "Run even without changes")
	cmd.Flags().StringVar(&onFail, "on-fail", "restart", "Failure strategy")
	cmd.Flags().BoolVar(&bail, "bail", false, "Stop at first failure")
	cmd.Flags().StringVar(&project, "project", "", "Project directory")
	return cmd
}
