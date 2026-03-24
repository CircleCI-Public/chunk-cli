package validate

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

func RunDryRun(cfg *ProjectConfig) error {
	if !cfg.HasCommands() {
		return fmt.Errorf("No validate commands configured. Run validate init first")
	}

	if cfg.InstallCommand != "" {
		fmt.Printf("Install: %s\n", cfg.InstallCommand)
	}
	if cfg.TestCommand != "" {
		fmt.Printf("Test: %s\n", cfg.TestCommand)
	}
	return nil
}

func RunLocally(cfg *ProjectConfig, workDir string) error {
	if !cfg.HasCommands() {
		return fmt.Errorf("No validate commands configured. Run validate init first")
	}

	if cfg.InstallCommand != "" {
		fmt.Printf("Running install: %s\n", cfg.InstallCommand)
		cmd := exec.Command("sh", "-c", cfg.InstallCommand)
		cmd.Dir = workDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Println("Test: skipped (install failed)")
			return fmt.Errorf("install command failed: %w", err)
		}
	}

	if cfg.TestCommand != "" {
		fmt.Printf("Running test: %s\n", cfg.TestCommand)
		cmd := exec.Command("sh", "-c", cfg.TestCommand)
		cmd.Dir = workDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("test command failed: %w", err)
		}
	}

	return nil
}

func RunRemote(ctx context.Context, client *circleci.Client, cfg *ProjectConfig, sandboxID string) error {
	token, err := client.CreateAccessToken(ctx, sandboxID)
	if err != nil {
		return err
	}

	if cfg.InstallCommand != "" {
		resp, err := client.Exec(ctx, token, sandboxID, "sh", []string{"-c", cfg.InstallCommand})
		if err != nil {
			return fmt.Errorf("remote install: %w", err)
		}
		if resp.Stdout != "" {
			fmt.Print(resp.Stdout)
		}
		if resp.ExitCode != 0 {
			return fmt.Errorf("remote install failed with exit code %d", resp.ExitCode)
		}
	}

	if cfg.TestCommand != "" {
		resp, err := client.Exec(ctx, token, sandboxID, "sh", []string{"-c", cfg.TestCommand})
		if err != nil {
			return fmt.Errorf("remote test: %w", err)
		}
		if resp.Stdout != "" {
			fmt.Print(resp.Stdout)
		}
		if resp.ExitCode != 0 {
			return fmt.Errorf("remote test failed with exit code %d", resp.ExitCode)
		}
	}

	return nil
}
