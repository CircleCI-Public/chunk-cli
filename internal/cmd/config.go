package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "config",
		Short:              "Manage configuration",
		RunE:               groupRunE,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
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
			insecureStorage, _ := cmd.Flags().GetBool("insecure-storage")
			rc, resolveErr := config.Resolve("", "", insecureStorage)
			if resolveErr != nil {
				io.ErrPrintln(ui.Warning(fmt.Sprintf("Could not load config: %v", resolveErr)))
			}

			workDir, wdErr := os.Getwd()
			if wdErr != nil {
				return wdErr
			}
			orgID, orgIDSource := config.ResolveOrgID(workDir)

			userCfg, userCfgErr := config.Load()
			if userCfgErr != nil {
				io.ErrPrintln(ui.Warning(fmt.Sprintf("Could not load config: %v", userCfgErr)))
			}
			telemetryEnabled := config.IsTelemetry(userCfg)

			if jsonOut {
				type configEntry struct {
					Value  string `json:"value"`
					Source string `json:"source,omitempty"`
				}
				type configOutput struct {
					Model              configEntry `json:"model"`
					AnthropicAPIKey    configEntry `json:"anthropicAPIKey"`
					CircleCIToken      configEntry `json:"circleCIToken"`
					GitHubToken        configEntry `json:"gitHubToken"`
					OrgID              configEntry `json:"orgID"`
					UseSSHIdentityFile bool        `json:"useSSHIdentityFile"`
					Telemetry          bool        `json:"telemetry"`
				}
				maskOrEmpty := func(key string) string {
					if key == "" {
						return ""
					}
					return config.MaskKey(key)
				}
				return iostream.PrintJSON(io.Out, configOutput{
					Model:              configEntry{Value: rc.Model, Source: rc.ModelSource},
					AnthropicAPIKey:    configEntry{Value: maskOrEmpty(rc.AnthropicAPIKey), Source: rc.AnthropicAPIKeySource},
					CircleCIToken:      configEntry{Value: maskOrEmpty(rc.CircleCIToken), Source: rc.CircleCITokenSource},
					GitHubToken:        configEntry{Value: maskOrEmpty(rc.GitHubToken), Source: rc.GitHubTokenSource},
					OrgID:              configEntry{Value: orgID, Source: orgIDSource},
					UseSSHIdentityFile: rc.UseSSHIdentityFile,
					Telemetry:          telemetryEnabled,
				})
			}

			w := 20
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

			if orgID != "" {
				io.Printf("%s %s %s\n", ui.Label("orgID:", w), orgID, ui.Dim("("+orgIDSource+")"))
			} else {
				io.Printf("%s %s\n", ui.Label("orgID:", w), ui.Dim("(not set)"))
			}

			io.Printf("%s %v\n", ui.Label("useSSHIdentityFile:", w), rc.UseSSHIdentityFile)
			io.Printf("%s %v\n", ui.Label("telemetry:", w), telemetryEnabled)

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
		// Built from the same maps the unknown-key error reads, so the keys the
		// help lists cannot drift from the ones the command accepts. Bare
		// "config set" prints this, making it the primary listing a user sees.
		Long: fmt.Sprintf(
			"Set a config value. Use 'chunk auth set <provider>' to store credentials with validation.\n\nUser keys: %s\nProject keys: %s",
			configKeyList(config.ValidConfigKeys), configKeyList(config.ValidProjectConfigKeys),
		),
		Args: configSetArgs,
		// configSetArgs names what is missing and lists the keys, so cobra's
		// usage dump ahead of it would only bury the part worth reading.
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Bare "config set" is a request to see how the command works, not a
			// failed attempt to set something, so it prints help and succeeds —
			// matching "chunk config". A partial invocation still errors.
			if len(args) == 0 {
				return cmd.Help()
			}

			io := iostream.FromCmd(cmd)
			key, value := args[0], args[1]

			if config.ValidProjectConfigKeys[key] {
				workDir, err := os.Getwd()
				if err != nil {
					return err
				}
				projCfg, err := config.LoadProjectConfigForUpdate(workDir)
				if err != nil {
					return malformedProjectConfigError(err)
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
				reportUnknownKeys(io, ".chunk/config.json", projCfg.UnknownKeys())
				return nil
			}

			if !config.ValidConfigKeys[key] {
				return &userError{
					msg:    fmt.Sprintf("Unknown config key: %q.", key),
					detail: fmt.Sprintf("Supported keys: %s, %s.", configKeyList(config.ValidConfigKeys), configKeyList(config.ValidProjectConfigKeys)),
					errMsg: fmt.Sprintf("unknown config key %q", key),
				}
			}

			cfg, err := config.Load()
			if err != nil {
				return malformedUserConfigError(msgCouldNotLoadConfig, err)
			}

			switch key {
			case "model":
				cfg.Model = value
			case "useSSHIdentityFile":
				switch value {
				case "true", "1":
					cfg.UseSSHIdentityFile = true
				case "false", "0":
					cfg.UseSSHIdentityFile = false
				default:
					return &userError{
						msg:    fmt.Sprintf("Invalid value %q for useSSHIdentityFile.", value),
						detail: "Accepted values: true, false.",
						errMsg: fmt.Sprintf("invalid boolean value %q", value),
					}
				}
			case "telemetry":
				switch value {
				case "true", "1":
					enabled := true
					cfg.Telemetry = &enabled
				case "false", "0":
					enabled := false
					cfg.Telemetry = &enabled
				default:
					return &userError{
						msg:    fmt.Sprintf("Invalid value %q for telemetry.", value),
						detail: "Accepted values: true, false.",
						errMsg: fmt.Sprintf("invalid boolean value %q", value),
					}
				}
			}

			// Save refuses a file it could not parse rather than flattening it, so
			// this reports the parse failure the same way the load above does.
			if err := config.Save(cfg); err != nil {
				return malformedUserConfigError("Could not save configuration.", err)
			}

			io.Printf("%s\n", ui.Success(fmt.Sprintf("Set %s to %s", key, value)))
			reportUnknownKeys(io, "the chunk config file", cfg.UnknownKeys())
			return nil
		},
	}
}

// reportUnknownKeys notes the keys a write kept but does not recognize. They are
// preserved either way; saying so is what keeps a typo from silently doing
// nothing forever.
func reportUnknownKeys(io iostream.Streams, file string, keys []string) {
	if len(keys) == 0 {
		return
	}
	noun := "keys"
	if len(keys) == 1 {
		noun = "key"
	}
	io.ErrPrintln(ui.Dim(fmt.Sprintf("Kept %d unrecognized %s in %s: %s",
		len(keys), noun, file, strings.Join(keys, ", "))))
}

// configSetArgs accepts a key and a value, or nothing at all — no arguments is a
// request for help, handled in RunE. One argument or three is a real mistake, and
// cobra.ExactArgs would report it as "accepts 2 arg(s), received 1", which carries
// no user-facing message and so surfaces as "An unknown error occurred" — a dead
// end for someone who just forgot an argument. Naming what is missing and listing
// the keys gives them the next step instead.
func configSetArgs(_ *cobra.Command, args []string) error {
	if len(args) == 0 || len(args) == 2 {
		return nil
	}
	msg := fmt.Sprintf("Too many arguments: expected a key and a value, got %d.", len(args))
	if len(args) == 1 {
		msg = fmt.Sprintf("Missing value for %q.", args[0])
	}
	return newUserError(msg).
		withCode("config.set_args").
		withDetail(fmt.Sprintf("Project keys: %s\nUser keys: %s",
			configKeyList(config.ValidProjectConfigKeys),
			configKeyList(config.ValidConfigKeys))).
		withSuggestion("Run 'chunk config set <key> <value>', for example: chunk config set orgID <your-org-id>").
		wrapMsg(fmt.Sprintf("config set accepts 2 args, received %d", len(args)))
}

// configKeyList renders a key-validity map as a sorted, comma-joined list, so
// the keys quoted in help and error text cannot drift from the ones "config set"
// actually accepts.
func configKeyList(keys map[string]bool) string {
	out := make([]string, 0, len(keys))
	for k := range keys {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
