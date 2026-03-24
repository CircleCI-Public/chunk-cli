package upgrade

import (
	"fmt"
	"os/exec"
)

func Run() error {
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return fmt.Errorf("gh CLI not found. Install it from https://cli.github.com")
	}

	// Check gh auth status
	cmd := exec.Command(ghPath, "auth", "status")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh is not authenticated. Run: gh auth login")
	}

	// Run the upgrade
	upgradeCmd := exec.Command(ghPath, "extension", "upgrade", "circleci-public/chunk-cli")
	upgradeCmd.Stdout = nil
	upgradeCmd.Stderr = nil
	if err := upgradeCmd.Run(); err != nil {
		return fmt.Errorf("upgrade failed: %w", err)
	}

	return nil
}
