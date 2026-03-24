package validate

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

func RunDryRun(cfg *ProjectConfig, streams iostream.Streams) error {
	if !cfg.HasCommands() {
		return fmt.Errorf("no validate commands configured, run validate init first")
	}

	for _, cmd := range cfg.Commands {
		streams.Printf("%s: %s\n", cmd.Name, cmd.Run)
	}
	return nil
}

func RunLocally(cfg *ProjectConfig, streams iostream.Streams) error {
	if !cfg.HasCommands() {
		return fmt.Errorf("no validate commands configured, run validate init first")
	}

	for i, c := range cfg.Commands {
		streams.ErrPrintf("Running %s: %s\n", c.Name, c.Run)
		cmd := exec.Command("sh", "-c", c.Run)
		cmd.Stdout = streams.Out
		cmd.Stderr = streams.Err
		if err := cmd.Run(); err != nil {
			for j := i + 1; j < len(cfg.Commands); j++ {
				streams.ErrPrintf("%s: skipped (%s failed)\n", cfg.Commands[j].Name, c.Name)
			}
			return fmt.Errorf("%s command failed: %w", c.Name, err)
		}
	}

	return nil
}

func RunRemote(ctx context.Context, client *circleci.Client, cfg *ProjectConfig, sandboxID, orgID string, streams iostream.Streams) error {
	token, err := client.CreateAccessToken(ctx, sandboxID)
	if err != nil {
		return err
	}

	for _, c := range cfg.Commands {
		resp, err := client.Exec(ctx, token, sandboxID, "sh", []string{"-c", c.Run})
		if err != nil {
			return fmt.Errorf("remote %s: %w", c.Name, err)
		}
		if resp.Stdout != "" {
			_, _ = fmt.Fprint(streams.Out, resp.Stdout)
		}
		if resp.ExitCode != 0 {
			return fmt.Errorf("remote %s failed with exit code %d", c.Name, resp.ExitCode)
		}
	}

	return nil
}
