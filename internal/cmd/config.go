package cmd

import (
	"fmt"
	"os"

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
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Display resolved configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			rc, resolveErr := config.Resolve("", "")
			if resolveErr != nil {
				io.ErrPrintln(ui.Warning(fmt.Sprintf("Could not load config: %v", resolveErr)))
			}

			if jsonOut {
				type configEntry struct {
					Value  string `json:"value"`
					Source string `json:"source,omitempty"`
				}
				type configOutput struct {
					Model           configEntry `json:"model"`
					AnthropicAPIKey configEntry `json:"anthropicAPIKey"`
					CircleCIToken   configEntry `json:"circleCIToken"`
					GitHubToken     configEntry `json:"gitHubToken"`
				}
				maskOrEmpty := func(key string) string {
					if key == "" {
						return ""
					}
					return config.MaskKey(key)
				}
				return iostream.PrintJSON(io.Out, configOutput{
					Model:           configEntry{Value: rc.Model, Source: rc.ModelSource},
					AnthropicAPIKey: configEntry{Value: maskOrEmpty(rc.AnthropicAPIKey), Source: rc.AnthropicAPIKeySource},
					CircleCIToken:   configEntry{Value: maskOrEmpty(rc.CircleCIToken), Source: rc.CircleCITokenSource},
					GitHubToken:     configEntry{Value: maskOrEmpty(rc.GitHubToken), Source: rc.GitHubTokenSource},
				})
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

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")

	return cmd
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value",
		Long:  "Set a config value. Use 'chunk auth set <provider>' to store credentials with validation.\n\nUser keys: model\nProject keys: orgID, validation.sidecarImage",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			key, value := args[0], args[1]

			if config.ValidProjectConfigKeys[key] {
				workDir, err := os.Getwd()
				if err != nil {
					return err
				}
				projCfg, err := config.LoadProjectConfig(workDir)
				if err != nil {
					projCfg = &config.ProjectConfig{}
				}
				switch key {
				case "orgID":
					projCfg.OrgID = value
				case "validation.sidecarImage":
					if projCfg.Validation == nil {
						projCfg.Validation = &config.ValidationConfig{}
					}
					projCfg.Validation.SidecarImage = value
				default:
					return fmt.Errorf("internal: unhandled project config key %q", key)
				}
				if err := config.SaveProjectConfig(workDir, projCfg); err != nil {
					return &userError{msg: "Could not save project configuration.", suggestion: configFilePermHint, err: err}
				}
				io.Printf("%s\n", ui.Success(fmt.Sprintf("Set %s to %s", key, value)))
				return nil
			}

			if !config.ValidConfigKeys[key] {
				return &userError{
					msg:    fmt.Sprintf("Unknown config key: %q.", key),
					detail: "Supported keys: model, orgID, validation.sidecarImage.",
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
