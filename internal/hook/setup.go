package hook

import (
	"fmt"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// RunSetup combines env update + repo init.
func RunSetup(targetDir, projectName string, force, skipEnv bool, envFile string, commands []config.Command, streams iostream.Streams) error {
	if !skipEnv {
		opts := EnvUpdateOptions{
			EnvFile: envFile,
			Verbose: false,
		}
		if err := RunEnvUpdate(opts, streams); err != nil {
			return fmt.Errorf("env update: %w", err)
		}
	}

	if err := RunRepoInit(targetDir, projectName, commands, force, streams); err != nil {
		return fmt.Errorf("repo init: %w", err)
	}

	streams.ErrPrintln(ui.Success("Setup complete"))
	return nil
}
