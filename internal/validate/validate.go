package validate

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// List prints all configured command names and their run strings.
func List(cfg *ProjectConfig, streams iostream.Streams) error {
	if !cfg.HasCommands() {
		streams.Println(ui.Dim("No commands configured."))
		streams.Println(ui.Dim("Add commands with: chunk validate <name> --cmd \"your command\" --save"))
		return nil
	}
	for _, c := range cfg.Commands {
		streams.Printf("  %s: %s\n", ui.Bold(c.Name), ui.Gray(c.Run))
	}
	return nil
}

// Status checks the cache for each command (or a single named command) and prints its status.
func Status(workDir, name string, cfg *ProjectConfig, streams iostream.Streams) error {
	commands := cfg.Commands
	if name != "" {
		c := cfg.FindCommand(name)
		if c == nil {
			return fmt.Errorf("command %q not configured", name)
		}
		commands = []Command{*c}
	}

	for _, c := range commands {
		cached := CheckCache(workDir, c.Name, c.FileExt)
		if cached != nil {
			streams.Printf("  %s: cached (%s)\n", ui.Bold(c.Name), colorStatus(cached.Status))
		} else {
			streams.Printf("  %s: %s\n", ui.Bold(c.Name), ui.Dim("no cached result"))
		}
	}
	return nil
}

// RunInline runs an inline command string, caching the result under the given name.
func RunInline(ctx context.Context, workDir, name, command string, force bool, streams iostream.Streams) error {
	if !force {
		if cached := CheckCache(workDir, name, ""); cached != nil {
			streams.Printf("%s: cached (%s)\n", ui.Bold(name), colorStatus(cached.Status))
			if cached.ExitCode != 0 {
				return fmt.Errorf("%s: cached failure", name)
			}
			return nil
		}
	}

	return runAndCache(ctx, workDir, name, command, "", streams)
}

// RunNamed runs a single named command from config with caching.
func RunNamed(ctx context.Context, workDir, name string, force bool, cfg *ProjectConfig, streams iostream.Streams) error {
	c := cfg.FindCommand(name)
	if c == nil {
		return fmt.Errorf("command %q not configured", name)
	}

	if !force {
		if cached := CheckCache(workDir, c.Name, c.FileExt); cached != nil {
			streams.Printf("%s: cached (%s)\n", ui.Bold(c.Name), colorStatus(cached.Status))
			if cached.ExitCode != 0 {
				return fmt.Errorf("%s: cached failure", c.Name)
			}
			return nil
		}
	}

	return runAndCache(ctx, workDir, c.Name, c.Run, c.FileExt, streams)
}

// RunAll runs all configured commands with optional cache bypass.
func RunAll(ctx context.Context, workDir string, force bool, cfg *ProjectConfig, streams iostream.Streams) error {
	if !cfg.HasCommands() {
		return fmt.Errorf("no validate commands configured, run validate init first")
	}

	for i, c := range cfg.Commands {
		if !force {
			if cached := CheckCache(workDir, c.Name, c.FileExt); cached != nil {
				streams.ErrPrintf("%s: cached (%s)\n", ui.Bold(c.Name), colorStatus(cached.Status))
				if cached.ExitCode != 0 {
					return fmt.Errorf("%s: cached failure", c.Name)
				}
				continue
			}
		}

		if err := runAndCache(ctx, workDir, c.Name, c.Run, c.FileExt, streams); err != nil {
			for j := i + 1; j < len(cfg.Commands); j++ {
				streams.ErrPrintf("%s: %s\n", ui.Bold(cfg.Commands[j].Name), ui.Yellow(fmt.Sprintf("skipped (%s failed)", c.Name)))
			}
			return err
		}
	}
	return nil
}

// RunDryRun prints commands without executing them.
func RunDryRun(cfg *ProjectConfig, name string, streams iostream.Streams) error {
	if !cfg.HasCommands() {
		return fmt.Errorf("no validate commands configured, run validate init first")
	}

	commands := cfg.Commands
	if name != "" {
		c := cfg.FindCommand(name)
		if c == nil {
			return fmt.Errorf("command %q not configured", name)
		}
		commands = []Command{*c}
	}

	for _, c := range commands {
		streams.Printf("%s: %s\n", ui.Bold(c.Name), ui.Gray(c.Run))
	}
	return nil
}

// RunRemote runs commands on a remote sandbox.
func RunRemote(ctx context.Context, client *circleci.Client, cfg *ProjectConfig, sandboxID, orgID string, streams iostream.Streams) error {
	for _, c := range cfg.Commands {
		resp, err := client.Exec(ctx, sandboxID, "sh", []string{"-c", c.Run})
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

func colorStatus(status string) string {
	switch status {
	case "pass":
		return ui.Green("PASS")
	case "fail":
		return ui.Red("FAIL")
	default:
		return status
	}
}

func runAndCache(ctx context.Context, workDir, name, command, fileExt string, streams iostream.Streams) error {
	streams.ErrPrintf("%s %s\n", ui.Dim("Running "+name+":"), ui.Gray(command))

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = workDir

	var combined bytes.Buffer
	cmd.Stdout = io.MultiWriter(streams.Out, &combined)
	cmd.Stderr = io.MultiWriter(streams.Err, &combined)

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	_ = WriteCache(workDir, name, fileExt, exitCode, combined.String())

	if exitCode != 0 {
		return fmt.Errorf("%s command failed", name)
	}
	return nil
}
