package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/task"
)

func newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Manage task runs",
	}

	cmd.AddCommand(newTaskRunCmd())

	return cmd
}

func newTaskRunCmd() *cobra.Command {
	var definition, prompt, branch string
	var newBranch, noPipelineAsTool bool

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Trigger a task run",
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := circleci.NewClient()
			if err != nil {
				return err
			}

			workDir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}

			cfg, err := task.LoadRunConfig(workDir)
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

			fmt.Printf("Run triggered: %s\n", resp.RunID)
			fmt.Printf("Pipeline: %s\n", resp.PipelineID)
			return nil
		},
	}

	cmd.Flags().StringVar(&definition, "definition", "", "Definition name or UUID")
	cmd.Flags().StringVar(&prompt, "prompt", "", "Prompt text")
	cmd.Flags().StringVar(&branch, "branch", "", "Checkout branch override")
	cmd.Flags().BoolVar(&newBranch, "new-branch", false, "Create a new branch")
	cmd.Flags().BoolVar(&noPipelineAsTool, "no-pipeline-as-tool", false, "Disable running pipeline as a tool")

	_ = cmd.MarkFlagRequired("definition")
	_ = cmd.MarkFlagRequired("prompt")

	return cmd
}
