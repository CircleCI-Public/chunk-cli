package hook

import (
	"fmt"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/usererr"
)

// ProfileEnable is the default hook profile.
const ProfileEnable = "enable"

// ValidProfiles lists the allowed profile names.
var ValidProfiles = []string{"disable", ProfileEnable, "tests-lint"}

// ValidateProfile returns an error if the profile name is not valid.
func ValidateProfile(profile string) error {
	for _, p := range ValidProfiles {
		if p == profile {
			return nil
		}
	}
	return usererr.New(
		fmt.Sprintf("Invalid profile %q. Valid profiles: %v", profile, ValidProfiles),
		fmt.Errorf("invalid profile %q", profile),
	)
}

// RunSetup combines env update + repo init.
func RunSetup(targetDir, projectName, profile string, force, skipEnv bool, envFile string, commands []config.Command, streams iostream.Streams) error {
	if profile == "" {
		profile = ProfileEnable
	}
	if err := ValidateProfile(profile); err != nil {
		return err
	}

	if !skipEnv {
		opts := EnvUpdateOptions{
			Profile: profile,
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
