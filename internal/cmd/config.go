package cmd

import (
	"fmt"
	"os"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/spf13/cobra"
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
			rc := config.Resolve("", "")

			fmt.Fprintf(cmd.OutOrStdout(), "model: %s (%s)\n", rc.Model, rc.ModelSource)

			if rc.APIKey != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "apiKey: %s (%s)\n", config.MaskAPIKey(rc.APIKey), rc.APIKeySource)
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "apiKey: (not set)")
			}

			fmt.Fprintf(cmd.OutOrStdout(), "analyzeModel: %s\n", rc.AnalyzeModel)
			fmt.Fprintf(cmd.OutOrStdout(), "promptModel: %s\n", rc.PromptModel)
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
			key, value := args[0], args[1]

			if !config.ValidConfigKeys[key] {
				fmt.Fprintf(cmd.ErrOrStderr(), "Unknown config key: %q\n", key)
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
