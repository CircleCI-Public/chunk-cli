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
	return &cobra.Command{
		Use:   "show",
		Short: "Display resolved configuration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			rc := config.Resolve("", "")

			w := 15
			io.Printf("%s %s %s\n", ui.Label("model:", w), rc.Model, ui.Dim("("+rc.ModelSource+")"))

			if rc.APIKey != "" {
				io.Printf("%s %s %s\n", ui.Label("apiKey:", w), config.MaskAPIKey(rc.APIKey), ui.Dim("("+rc.APIKeySource+")"))
			} else {
				io.Printf("%s %s\n", ui.Label("apiKey:", w), ui.Dim("(not set)"))
			}

			io.Printf("%s %s\n", ui.Label("analyzeModel:", w), rc.AnalyzeModel)
			io.Printf("%s %s\n", ui.Label("promptModel:", w), rc.PromptModel)
			return nil
		},
	}
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			key, value := args[0], args[1]

			if !config.ValidConfigKeys[key] {
				io.ErrPrintf("%s\n", ui.Warning(fmt.Sprintf("Unknown config key: %q", key)))
				os.Exit(2)
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			switch key {
			case "model":
				cfg.Model = value
			case "apiKey":
				cfg.APIKey = value
			}

			if err := config.Save(cfg); err != nil {
				return err
			}

			io.Printf("%s\n", ui.Success(fmt.Sprintf("Set %s to %s", key, value)))
			return nil
		},
	}
}
