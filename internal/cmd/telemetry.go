package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

func newTelemetryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telemetry",
		Short: "Manage telemetry settings",
		RunE:  groupRunE,
	}

	enableCmd := newTelemetryEnableCmd()
	disableCmd := newTelemetryDisableCmd()

	disableTelemetry(enableCmd)
	disableTelemetry(disableCmd)

	cmd.AddCommand(enableCmd)
	cmd.AddCommand(disableCmd)

	return cmd
}

func newTelemetryEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable",
		Short: "Enable anonymous usage telemetry",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			cfg, err := config.Load()
			if err != nil {
				return &userError{msg: msgCouldNotLoadConfig, suggestion: configFilePermHint, err: err}
			}
			cfg.NoTelemetry = false
			if err := config.Save(cfg); err != nil {
				return &userError{msg: msgCouldNotSaveConfig, suggestion: configFilePermHint, err: err}
			}
			io.Printf("%s\n", ui.Success("Telemetry enabled."))
			return nil
		},
	}
}

func newTelemetryDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Disable anonymous usage telemetry",
		Long: fmt.Sprintf(`Disable anonymous usage telemetry.

You can also set %s=1 in your environment, or pass --no-telemetry
to disable telemetry for a single invocation.

Run 'chunk telemetry enable' to re-enable.`, config.EnvChunkNoTelemetry),
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			cfg, err := config.Load()
			if err != nil {
				return &userError{msg: msgCouldNotLoadConfig, suggestion: configFilePermHint, err: err}
			}
			cfg.NoTelemetry = true
			if err := config.Save(cfg); err != nil {
				return &userError{msg: msgCouldNotSaveConfig, suggestion: configFilePermHint, err: err}
			}
			io.Printf("%s\n", ui.Success("Telemetry disabled."))
			return nil
		},
	}
}
