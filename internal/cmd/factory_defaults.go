package cmd

import (
	"os"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/sidecar/docker"
)

// defaultFactoryAttempts is how many rounds a factory run checks when neither
// --max-attempts nor factory.maxAttempts says.
const defaultFactoryAttempts = 3

// factorySettings are the `smelt factory build` settings that can come from
// either a flag or the project config.
type factorySettings struct {
	Backend  string
	Image    string
	Attempts int
	Log      bool
	// Dockerfile is the repo-relative Dockerfile to build Image from, set only
	// when ImageSource is imageFromDockerfile.
	Dockerfile  string
	ImageSource imageSource
}

// imageSource says where the resolved image came from, which decides whether
// smelt may build it.
type imageSource int

const (
	imageUnset          imageSource = iota
	imageFromFlag                   // --image
	imageFromConfig                 // factory.image: user-managed, never auto-built
	imageFromDockerfile             // factory.dockerfile: managed, built when missing
	imageFromEnv                    // SMELT_DOCKER_IMAGE, read by the provider
)

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

	out.Image, out.Dockerfile, out.ImageSource = "", "", imageUnset
	switch {
	case set.Image && flags.Image != "":
		out.Image, out.ImageSource = flags.Image, imageFromFlag
	case out.Backend == config.BackendDocker:
		out.Image, out.Dockerfile, out.ImageSource = dockerImageFromConfig(fc)
	case cfg != nil && cfg.Validation != nil && cfg.Validation.SidecarImage != "":
		out.Image, out.ImageSource = cfg.Validation.SidecarImage, imageFromConfig
	}
	if out.Backend == config.BackendDocker && out.ImageSource == imageUnset && os.Getenv(docker.EnvImage) != "" {
		out.ImageSource = imageFromEnv
	}

	if out.Backend == config.BackendDocker && out.ImageSource == imageUnset {
		return out, newUserError("No Docker image is set for factory runs.").
			withCode("command.invalid_args").
			withSuggestion("Pass --image, or run smelt init to write a Dockerfile.smelt (or set factory.image in .smelt/config.json).").
			withExitCode(ExitBadArgs).
			withoutDetail()
	}
	return out, nil
}

// dockerImageFromConfig picks the docker image from the factory section: an
// explicit factory.image over a managed factory.dockerfile.
func dockerImageFromConfig(fc config.FactoryConfig) (img, dockerfile string, src imageSource) {
	switch {
	case fc.Image != "":
		return fc.Image, "", imageFromConfig
	case fc.Dockerfile != "":
		return "", fc.Dockerfile, imageFromDockerfile
	}
	return "", "", imageUnset
}
