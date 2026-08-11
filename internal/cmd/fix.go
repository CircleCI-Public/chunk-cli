package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

func newFixCmd() *cobra.Command {
	var projectDir string

	cmd := &cobra.Command{
		Use:          "fix [name]",
		Short:        "Run fix commands (formatters and other file rewrites)",
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			streams := iostream.FromCmd(cmd)

			workDir := projectDir
			if workDir == "" {
				var err error
				workDir, err = os.Getwd()
				if err != nil {
					return err
				}
			}

			cfg, err := config.LoadProjectConfig(workDir)
			if err != nil || !cfg.HasFixCommands() {
				return &userError{
					msg:        "No fix commands configured.",
					suggestion: "Add fix commands to .chunk/config.json under the \"fix\" key.",
					hideDetail: true,
				}
			}

			var name string
			if len(args) == 1 {
				name = args[0]
			}

			statusFn := newStatusFunc(streams)
			return validate.RunFix(cmd.Context(), workDir, name, cfg.Fix, statusFn, streams)
		},
	}

	cmd.Flags().StringVar(&projectDir, "project", "", "Override project directory")
	return cmd
}
