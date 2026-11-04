package bootstrap

import (
	"context"
	"fmt"
	"path/filepath"

	petname "github.com/dustinkirkland/golang-petname"

	"github.com/CircleCI-Public/chunk-cli/envbuilder"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/secrets"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// The helpers below are copies of ones in internal/cmd, which its other
// commands keep using and which return cmd's user errors. Each says what it
// copies; keep the two in step.

// msgUsingSavedEnvironment copies cmd.msgUsingSavedEnvironment.
const msgUsingSavedEnvironment = "Using environment from .chunk/config.json"

// envFile copies cmd.defaultEnvFile.
const envFile = ".env.local"

// DetectEnvironmentError is a failure to detect the project's environment.
type DetectEnvironmentError struct{ Err error }

func (e *DetectEnvironmentError) Error() string {
	return fmt.Sprintf("detect the environment: %v", e.Err)
}
func (e *DetectEnvironmentError) Unwrap() error { return e.Err }

// EnvFileError is a failure to read the project's .env.local.
type EnvFileError struct {
	Path string // as the user knows it, relative to the project
	Err  error
}

func (e *EnvFileError) Error() string { return fmt.Sprintf("load %s: %v", e.Path, e.Err) }
func (e *EnvFileError) Unwrap() error { return e.Err }

// SecretsError is a failure to resolve a secret reference in .env.local.
type SecretsError struct{ Err error }

func (e *SecretsError) Error() string { return fmt.Sprintf("resolve secrets: %v", e.Err) }
func (e *SecretsError) Unwrap() error { return e.Err }

// loadEnvironment copies cmd.loadEnvironment with force false: it returns the
// environment saved in cfg, or detects one in dir and records it in cfg when
// there is none. cached reports whether the saved one was used; when it was
// not, cfg has changed and the caller should save it.
func loadEnvironment(ctx context.Context, dir string, cfg *config.ProjectConfig, status iostream.StatusFunc) (env *envbuilder.Environment, cached bool, err error) {
	if cfg.Environment != nil {
		return cfg.Environment, true, nil
	}
	status(iostream.LevelStep, fmt.Sprintf("Detecting environment in %s...", dir))
	env, err = envbuilder.DetectEnvironment(ctx, dir)
	if err != nil {
		return nil, false, &DetectEnvironmentError{Err: err}
	}
	cfg.Environment = env.ForConfig()
	status(iostream.LevelInfo, "stack: "+env.Stack)
	return env, false, nil
}

// saveConfigOrWarn copies cmd.saveConfigOrWarn: it saves cfg as the project's
// config, warning rather than failing when it cannot.
func saveConfigOrWarn(dir string, cfg *config.ProjectConfig, status iostream.StatusFunc) {
	if err := config.SaveProjectConfig(dir, cfg); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("Could not save config: %v", err))
	}
}

// envVars copies cmd.resolveEnvVars with the default --env-file and no --env
// flags: the vars in workDir's .env.local, a missing file being none, with
// their secret references resolved.
func envVars(ctx context.Context, workDir string) (map[string]string, error) {
	vars, err := sidecar.LoadEnvFileAt(filepath.Join(workDir, envFile))
	if err != nil {
		return nil, &EnvFileError{Path: envFile, Err: err}
	}
	vars = sidecar.MergeEnv(vars, nil)
	if len(vars) == 0 {
		return vars, nil
	}
	vars, err = secrets.ResolveAll(ctx, vars, nil)
	if err != nil {
		return nil, &SecretsError{Err: err}
	}
	return vars, nil
}

// randomSidecarName copies cmd.randomSidecarName.
func randomSidecarName() string {
	return petname.Generate(3, "-")
}
