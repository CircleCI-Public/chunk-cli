package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/mutate"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newMutateCmd() *cobra.Command {
	var outputFmt string
	var parallel int
	var testCmdFlag string
	var maxMutations int
	var destroyPool bool
	var testTimeout time.Duration
	var orgID string

	cmd := &cobra.Command{
		Use:   "mutate [path]",
		Short: "Find test coverage gaps via mutation testing",
		Long: `Enumerate candidate mutations in production code and validate them against
the test suite using chunk sidecars. Survivors — mutations the suite fails to
catch — reveal real coverage gaps.

Without --parallel, prints the mutation inventory and exits.
With --parallel N, creates a pool of N sidecars and runs each mutation
against the test suite in parallel.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			workDir, err := os.Getwd()
			if err != nil {
				return err
			}

			cfg, err := config.LoadProjectConfig(workDir)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			stack := mutate.DetectedStack(workDir, cfg)
			if stack == "" {
				return &userError{
					msg:    "Could not detect project language.",
					errMsg: "language detection failed",
					suggestion: "Set \"environment\": {\"stack\": \"go\"} in .chunk/config.json " +
						"or ensure a marker file (go.mod, package.json, Cargo.toml) exists.",
				}
			}

			enumerator, err := mutate.EnumeratorFor(workDir, cfg)
			if err != nil {
				return err
			}

			if len(args) > 0 {
				enumerator.Paths = args
			}

			cmd.Printf("Enumerating mutations (stack: %s)...\n", stack)

			mutations, err := enumerator.Enumerate(cmd.Context(), workDir)
			if err != nil {
				return fmt.Errorf("enumerate: %w", err)
			}

			if len(mutations) == 0 {
				cmd.Println("No mutations found.")
				return nil
			}

			if maxMutations > 0 && len(mutations) > maxMutations {
				mutations = mutations[:maxMutations]
			}

			if parallel <= 0 {
				switch outputFmt {
				case "json":
					enc := json.NewEncoder(cmd.OutOrStdout())
					enc.SetIndent("", "  ")
					return enc.Encode(mutations)
				default:
					printMutationTable(cmd, mutations)
				}
				return nil
			}

			// Stage 3: validate mutations on a sidecar pool.
			testCmd := testCmdFlag
			if testCmd == "" && cfg != nil {
				if c := cfg.FindCommand("test"); c != nil {
					testCmd = c.Run
				}
			}
			if testCmd == "" {
				return &userError{
					msg:        "No test command configured.",
					errMsg:     "test command not found",
					suggestion: "Add a \"test\" command to .chunk/config.json or pass --test-cmd.",
				}
			}

			io := iostream.FromCmd(cmd)
			insecureStorage := insecureStorageFlag(cmd)
			rc, _ := config.ResolveCircleCI(insecureStorage)
			client, err := ensureCircleCIClient(cmd.Context(), cmd, rc, io, ui.PromptHidden)
			if err != nil {
				return err
			}

			orgID, err = resolveOrgID(orgID, workDir, orgPicker(cmd.Context(), client, rc.CircleCITokenSource))
			if err != nil {
				return err
			}
			image := resolveImage("", cfg)
			poolSize := mutationPoolSize(parallel, len(mutations))

			statusFn := newStatusFunc(io)
			statusFn(iostream.LevelStep, fmt.Sprintf("Creating pool of %d sidecar(s)...", poolSize))

			pool, err := sidecar.NewPool(cmd.Context(), client, sidecar.PoolOptions{
				Size:    poolSize,
				Name:    "mutate",
				OrgID:   orgID,
				Image:   image,
				WorkDir: workDir,
			}, statusFn)
			if err != nil {
				return fmt.Errorf("pool: %w", err)
			}
			defer func() {
				cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(cmd.Context()), cleanupTimeout)
				defer cancel()
				if destroyPool {
					pool.Destroy(cleanupCtx)
					return
				}
				pool.Close(cleanupCtx)
			}()

			statusFn(iostream.LevelStep, fmt.Sprintf("Running %d mutations (test: %s)...", len(mutations), testCmd))

			results, err := mutate.Run(cmd.Context(), pool, mutations, workDir, testCmd, testTimeout, statusFn)
			if err != nil {
				return fmt.Errorf("run: %w", err)
			}

			errored := printMutationResults(cmd, results)
			return mutationRunError(errored)
		},
	}

	cmd.Flags().StringVarP(&outputFmt, "output", "o", "table", "output format: table, json")
	cmd.Flags().IntVar(&parallel, "parallel", 0, "number of sidecars to use (0: enumerate only)")
	cmd.Flags().IntVar(&maxMutations, "max", 0, "limit to the first N mutations (0: no limit)")
	cmd.Flags().BoolVar(&destroyPool, "destroy-pool", false, "delete sidecars and clear pool state after run")
	cmd.Flags().StringVar(&orgID, "org-id", "", "CircleCI organization ID")
	cmd.Flags().StringVar(&testCmdFlag, "test-cmd", "", "test command to run (default: 'test' command from config)")
	cmd.Flags().DurationVar(&testTimeout, "test-timeout", mutate.DefaultTestTimeout, "max time to wait for each mutation's test run")
	return cmd
}

const cleanupTimeout = 30 * time.Second

func mutationPoolSize(parallel, mutations int) int {
	return min(parallel, mutations)
}

func printMutationTable(cmd *cobra.Command, mutations []mutate.Mutation) {
	cmd.Printf("%-9s %-13s %-26s %5s %4s  %s\n", "ID", "STATUS", "FILE", "LINE", "COL", "OPERATOR")
	cmd.Printf("%-9s %-13s %-26s %5s %4s  %s\n",
		"---------", "-------------", "--------------------------", "-----", "----", "------------------------")
	for _, m := range mutations {
		file := m.File
		if len(file) > 26 {
			file = "…" + file[len(file)-25:]
		}
		cmd.Printf("%-9s %-13s %-26s %5d %4d  %s\n", m.ID, m.Status, file, m.Line, m.Col, m.Operator)
	}
	cmd.Printf("\n%d candidates\n", len(mutations))
}

func printMutationResults(cmd *cobra.Command, results []mutate.Result) int {
	killed, survived, errored := 0, 0, 0
	for _, r := range results {
		switch {
		case r.Error != "":
			errored++
		case r.Killed:
			killed++
		default:
			survived++
		}
	}

	cmd.Printf("%-9s %-10s %-26s %5s  %s\n", "ID", "RESULT", "FILE", "LINE", "OPERATOR")
	cmd.Printf("%-9s %-10s %-26s %5s  %s\n",
		"---------", "----------", "--------------------------", "-----", "------------------------")
	for _, r := range results {
		m := r.Mutation
		result := "KILLED"
		if !r.Killed {
			result = "SURVIVED"
		}
		if r.Error != "" {
			result = "ERROR"
		}
		file := m.File
		if len(file) > 26 {
			file = "…" + file[len(file)-25:]
		}
		cmd.Printf("%-9s %-10s %-26s %5d  %s\n", m.ID, result, file, m.Line, m.Operator)
		if r.Error != "" {
			cmd.Printf("          error: %s\n", r.Error)
		} else if !r.Killed {
			cmd.Printf("          line %d: %s → %s\n", m.Line, m.Before, m.After)
		}
	}
	cmd.Printf("\nkilled: %d / survived: %d / error: %d / total: %d\n", killed, survived, errored, killed+survived+errored)
	return errored
}

func mutationRunError(errored int) error {
	if errored == 0 {
		return nil
	}
	return &userError{
		msg:        fmt.Sprintf("%d mutation(s) could not be tested.", errored),
		errMsg:     fmt.Sprintf("mutation run completed with %d error(s)", errored),
		suggestion: "Review the ERROR results above and rerun the mutation test.",
	}
}
