package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
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
		RunE: func(cmd *cobra.Command, args []string) error {
			io := iostream.FromCmd(cmd)
			rc := config.Resolve("", "")

			io.Printf("model: %s (%s)\n", rc.Model, rc.ModelSource)

			if rc.APIKey != "" {
				io.Printf("apiKey: %s (%s)\n", config.MaskAPIKey(rc.APIKey), rc.APIKeySource)
			} else {
				io.Println("apiKey: (not set)")
			}

			io.Printf("analyzeModel: %s\n", rc.AnalyzeModel)
			io.Printf("promptModel: %s\n", rc.PromptModel)
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
				io.ErrPrintf("Unknown config key: %q\n", key)
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

			return config.Save(cfg)
		},
	}
}
