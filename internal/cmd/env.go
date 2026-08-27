package cmd

import (
	"context"
	"errors"
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
			return nil, secretResolveError(err)
		}
	}
	return envVars, nil
}

// secretResolveError turns a failed secret lookup into actionable guidance.
// Local validate runs resolve references too, so an unresolvable op:// entry in
// .env.local blocks the run; without this the failure surfaced as the generic
// "An unknown error occurred." with a bare exec error underneath.
func secretResolveError(err error) error {
	e := newUserError("Could not resolve a secret reference.").
		withCode("secrets.resolve_failed").
		wrap(err)
	if errors.Is(err, secrets.ErrOpNotFound) {
		return e.withCode("secrets.op_not_found").
			withSuggestion("Install the 1Password CLI from https://developer.1password.com/docs/cli/get-started/,\n" +
				"or replace the op:// reference with a literal value.")
	}
	return e.withSuggestion("Check the reference is correct and that you are signed in: op signin")
}
