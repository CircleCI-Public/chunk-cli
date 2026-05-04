package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/CircleCI-Public/chunk-cli/internal/cmd"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	appversion "github.com/CircleCI-Public/chunk-cli/internal/version"
)

var version = "dev"

func main() {
	signal.Ignore(syscall.SIGPIPE)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	rewriteColonSyntax()
	appversion.Value = version

	rootCmd := cmd.NewRootCmd(version)
	rootCmd.SetContext(ctx)
	err := rootCmd.Execute()
	stop() // release signal resources before any os.Exit
	if err != nil {
		// ExitCode errors have already written their output; exit without
		// printing further error text. Only errors returned after all output
		// has been written should implement ExitCode().
		if ec, ok := err.(interface{ ExitCode() int }); ok {
			os.Exit(ec.ExitCode())
		}
		m, d, s, exitCode := errorDetails(err)
		if jsonFlagPresent() {
			type jsonErr struct {
				Error      bool   `json:"error"`
				Code       string `json:"code,omitempty"`
				Message    string `json:"message"`
				Detail     string `json:"detail,omitempty"`
				Suggestion string `json:"suggestion,omitempty"`
			}
			b, jsonMarshalErr := json.MarshalIndent(jsonErr{Error: true, Code: errorCode(err), Message: m, Detail: d, Suggestion: s}, "", "  ")
			if jsonMarshalErr != nil {
				_, _ = fmt.Fprint(os.Stderr, ui.FormatError(m, d, s))
			} else {
				_, _ = fmt.Fprintf(os.Stderr, "%s\n", b)
			}
		} else {
			_, _ = fmt.Fprint(os.Stderr, ui.FormatError(m, d, s))
		}
		os.Exit(exitCode)
	}
}

func errorDetails(err error) (msg, detail, suggestion string, exitCode int) {
	msg = "An unknown error occurred."
	detail = err.Error()
	suggestion = errorSuggestion(err)
	exitCode = 1
	if um, ok := err.(interface{ UserMessage() string }); ok {
		msg = um.UserMessage()
	}
	if d, ok := err.(interface{ Detail() string }); ok && d.Detail() != "" {
		detail = d.Detail()
	}
	if s, ok := err.(interface{ Suggestion() string }); ok && s.Suggestion() != "" {
		suggestion = s.Suggestion()
	}
	if ec, ok := err.(interface{ UserExitCode() int }); ok {
		exitCode = ec.UserExitCode()
	}
	return
}

// errorCode extracts the namespaced error code from err, if present.
func errorCode(err error) string {
	if ec, ok := err.(interface{ ErrorCode() string }); ok {
		return ec.ErrorCode()
	}
	return ""
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
