package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

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
		Use:   "task",
		Short: "Manage task runs",
	}

	cmd.AddCommand(newTaskRunCmd())
	cmd.AddCommand(newTaskConfigCmd())

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
				return &userError{msg: "Could not determine working directory.", err: fmt.Errorf("get working directory: %w", err)}
			}
			repoRoot, err := gitutil.RepoRoot(cwd)
			if err != nil {
				return &userError{
					msg:        "Not in a git repository.",
					suggestion: "Run this command from inside a git repo.",
					err:        fmt.Errorf("not in a git repository: %w", err),
				}
			}

			cfg, err := task.LoadRunConfig(repoRoot)
			if err != nil {
				return err
			}

			io := iostream.FromCmd(cmd)
			client, err := ensureCircleCIClient(cmd.Context(), io, tui.PromptHidden)
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
				return &userError{msg: "Could not determine working directory.", err: fmt.Errorf("get working directory: %w", err)}
			}
			repoRoot, err := gitutil.RepoRoot(cwd)
			if err != nil {
				return &userError{
					msg:        "Not in a git repository.",
					suggestion: "Run this command from inside a git repo.",
					err:        fmt.Errorf("not in a git repository: %w", err),
				}
			}

			// Check for existing config and prompt before overwriting
			if task.ConfigExists(repoRoot) && !force && !nonInteractive() {
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

			client, err := ensureCircleCIClient(ctx, io, tui.PromptHidden)
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
