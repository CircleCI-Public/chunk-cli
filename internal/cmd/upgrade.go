package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/upgrade"
	"github.com/CircleCI-Public/chunk-cli/internal/version"
)

func newUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade chunk to the latest version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			streams := iostream.FromCmd(cmd)

			apiBase := os.Getenv(config.EnvGitHubAPIURL)
			if apiBase == "" {
				apiBase = "https://api.github.com"
			}

			// Not resolved through symlinks: Run needs to see one, because the
			// symlink task dev-install makes marks a source build.
			installPath := os.Getenv("CHUNK_INSTALL_PATH")
			if installPath == "" {
				execPath, err := os.Executable()
				if err != nil {
					return fmt.Errorf("find executable: %w", err)
				}
				installPath = execPath
			}

			err := upgrade.Run(upgrade.Options{
				Out:         streams.Out,
				Client:      http.DefaultClient,
				APIBase:     apiBase,
				InstallPath: installPath,
				Version:     version.Value,
			})
			return upgradeError(err)
		},
	}
}

// upgradeError gives the two deliberate refusals a message and a way forward;
// anything else is reported as it is.
func upgradeError(err error) error {
	switch {
	case errors.Is(err, upgrade.ErrSourceBuild):
		return newUserError("This chunk is a source build, so there is no release to upgrade to.").
			withSuggestion("Update it with: git pull && task build").
			wrap(err)
	case errors.Is(err, upgrade.ErrHomebrewManaged):
		return newUserError("This chunk was installed by Homebrew, which is not this project's build.").
			withSuggestion("Install this project's release with: " + upgrade.InstallCommand).
			wrap(err)
	}
	return err
}
