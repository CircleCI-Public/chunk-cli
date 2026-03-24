package cmd

import (
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
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
			io := iostream.FromCmd(cmd)
			rc := config.Resolve("", "")

			if rc.APIKey == "" {
				io.Println("Not authenticated")
				return nil
			}

			io.Printf("Authenticated: %s (source: %s)\n",
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
			io := iostream.FromCmd(cmd)
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if cfg.APIKey == "" {
				io.Println("No API key stored in config file")
				return nil
			}
			if err := config.ClearAPIKey(); err != nil {
				return err
			}
			io.Println("API key removed from config file")
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
