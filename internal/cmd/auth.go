package cmd

import (
	"fmt"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/spf13/cobra"
)

func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication",
	}
	cmd.AddCommand(newAuthStatusCmd())
	cmd.AddCommand(newAuthLogoutCmd())
	return cmd
}

func newAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check authentication status",
		RunE: func(cmd *cobra.Command, args []string) error {
			rc := config.Resolve("", "")

			if rc.APIKey == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "Not authenticated")
				return nil
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Authenticated: %s (source: %s)\n",
				config.MaskAPIKey(rc.APIKey), sourceLabel(rc.APIKeySource))
			return nil
		},
	}
}

func newAuthLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Clear stored API key",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.APIKey == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "No API key stored in config file")
				return nil
			}
			if err := config.ClearAPIKey(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "API key removed from config file")
			return nil
		},
	}
}

func sourceLabel(source string) string {
	switch source {
	case "Config file (user config)":
		return "Config file"
	case "Environment variable":
		return "Environment variable"
	default:
		return source
	}
}
