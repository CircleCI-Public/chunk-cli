package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

func newHookCmd() *cobra.Command {
	var projectDir string
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Manage chunk hook execution",
	}
	cmd.PersistentFlags().StringVar(&projectDir, "project", "", "Override project directory")
	cmd.AddCommand(newHookDisableCmd(&projectDir))
	cmd.AddCommand(newHookEnableCmd(&projectDir))
	cmd.AddCommand(newHookStatusCmd(&projectDir))
	return cmd
}

func resolveHookRoot(ctx context.Context, override string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if override != "" {
		return override, nil
	}
	if root := gitutil.TopLevelCtx(ctx, "."); root != "" {
		return root, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ".", nil
	}
	return cwd, nil
}

func newHookDisableCmd(projectDir *string) *cobra.Command {
	return &cobra.Command{
		Use:          "disable",
		Short:        "Disable chunk validate hooks",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveHookRoot(cmd.Context(), *projectDir)
			if err != nil {
				return fmt.Errorf("resolve hook root: %w", err)
			}
			p := filepath.Join(root, ".chunk", "hooks-disabled")
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return fmt.Errorf("create .chunk directory: %w", err)
			}
			if err := os.WriteFile(p, []byte{}, 0o644); err != nil {
				return fmt.Errorf("create hooks-disabled sentinel: %w", err)
			}
			streams := iostream.FromCmd(cmd)
			streams.ErrPrintln("Hooks disabled. Run 'chunk hook enable' to re-enable.")
			return nil
		},
	}
}

func newHookEnableCmd(projectDir *string) *cobra.Command {
	return &cobra.Command{
		Use:          "enable",
		Short:        "Re-enable chunk validate hooks",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := resolveHookRoot(cmd.Context(), *projectDir)
			if err != nil {
				return fmt.Errorf("resolve hook root: %w", err)
			}
			p := filepath.Join(root, ".chunk", "hooks-disabled")
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove hooks-disabled sentinel: %w", err)
			}
			streams := iostream.FromCmd(cmd)
			streams.ErrPrintln("Hooks enabled.")
			return nil
		},
	}
}

func newHookStatusCmd(projectDir *string) *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Show whether hooks are enabled or disabled",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			streams := iostream.FromCmd(cmd)
			root, err := resolveHookRoot(cmd.Context(), *projectDir)
			if err != nil {
				return fmt.Errorf("resolve hook root: %w", err)
			}
			envDisabled := os.Getenv(config.EnvChunkHooksDisabled) != ""
			if validate.HooksDisabled(root, envDisabled) {
				streams.Println("disabled")
			} else {
				streams.Println("enabled")
			}
			return nil
		},
	}
}
