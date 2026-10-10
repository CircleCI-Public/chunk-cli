package cmd

import (
	"os"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar/docker"
)

// defaultFactoryAttempts is how many rounds a factory run checks when neither
// --max-attempts nor factory.maxAttempts says.
const defaultFactoryAttempts = 3

// factorySettings are the `chunk factory build` settings that can come from
// either a flag or the project config.
type factorySettings struct {
	Backend  string
	Image    string
	Attempts int
	Log      bool
}

// factoryExplicit says which factorySettings were set by a flag, as opposed to
// holding the flag's default.
type factoryExplicit struct {
	Backend, Image, Attempts, Log bool
}

// resolveFactorySettings applies the precedence: an explicit flag, then
// factory.* in the project config, then the built-in default. The Docker
// backend never takes validation.sidecarImage, which is a CircleCI snapshot ID.
func resolveFactorySettings(flags factorySettings, set factoryExplicit, cfg *config.ProjectConfig) (factorySettings, error) {
	var fc config.FactoryConfig
	if cfg != nil && cfg.Factory != nil {
		fc = *cfg.Factory
	}
	out := flags

	if !set.Backend {
		out.Backend = fc.Backend
	}
	out.Backend = config.BackendOrDefault(out.Backend)

	if !set.Attempts {
		out.Attempts = defaultFactoryAttempts
		if fc.MaxAttempts > 0 {
			out.Attempts = fc.MaxAttempts
		}
	}

	if !set.Log {
		out.Log = fc.Log
	}

	if set.Image && flags.Image != "" {
		out.Image = flags.Image
	} else {
		out.Image = ""
		switch {
		case out.Backend == config.BackendDocker:
			out.Image = fc.Image
		case cfg != nil && cfg.Validation != nil:
			out.Image = cfg.Validation.SidecarImage
		}
	}

	if out.Backend == config.BackendDocker && out.Image == "" && os.Getenv(docker.EnvImage) == "" {
		return out, newUserError("No Docker image is set for factory runs.").
			withCode("command.invalid_args").
			withSuggestion("Pass --image, or set factory.image in .chunk/config.json (chunk init can do this).").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	return out, nil
}
