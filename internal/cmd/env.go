package cmd

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/internal/secrets"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

const defaultEnvFile = ".env.local"

// resolveEnvVars builds the env var map from --env flags and --env-file. Flags win over file.
func resolveEnvVars(ctx context.Context, workDir, envFile string, envVarsFlag []string) (map[string]string, error) {
	flagVars, err := sidecar.ParseEnvPairs(envVarsFlag)
	if err != nil {
		return nil, &userError{msg: fmt.Sprintf("invalid --env value: %s", err), err: err}
	}
	var fileVars map[string]string
	if envFile != "" {
		path := envFile
		if !filepath.IsAbs(path) {
			path = filepath.Join(workDir, path)
		}
		fileVars, err = sidecar.LoadEnvFileAt(path)
		if err != nil {
			return nil, &userError{msg: fmt.Sprintf("load %s: %s", envFile, err), err: err}
		}
	}
	envVars := sidecar.MergeEnv(fileVars, flagVars)
	if len(envVars) > 0 {
		envVars, err = secrets.ResolveAll(ctx, envVars, nil)
		if err != nil {
			return nil, err
		}
	}
	return envVars, nil
}
