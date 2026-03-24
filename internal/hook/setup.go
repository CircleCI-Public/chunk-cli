package hook

import (
	"fmt"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/usererr"
)

// ValidProfiles lists the allowed profile names.
var ValidProfiles = []string{"disable", "enable", "tests-lint"}

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
func RunSetup(targetDir, profile string, force, skipEnv bool, envFile string, streams iostream.Streams) error {
	if profile == "" {
		profile = "enable"
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

	if err := RunRepoInit(targetDir, force, streams); err != nil {
		return fmt.Errorf("repo init: %w", err)
	}

	streams.ErrPrintln("Setup complete")
	return nil
}
