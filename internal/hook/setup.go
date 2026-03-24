package hook

import "fmt"

// ValidProfiles lists the allowed profile names.
var ValidProfiles = []string{"disable", "enable", "tests-lint"}

// ValidateProfile returns an error if the profile name is not valid.
func ValidateProfile(profile string) error {
	for _, p := range ValidProfiles {
		if p == profile {
			return nil
		}
	}
	return fmt.Errorf("Invalid profile %q. Valid profiles: %v", profile, ValidProfiles)
}

// RunSetup combines env update + repo init.
func RunSetup(targetDir, profile string, force, skipEnv bool, envFile string) error {
	if profile == "" {
		profile = "enable"
	}
	if err := ValidateProfile(profile); err != nil {
		return err
	}

	if !skipEnv {
		opts := EnvUpdateOptions{
			Profile:  profile,
			EnvFile:  envFile,
			Verbose:  false,
		}
		if err := RunEnvUpdate(opts); err != nil {
			return fmt.Errorf("env update: %w", err)
		}
	}

	if err := RunRepoInit(targetDir, force); err != nil {
		return fmt.Errorf("repo init: %w", err)
	}

	fmt.Println("Setup complete")
	return nil
}
