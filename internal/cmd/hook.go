package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

const prePushHookScript = "#!/bin/sh\n# Installed by chunk. Run 'chunk hook disable' to remove.\nchunk validate\n"

func newHookCmd() *cobra.Command {
	var projectDir string
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Manage the chunk pre-push git hook",
	}
	cmd.PersistentFlags().StringVar(&projectDir, "project", "", "Override project directory")
	cmd.AddCommand(newHookEnableCmd(&projectDir))
	cmd.AddCommand(newHookDisableCmd(&projectDir))
	cmd.AddCommand(newHookStatusCmd(&projectDir))
	return cmd
}

// resolveGitCommonDir returns the absolute path of the git common directory
// for the project at root, which is where shared hooks live even in worktrees.
func resolveGitCommonDir(root string) (string, error) {
	out, err := exec.Command("git", "-C", root, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --git-common-dir: %w", err)
	}
	dir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	return dir, nil
}

func resolveHookRoot(override string) string {
	if override != "" {
		return override
	}
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

func prePushHookPath(projectDir string) (string, error) {
	root := resolveHookRoot(projectDir)
	gitDir, err := resolveGitCommonDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(gitDir, "hooks", "pre-push"), nil
}

func newHookEnableCmd(projectDir *string) *cobra.Command {
	return &cobra.Command{
		Use:          "enable",
		Short:        "Install the pre-push git hook that runs chunk validate",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			streams := iostream.FromCmd(cmd)
			hookPath, err := prePushHookPath(*projectDir)
			if err != nil {
				return fmt.Errorf("locate git hooks directory: %w", err)
			}

			if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
				return fmt.Errorf("create hooks directory: %w", err)
			}

			existing, readErr := os.ReadFile(hookPath)
			if readErr == nil {
				if strings.Contains(string(existing), "chunk validate") {
					streams.ErrPrintln("Pre-push hook already installed.")
					return nil
				}
				// Append to existing hook rather than overwriting.
				content := string(existing)
				if len(content) > 0 && content[len(content)-1] != '\n' {
					content += "\n"
				}
				content += "chunk validate\n"
				info, err := os.Stat(hookPath)
				if err != nil {
					return fmt.Errorf("stat pre-push hook: %w", err)
				}
				if err := os.WriteFile(hookPath, []byte(content), info.Mode()); err != nil {
					return fmt.Errorf("update pre-push hook: %w", err)
				}
				streams.ErrPrintln("Updated .git/hooks/pre-push to include chunk validate.")
				return nil
			}
			if !errors.Is(readErr, fs.ErrNotExist) {
				return fmt.Errorf("read pre-push hook: %w", readErr)
			}

			if err := os.WriteFile(hookPath, []byte(prePushHookScript), 0o755); err != nil {
				return fmt.Errorf("write pre-push hook: %w", err)
			}
			streams.ErrPrintln("Installed .git/hooks/pre-push.")
			return nil
		},
	}
}

func newHookDisableCmd(projectDir *string) *cobra.Command {
	return &cobra.Command{
		Use:          "disable",
		Short:        "Remove the chunk pre-push git hook",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			streams := iostream.FromCmd(cmd)
			hookPath, err := prePushHookPath(*projectDir)
			if err != nil {
				return fmt.Errorf("locate git hooks directory: %w", err)
			}

			existing, readErr := os.ReadFile(hookPath)
			if errors.Is(readErr, fs.ErrNotExist) {
				streams.ErrPrintln("No pre-push hook installed.")
				return nil
			}
			if readErr != nil {
				return fmt.Errorf("read pre-push hook: %w", readErr)
			}

			content := string(existing)
			if !strings.Contains(content, "chunk validate") {
				streams.ErrPrintln("Pre-push hook does not contain chunk validate — nothing to remove.")
				return nil
			}

			// If the hook is entirely chunk-managed, remove the file.
			if isChunkManagedHook(content) {
				if err := os.Remove(hookPath); err != nil {
					return fmt.Errorf("remove pre-push hook: %w", err)
				}
				streams.ErrPrintln("Removed .git/hooks/pre-push.")
				return nil
			}

			// Otherwise remove only the chunk validate line(s).
			var kept []string
			for _, line := range strings.Split(content, "\n") {
				if strings.TrimSpace(line) == "chunk validate" || strings.TrimSpace(line) == "# Installed by chunk. Run 'chunk hook disable' to remove." {
					continue
				}
				kept = append(kept, line)
			}
			updated := strings.Join(kept, "\n")
			info, err := os.Stat(hookPath)
			if err != nil {
				return fmt.Errorf("stat pre-push hook: %w", err)
			}
			if err := os.WriteFile(hookPath, []byte(updated), info.Mode()); err != nil {
				return fmt.Errorf("update pre-push hook: %w", err)
			}
			streams.ErrPrintln("Removed chunk validate from .git/hooks/pre-push.")
			return nil
		},
	}
}

func newHookStatusCmd(projectDir *string) *cobra.Command {
	return &cobra.Command{
		Use:          "status",
		Short:        "Show whether the pre-push git hook is installed",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			streams := iostream.FromCmd(cmd)
			hookPath, err := prePushHookPath(*projectDir)
			if err != nil {
				streams.Println("disabled")
				return nil
			}
			data, err := os.ReadFile(hookPath)
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					return fmt.Errorf("read pre-push hook: %w", err)
				}
				streams.Println("disabled")
				return nil
			}
			if !strings.Contains(string(data), "chunk validate") {
				streams.Println("disabled")
				return nil
			}
			streams.Println("enabled")
			return nil
		},
	}
}

// isChunkManagedHook reports whether a hook file is entirely chunk-managed
// (i.e. only contains the shebang and chunk validate, possibly with our comment).
func isChunkManagedHook(content string) bool {
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" ||
			line == "#!/bin/sh" ||
			line == "chunk validate" ||
			line == "# Installed by chunk. Run 'chunk hook disable' to remove." {
			continue
		}
		return false
	}
	return true
}
