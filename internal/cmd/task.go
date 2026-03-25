package cmd

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
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

			io := iostream.FromCmd(cmd)
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

	_ = cmd.MarkFlagRequired("definition")
	_ = cmd.MarkFlagRequired("prompt")

	return cmd
}

func newTaskConfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Set up .chunk/run.json for this repository",
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			ctx := cmd.Context()

			client, err := circleci.NewClient()
			if err != nil {
				return err
			}

			io.Println("")
			io.Println(ui.Bold("Chunk Run Setup"))
			io.Println("")

			io.ErrPrintln(ui.Dim("Fetching your CircleCI projects..."))

			// Fetch projects and collaborations in parallel
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
				return fmt.Errorf("fetch projects: %w", projErr)
			}
			if collabErr != nil {
				return fmt.Errorf("fetch collaborations: %w", collabErr)
			}

			var orgID, projectID, orgType string

			// Build project selection list
			items := make([]string, 0, len(projects)+1)
			for _, p := range projects {
				items = append(items, fmt.Sprintf("%s/%s", p.Username, p.Reponame))
			}
			items = append(items, "Enter manually")

			idx, err := tui.SelectFromList("Select a project:", items)
			if err != nil {
				return nil
			}

			if idx < len(projects) {
				// Selected a project from the list
				p := projects[idx]
				vcsPrefix := "gh"
				if strings.Contains(p.VcsType, "bitbucket") {
					vcsPrefix = "bb"
				}
				slug := fmt.Sprintf("%s/%s/%s", vcsPrefix, p.Username, p.Reponame)

				io.ErrPrintf("%s\n", ui.Dim(fmt.Sprintf("Fetching project details for %s...", slug)))
				detail, err := client.GetProjectBySlug(ctx, slug)
				if err != nil {
					return fmt.Errorf("fetch project details: %w", err)
				}

				projectID = detail.ID
				orgID = detail.OrgID
				orgType = "github"
				if strings.Contains(p.VcsType, "bitbucket") {
					orgType = "bitbucket"
				}
			} else {
				// Manual entry
				if len(collabs) == 0 {
					return fmt.Errorf("no organizations found")
				}

				orgItems := make([]string, len(collabs))
				for i, c := range collabs {
					orgItems[i] = c.Name
				}

				orgIdx, err := tui.SelectFromList("Select your organization:", orgItems)
				if err != nil {
					return nil
				}

				orgID = collabs[orgIdx].ID
				orgType = collabs[orgIdx].VcsType

				projectID, err = tui.PromptText("Project ID (UUID)", "")
				if err != nil {
					return nil
				}
				if projectID == "" {
					return fmt.Errorf("project ID is required")
				}
			}

			// Collect definitions
			definitions := make(map[string]task.RunDefinition)

			for {
				name, err := tui.PromptText("Definition name (e.g. dev, prod)", "")
				if err != nil {
					return nil
				}
				if name == "" {
					return fmt.Errorf("definition name is required")
				}

				defID, err := tui.PromptText("Definition ID (UUID)", "")
				if err != nil {
					return nil
				}
				if defID == "" {
					return fmt.Errorf("definition ID is required")
				}

				defaultBranch, err := tui.PromptText("Default branch", "main")
				if err != nil {
					return nil
				}

				envID, err := tui.PromptText("Environment ID (optional UUID)", "")
				if err != nil {
					return nil
				}

				def := task.RunDefinition{
					DefinitionID:  defID,
					DefaultBranch: defaultBranch,
				}
				if envID != "" {
					def.ChunkEnvironmentID = &envID
				}

				definitions[name] = def

				more, err := tui.Confirm("Add another definition?", false)
				if err != nil || !more {
					break
				}
			}

			workDir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}

			runCfg := &task.RunConfig{
				OrgID:       orgID,
				ProjectID:   projectID,
				OrgType:     orgType,
				Definitions: definitions,
			}

			if err := task.SaveRunConfig(workDir, runCfg); err != nil {
				return err
			}

			io.Println("")
			io.Println(ui.Success("Configuration saved to .chunk/run.json"))
			io.Println("")
			io.Println(ui.Dim("Run a task with: chunk task run --definition <name> --prompt <text>"))
			return nil
		},
	}
}
