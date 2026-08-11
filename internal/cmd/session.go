package cmd

import (
	"encoding/json"
	"os"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

func newSessionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "session",
		Short:  "Manage Claude Code session state",
		Hidden: true,
	}
	cmd.AddCommand(newSessionStartCmd())
	return cmd
}

func newSessionStartCmd() *cobra.Command {
	var projectDir string

	c := &cobra.Command{
		Use:          "start",
		Short:        "Record the current Claude Code session ID (called by the SessionStart hook)",
		SilenceUsage: true,
		Hidden:       true,
		RunE: func(cmd *cobra.Command, args []string) error {
			var payload struct {
				SessionID string `json:"session_id"`
			}
			if err := json.NewDecoder(cmd.InOrStdin()).Decode(&payload); err != nil || payload.SessionID == "" {
				return nil
			}
			workDir := projectDir
			if workDir == "" {
				var err error
				workDir, err = os.Getwd()
				if err != nil {
					return err
				}
			}
			return sidecar.SaveSessionID(workDir, payload.SessionID)
		},
	}
	c.Flags().StringVar(&projectDir, "project", "", "Override project directory")
	return c
}
