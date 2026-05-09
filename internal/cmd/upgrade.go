package cmd

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/upgrade"
)

func newUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade chunk to the latest version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			streams := iostream.FromCmd(cmd)

			apiBase := os.Getenv("GITHUB_API_URL")
			if apiBase == "" {
				apiBase = "https://api.github.com"
			}

			installPath := os.Getenv("CHUNK_INSTALL_PATH")
			if installPath == "" {
				execPath, err := os.Executable()
				if err != nil {
					return fmt.Errorf("find executable: %w", err)
				}
				execPath, err = filepath.EvalSymlinks(execPath)
				if err != nil {
					return fmt.Errorf("resolve executable path: %w", err)
				}
				installPath = execPath
			}

			return upgrade.Run(streams.Out, http.DefaultClient, apiBase, installPath)
		},
	}
}
