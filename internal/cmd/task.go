package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/task"
	"github.com/CircleCI-Public/chunk-cli/internal/tui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "task",
		Short:              "Manage task runs",
		RunE:               groupRunE,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
	}

	cmd.AddCommand(newTaskRunCmd())
	cmd.AddCommand(newTaskConfigCmd())
	cmd.AddCommand(newTaskStatusCmd())
	cmd.AddCommand(newTaskWatchCmd())

	return cmd
}

func newTaskRunCmd() *cobra.Command {
	var definition, prompt, branch string
	var newBranch, noPipelineAsTool, jsonOut bool

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Trigger a task run",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cwd, err := os.Getwd()
			if err != nil {
				return &userError{msg: msgCouldNotDetermineWorkDir, err: fmt.Errorf("get working directory: %w", err)}
			}
			repoRoot, err := gitutil.RepoRoot(cwd)
			if err != nil {
				return &userError{
					msg:        "Not in a git repository.",
					suggestion: suggestionGitRepo,
					err:        fmt.Errorf("not in a git repository: %w", err),
				}
			}

			cfg, err := task.LoadRunConfig(repoRoot)
			if err != nil {
				return err
			}

			io := iostream.FromCmd(cmd)
			insecureStorage := insecureStorageFlag(cmd)
			rc, _ := config.Resolve("", "", insecureStorage)
			client, err := ensureCircleCIClient(cmd.Context(), cmd, rc, io, tui.PromptHidden)
			if err != nil {
				return err
			}

			pipelineAsTool := !noPipelineAsTool

			resp, err := task.TriggerRun(cmd.Context(), client, cfg, task.RunParams{
				Definition:     definition,
				Prompt:         prompt,
				Branch:         branch,
				NewBranch:      newBranch,
				PipelineAsTool: pipelineAsTool,
			})
			if err != nil {
				return err
			}

			if jsonOut {
				return iostream.PrintJSON(io.Out, resp)
			}
			w := 12
			io.Printf("%s %s\n", ui.Label("Run triggered:", w), ui.Green(resp.RunID))
			io.Printf("%s %s\n", ui.Label("Pipeline:", w), resp.PipelineID)
			if webURL, err := task.RunWebURL(cmd.Context(), client, resp.PipelineID); err == nil {
				io.Printf("%s %s\n", ui.Label("URL:", w), webURL)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&definition, "definition", "", "Definition name or UUID")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Prompt text")
	cmd.Flags().StringVar(&branch, "branch", "", "Checkout branch override")
	cmd.Flags().BoolVar(&newBranch, "new-branch", false, "Create a new branch")
	cmd.Flags().BoolVar(&noPipelineAsTool, "no-pipeline-as-tool", false, "Disable running pipeline as a tool")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	_ = cmd.MarkFlagRequired("definition")
	_ = cmd.MarkFlagRequired("prompt")

	return cmd
}

func newTaskConfigCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Set up .chunk/run.json for this repository",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			ctx := cmd.Context()

			// Find git repo root instead of using cwd
			cwd, err := os.Getwd()
			if err != nil {
				return &userError{msg: msgCouldNotDetermineWorkDir, err: fmt.Errorf("get working directory: %w", err)}
			}
			repoRoot, err := gitutil.RepoRoot(cwd)
			if err != nil {
				return &userError{
					msg:        "Not in a git repository.",
					suggestion: suggestionGitRepo,
					err:        fmt.Errorf("not in a git repository: %w", err),
				}
			}

			// Check for existing config and prompt before overwriting
			if task.ConfigExists(repoRoot) && !force {
				if nonInteractive() {
					return errNoForce("overwrite task configuration")
				}
				overwrite, err := tui.Confirm("Overwrite the existing configuration?", false)
				if errors.Is(err, tui.ErrNoTTY) {
					return errNoForce("overwrite task configuration")
				}
				if err != nil || !overwrite {
					io.Println("\nSetup cancelled.")
					return nil
				}
				io.Println("")
			}

			io.Println("")
			io.Println(ui.Bold("Chunk Run Setup"))
			io.Println("")

			insecureStorage := insecureStorageFlag(cmd)
			rc, _ := config.Resolve("", "", insecureStorage)
			client, err := ensureCircleCIClient(ctx, cmd, rc, io, tui.PromptHidden)
			if err != nil {
				return err
			}

			io.ErrPrintln(ui.Dim("Fetching your CircleCI projects..."))

			projects, collabs, err := fetchProjectsAndCollabs(ctx, client)
			if err != nil {
				return err
			}

			prompts := task.Prompts{
				Confirm:    tui.Confirm,
				SelectFrom: tui.SelectFromList,
				PromptText: tui.PromptText,
				Warn:       func(msg string) { io.ErrPrintln(ui.Yellow(msg)) },
			}

			fetchDetail := func(ctx context.Context, slug string) (*circleci.ProjectDetail, error) {
				io.ErrPrintf("%s\n", ui.Dim(fmt.Sprintf("Fetching project details for %s...", slug)))
				return client.GetProjectBySlug(ctx, slug)
			}

			runCfg, err := task.CollectRunConfig(ctx, prompts, projects, collabs, fetchDetail, os.Getenv(config.EnvCircleCIOrgID))
			if errors.Is(err, tui.ErrCancelled) {
				return nil
			}
			if err != nil {
				return err
			}

			if err := task.SaveRunConfig(repoRoot, runCfg); err != nil {
				return err
			}

			io.Println("")
			io.Println(ui.Success("Configuration saved to .chunk/run.json"))
			io.Println("")
			io.Println(ui.Dim("Run a task with: chunk task run --definition <name> --prompt <text>"))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing configuration without confirmation")
	return cmd
}

func newTaskStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status <pipeline-id>",
		Short: "Show the status of a task run pipeline",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			insecureStorage := insecureStorageFlag(cmd)
			rc, _ := config.Resolve("", "", insecureStorage)
			client, err := ensureCircleCIClient(cmd.Context(), cmd, rc, io, tui.PromptHidden)
			if err != nil {
				return err
			}

			status, err := task.FetchStatus(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			if jsonOut {
				return iostream.PrintJSON(io.Out, status)
			}
			printRunStatus(io, status)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func newTaskWatchCmd() *cobra.Command {
	var (
		jsonOut  bool
		interval time.Duration
		timeout  time.Duration
	)
	cmd := &cobra.Command{
		Use:   "watch <pipeline-id>",
		Short: "Watch a task run pipeline until completion",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			insecureStorage := insecureStorageFlag(cmd)
			rc, _ := config.Resolve("", "", insecureStorage)
			client, err := ensureCircleCIClient(cmd.Context(), cmd, rc, io, tui.PromptHidden)
			if err != nil {
				return err
			}

			var lastKey string
			status, err := task.WatchStatus(cmd.Context(), client, args[0], task.WatchOptions{
				Interval: interval,
				Timeout:  timeout,
				OnUpdate: func(s task.RunStatus) {
					key := statusKey(s)
					if key == lastKey {
						return
					}
					lastKey = key
					printRunStatus(io, s)
				},
			})
			if err != nil {
				return &userError{
					msg:        "Task run did not complete successfully.",
					suggestion: "Check the workflow in the CircleCI web UI or run `chunk task status`.",
					err:        err,
				}
			}
			if jsonOut {
				return iostream.PrintJSON(io.Out, status)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output final status as JSON")
	cmd.Flags().DurationVar(&interval, "interval", 5*time.Second, "Polling interval")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "Maximum time to wait")
	return cmd
}

func printRunStatus(io iostream.Streams, status task.RunStatus) {
	w := 12
	io.Printf("%s %s\n", ui.Label("Pipeline:", w), status.PipelineID)
	io.Printf("%s %d\n", ui.Label("Number:", w), status.PipelineNumber)
	io.Printf("%s %s\n", ui.Label("State:", w), status.PipelineState)
	io.Printf("%s %s\n", ui.Label("URL:", w), status.WebURL)
	for _, wf := range status.Workflows {
		line := fmt.Sprintf("%s (%s)", wf.Name, formatWorkflowStatus(wf.Status))
		io.Printf("%s %s\n", ui.Label("Workflow:", w), line)
		for _, job := range wf.Jobs {
			if job.Status == "failed" || job.Status == "error" {
				io.Printf("%s %s (%s)\n", ui.Label("Failed job:", w), job.Name, job.Status)
			}
		}
	}
}

func formatWorkflowStatus(status string) string {
	switch status {
	case "success":
		return ui.Green(status)
	case "failed", "error", "canceled", "unauthorized":
		return ui.Red(status)
	case "running":
		return ui.Yellow(status)
	default:
		return status
	}
}

func statusKey(s task.RunStatus) string {
	var b strings.Builder
	b.WriteString(s.PipelineState)
	for _, wf := range s.Workflows {
		b.WriteByte('|')
		b.WriteString(wf.ID)
		b.WriteByte(':')
		b.WriteString(wf.Status)
		for _, job := range wf.Jobs {
			b.WriteByte(':')
			b.WriteString(job.Name)
			b.WriteByte('=')
			b.WriteString(job.Status)
		}
	}
	return b.String()
}

func fetchProjectsAndCollabs(ctx context.Context, client *circleci.Client) ([]circleci.FollowedProject, []circleci.Collaboration, error) {
	var projects []circleci.FollowedProject
	var collabs []circleci.Collaboration
	var projErr, collabErr error

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		projects, projErr = client.ListFollowedProjects(ctx)
	}()
	go func() {
		defer wg.Done()
		collabs, collabErr = client.ListCollaborations(ctx)
	}()
	wg.Wait()

	if projErr != nil {
		return nil, nil, &userError{
			msg:        "Could not fetch CircleCI projects.",
			suggestion: "Check your token and network connection.",
			err:        fmt.Errorf("fetch projects: %w", projErr),
		}
	}
	if collabErr != nil {
		return nil, nil, &userError{
			msg:        "Could not fetch CircleCI projects.",
			suggestion: "Check your token and network connection.",
			err:        fmt.Errorf("fetch collaborations: %w", collabErr),
		}
	}
	return projects, collabs, nil
}
