package validate

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

func RunDryRun(cfg *ProjectConfig, w io.Writer) error {
	if !cfg.HasCommands() {
		return fmt.Errorf("No validate commands configured. Run validate init first")
	}

	for _, cmd := range cfg.Commands {
		fmt.Fprintf(w, "%s: %s\n", cmd.Name, cmd.Run)
	}
	return nil
}

func RunLocally(cfg *ProjectConfig, w io.Writer) error {
	if !cfg.HasCommands() {
		return fmt.Errorf("No validate commands configured. Run validate init first")
	}

	for i, c := range cfg.Commands {
		fmt.Fprintf(w, "Running %s: %s\n", c.Name, c.Run)
		cmd := exec.Command("sh", "-c", c.Run)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			// If a command fails, skip remaining commands
			for j := i + 1; j < len(cfg.Commands); j++ {
				fmt.Fprintf(w, "%s: skipped (%s failed)\n", cfg.Commands[j].Name, c.Name)
			}
			return fmt.Errorf("%s command failed: %w", c.Name, err)
		}
	}

	return nil
}

func RunRemote(ctx context.Context, client *circleci.Client, cfg *ProjectConfig, sandboxID, orgID string, w io.Writer) error {
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
			fmt.Fprint(w, resp.Stdout)
		}
		if resp.ExitCode != 0 {
			return fmt.Errorf("remote %s failed with exit code %d", c.Name, resp.ExitCode)
		}
	}

	return nil
}
