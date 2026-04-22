package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage configuration",
	}
	cmd.AddCommand(newConfigShowCmd())
	cmd.AddCommand(newConfigSetCmd())
	return cmd
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Display resolved configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			rc, resolveErr := config.Resolve("", "")
			if resolveErr != nil {
				io.ErrPrintln(ui.Warning(fmt.Sprintf("Could not load config: %v", resolveErr)))
			}

			w := 15
			io.Printf("%s %s %s\n", ui.Label("model:", w), rc.Model, ui.Dim("("+rc.ModelSource+")"))

			if rc.AnthropicAPIKey != "" {
				io.Printf("%s %s %s\n", ui.Label("anthropicAPIKey:", w), config.MaskKey(rc.AnthropicAPIKey), ui.Dim("("+rc.AnthropicAPIKeySource+")"))
			} else {
				io.Printf("%s %s\n", ui.Label("anthropicAPIKey:", w), ui.Dim("(not set)"))
			}

			if rc.CircleCIToken != "" {
				io.Printf("%s %s %s\n", ui.Label("circleCIToken:", w), config.MaskKey(rc.CircleCIToken), ui.Dim("("+rc.CircleCITokenSource+")"))
			} else {
				io.Printf("%s %s\n", ui.Label("circleCIToken:", w), ui.Dim("(not set)"))
			}

			if rc.GitHubToken != "" {
				io.Printf("%s %s %s\n", ui.Label("gitHubToken:", w), config.MaskKey(rc.GitHubToken), ui.Dim("("+rc.GitHubTokenSource+")"))
			} else {
				io.Printf("%s %s\n", ui.Label("gitHubToken:", w), ui.Dim("(not set)"))
			}

			return nil
		},
	}
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value",
		Long:  "Set a config value (model). Use 'chunk auth set <provider>' to store credentials with validation.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			key, value := args[0], args[1]

			if !config.ValidConfigKeys[key] {
				return &userError{
					msg:    fmt.Sprintf("Unknown config key: %q.", key),
					detail: "Supported keys: model.",
					errMsg: fmt.Sprintf("unknown config key %q", key),
				}
			}

			cfg, err := config.Load()
			if err != nil {
				return &userError{msg: "Could not load configuration.", suggestion: configFilePermHint, err: err}
			}

			if key == "model" {
				cfg.Model = value
			}

			if err := config.Save(cfg); err != nil {
				return &userError{msg: "Could not save configuration.", suggestion: configFilePermHint, err: err}
			}

			io.Printf("%s\n", ui.Success(fmt.Sprintf("Set %s to %s", key, value)))
			return nil
		},
	}
}
