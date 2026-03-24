package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/CircleCI-Public/chunk-cli/internal/cmd"
	"github.com/CircleCI-Public/chunk-cli/internal/usererr"
)

var version = "dev"

func main() {
	rootCmd := cmd.NewRootCmd(version)
	if err := rootCmd.Execute(); err != nil {
		var ue *usererr.Error
		if errors.As(err, &ue) {
			fmt.Fprintln(os.Stderr, ue.UserMessage())
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
}
