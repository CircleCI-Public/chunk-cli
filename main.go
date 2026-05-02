package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/cmd"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	appversion "github.com/CircleCI-Public/chunk-cli/internal/version"
)

var version = "dev"

func main() {
	rewriteColonSyntax()
	appversion.Value = version

	rootCmd := cmd.NewRootCmd(version)
	if err := rootCmd.Execute(); err != nil {
		// ExitCode errors have already written their output; exit without
		// printing further error text. Only errors returned after all output
		// has been written should implement ExitCode().
		if ec, ok := err.(interface{ ExitCode() int }); ok {
			os.Exit(ec.ExitCode())
		}
		m, d, s := errorDetails(err)
		if jsonFlagPresent() {
			type jsonErr struct {
				Error      bool   `json:"error"`
				Message    string `json:"message"`
				Detail     string `json:"detail,omitempty"`
				Suggestion string `json:"suggestion,omitempty"`
			}
			b, jsonMarshalErr := json.MarshalIndent(jsonErr{Error: true, Message: m, Detail: d, Suggestion: s}, "", "  ")
			if jsonMarshalErr != nil {
				_, _ = fmt.Fprint(os.Stderr, ui.FormatError(m, d, s))
			} else {
				_, _ = fmt.Fprintf(os.Stderr, "%s\n", b)
			}
		} else {
			_, _ = fmt.Fprint(os.Stderr, ui.FormatError(m, d, s))
		}
		os.Exit(1)
	}
}

func errorDetails(err error) (msg, detail, suggestion string) {
	msg = "An unknown error occurred."
	detail = err.Error()
	suggestion = errorSuggestion(err)
	if um, ok := err.(interface{ UserMessage() string }); ok {
		msg = um.UserMessage()
	}
	if d, ok := err.(interface{ Detail() string }); ok && d.Detail() != "" {
		detail = d.Detail()
	}
	if s, ok := err.(interface{ Suggestion() string }); ok && s.Suggestion() != "" {
		suggestion = s.Suggestion()
	}
	return msg, detail, suggestion
}

// errorSuggestion returns a contextual hint for common error patterns.
func errorSuggestion(err error) string {
	msg := err.Error()
	lower := strings.ToLower(msg)

	switch {
	case strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "invalid api key") ||
		strings.Contains(lower, "401"):
		return "Hint: Run `chunk auth set anthropic` to set up your API key."
	case strings.Contains(lower, "no such host") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "network is unreachable") ||
		strings.Contains(lower, "dial tcp"):
		return "Hint: Check your internet connection."
	}
	return ""
}

// jsonFlagPresent reports whether --json appears in the raw argument list.
// Used to format errors as JSON when the flag is set, before cobra has run.
func jsonFlagPresent() bool {
	for _, arg := range os.Args[1:] {
		if arg == "--" {
			break
		}
		if arg == "--json" || arg == "--json=true" {
			return true
		}
	}
	return false
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
