package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/spf13/cobra/doc"

	"github.com/CircleCI-Public/chunk-cli/internal/closer"
	"github.com/CircleCI-Public/chunk-cli/internal/telemetry"
)

func newManCmd() *cobra.Command {
	var outputPath string
	cmd := &cobra.Command{
		Use:    "man",
		Short:  "Generate man page",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if outputPath == "" {
				return fmt.Errorf("--output is required")
			}
			if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
				return fmt.Errorf("create output directory: %w", err)
			}
			f, err := os.Create(outputPath) //#nosec:G304 // path is user-supplied
			if err != nil {
				return fmt.Errorf("create output file: %w", err)
			}
			defer closer.ErrorHandler(f, &err)
			header := &doc.GenManHeader{
				Title:   "CHUNK",
				Section: "1",
			}
			return doc.GenMan(cmd.Root(), header, f)
		},
	}
	telemetry.DisableTelemetry(cmd)
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "Write man page to this file (required)")
	return cmd
}
