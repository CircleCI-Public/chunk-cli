package cmd

import (
	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

func newCommandsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "commands",
		Short: "List all available commands",
		RunE: func(cmd *cobra.Command, _ []string) error {
			io := iostream.FromCmd(cmd)
			root := cmd.Root()

			var maxWidth int
			walkCommands(root, func(c *cobra.Command) {
				if w := len(c.CommandPath()); w > maxWidth {
					maxWidth = w
				}
			})

			walkCommands(root, func(c *cobra.Command) {
				io.Printf("%-*s  %s\n", maxWidth, c.CommandPath(), c.Short)
			})
			return nil
		},
	}
}

func walkCommands(cmd *cobra.Command, fn func(*cobra.Command)) {
	if cmd.Hidden {
		return
	}
	fn(cmd)
	for _, c := range cmd.Commands() {
		if !c.IsAvailableCommand() && c.Name() != "help" {
			continue
		}
		walkCommands(c, fn)
	}
}
