package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/cmd"
	"github.com/CircleCI-Public/chunk-cli/internal/usererr"
)

var version = "dev"

func main() {
	rewriteColonSyntax()

	rootCmd := cmd.NewRootCmd(version)
	if err := rootCmd.Execute(); err != nil {
		if !errors.Is(err, cmd.ErrSilent) {
			var ue *usererr.Error
			if errors.As(err, &ue) {
				fmt.Fprintln(os.Stderr, ue.UserMessage())
			} else {
				fmt.Fprintln(os.Stderr, err)
			}
			if suggestion := errorSuggestion(err); suggestion != "" {
				fmt.Fprintln(os.Stderr, suggestion)
			}
		}
		os.Exit(1)
	}
}

// errorSuggestion returns a contextual hint for common error patterns.
func errorSuggestion(err error) string {
	msg := err.Error()
	lower := strings.ToLower(msg)

	switch {
	case strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "invalid api key") ||
		strings.Contains(lower, "401"):
		return "Hint: Run `chunk auth login` to set up your API key."
	case strings.Contains(lower, "no such host") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "network is unreachable") ||
		strings.Contains(lower, "dial tcp"):
		return "Hint: Check your internet connection."
	}
	return ""
}

// rewriteColonSyntax rewrites "validate:name" to "validate" "name" in os.Args
// before cobra parses, matching the TypeScript CLI's colon syntax support.
func rewriteColonSyntax() {
	for i, arg := range os.Args {
		if strings.HasPrefix(arg, "validate:") {
			name := strings.TrimPrefix(arg, "validate:")
			if name == "" {
				continue
			}
			newArgs := make([]string, 0, len(os.Args)+1)
			newArgs = append(newArgs, os.Args[:i]...)
			newArgs = append(newArgs, "validate", name)
			newArgs = append(newArgs, os.Args[i+1:]...)
			os.Args = newArgs
			return
		}
	}
}
