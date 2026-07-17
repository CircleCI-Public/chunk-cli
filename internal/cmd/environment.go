package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/envbuilder"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

const (
	envFormatJSON       = "json"
	envFormatDockerfile = "dockerfile"
)

func newEnvCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "env",
		Short: "Detect and render project environments",
		Long: `Detect a repository's tech stack and emit a build environment.

Detection produces a spec that describes the stack, image, and setup steps.
'env init' writes Dockerfile.test to --dir by default, or prints the spec as
JSON with --format json. The spec is the source of truth — the Dockerfile is
one rendering of it.

Example:
  chunk env init --dir .`,
		RunE:               groupRunE,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
	}

	cmd.AddCommand(newEnvInitCmd())

	return cmd
}

func newEnvInitCmd() *cobra.Command {
	var dir string
	var noSave bool
	var format string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Detect tech stack and write Dockerfile.test (or output a JSON env spec)",
		Long: `Analyse the repository at --dir, detect its tech stack, and write
Dockerfile.test to --dir. The path to the written file is printed to stdout.

Pass --format json to print the environment spec to stdout instead.

The detected environment is saved to .chunk/config.json so that
'chunk sidecar setup' can reuse it without re-detecting. Pass --no-save to
skip writing the config.

Examples:
  chunk env init                            # write Dockerfile.test to --dir
  chunk env init --dir .
  docker build -f Dockerfile.test -t myapp:test .
  chunk env init --format json              # print the env spec as JSON`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			streams := iostream.FromCmd(cmd)

			if format != envFormatJSON && format != envFormatDockerfile {
				return &userError{
					msg:        fmt.Sprintf("Invalid --format %q.", format),
					suggestion: "Use 'json' or 'dockerfile'.",
					errMsg:     fmt.Sprintf("invalid format %q", format),
				}
			}

			if _, err := os.Stat(dir); err != nil {
				return &userError{
					msg:        fmt.Sprintf("Directory %q not found.", dir),
					suggestion: "Check the --dir path and try again.",
					err:        err,
				}
			}
			streams.ErrPrintf("Detecting environment in %s...\n", dir)

			env, err := envbuilder.DetectEnvironment(cmd.Context(), dir)
			if err != nil {
				return &userError{
					msg:        "Could not detect the environment.",
					suggestion: "Check the directory contains a supported project.",
					err:        err,
				}
			}

			if !noSave {
				cfg, loadErr := config.LoadProjectConfig(dir)
				if loadErr != nil {
					cfg = &config.ProjectConfig{}
				}
				cfg.Environment = env
				if saveErr := config.SaveProjectConfig(dir, cfg); saveErr != nil {
					streams.ErrPrintf("Warning: could not save environment to config: %v\n", saveErr)
				}
			}

			if format == envFormatDockerfile {
				dockerfilePath, err := envbuilder.WriteDockerfile(dir, env)
				if err != nil {
					return &userError{
						msg:        "Could not write the Dockerfile.",
						suggestion: "Check directory permissions and try again.",
						err:        err,
					}
				}
				streams.ErrPrintf("%s\n", ui.Success("Wrote "+dockerfilePath))
				streams.Printf("%s\n", dockerfilePath)
				return nil
			}

			out, err := json.MarshalIndent(env, "", "  ")
			if err != nil {
				return &userError{msg: "Could not encode the environment spec.", err: err}
			}
			streams.Printf("%s\n", out)
			return nil
		},
	}

	cmd.Flags().StringVar(&dir, "dir", ".", "Directory to detect environment in")
	cmd.Flags().BoolVar(&noSave, "no-save", false, "Skip saving the detected environment to .chunk/config.json")
	cmd.Flags().StringVar(&format, "format", envFormatDockerfile, "Output format: dockerfile (write Dockerfile.test) or json (env spec)")

	return cmd
}
