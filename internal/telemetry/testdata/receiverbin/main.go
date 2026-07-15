// Command receiverbin is a minimal stand-in for the chunk binary's hidden
// "receive-telemetry" subcommand, built by TestMain in delegate_test.go. It
// has no other subcommands, so pointing delegateDestination.bin at it lets
// tests exercise the real subprocess spawn/stdin/env-var path without any
// risk of the "go test" binary re-execing itself.
package main

import (
	"os"

	"github.com/CircleCI-Public/chunk-cli/internal/telemetry/receiver"
)

func main() {
	if err := receiver.Receive(os.Stdin); err != nil {
		os.Exit(1)
	}
}
