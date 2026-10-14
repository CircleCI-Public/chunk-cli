package commandutil

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

func FormatElapsed(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

func SkipRemaining(status iostream.StatusFunc, remaining []config.Command, width int) {
	for _, c := range remaining {
		status(iostream.LevelWarn, fmt.Sprintf("%-*s  skipped", width, c.Name))
	}
}

func NameWidth(commands []config.Command) int {
	w := 0
	for _, c := range commands {
		if len(c.Name) > w {
			w = len(c.Name)
		}
	}
	return w
}

// ExpandCommand replaces template variables in command before execution.
// {{CHANGED_PACKAGES}} expands to the space-separated list of Go package
// paths whose source files appear in `git diff HEAD`, leaving out packages
// the change deleted, since go test fails on a directory that is gone, and
// packages in a nested Go module, which a command run from workDir cannot name.
// Expands to "./..." when no such packages changed.
func ExpandCommand(workDir, command string) string {
	return ExpandCommandCtx(context.Background(), workDir, command)
}

// ExpandCommandCtx is ExpandCommand with cancellation support for git queries.
func ExpandCommandCtx(ctx context.Context, workDir, command string) string {
	if !strings.Contains(command, "{{CHANGED_PACKAGES}}") {
		return command
	}

	out, err := (gitexec.Runner{Dir: workDir}).Output(ctx, "diff", "HEAD", "--name-only")
	if err != nil {
		return strings.ReplaceAll(command, "{{CHANGED_PACKAGES}}", "./...")
	}

	seen := map[string]bool{}
	var pkgs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" || !strings.HasSuffix(line, ".go") {
			continue
		}
		dir := filepath.Dir(line)
		pkg := "./" + dir
		if seen[pkg] {
			continue
		}
		seen[pkg] = true
		if inNestedModule(workDir, dir) {
			continue
		}
		if goFiles, _ := filepath.Glob(filepath.Join(workDir, dir, "*.go")); len(goFiles) > 0 {
			pkgs = append(pkgs, pkg)
		}
	}

	expanded := "./..."
	if len(pkgs) > 0 {
		expanded = strings.Join(pkgs, " ")
	}
	return strings.ReplaceAll(command, "{{CHANGED_PACKAGES}}", expanded)
}

// inNestedModule reports whether dir, relative to workDir, belongs to a Go
// module of its own: a go.mod sits between it and workDir. go test run from
// workDir fails on such a package, as it is outside the main module.
func inNestedModule(workDir, dir string) bool {
	for d := dir; d != "." && d != string(filepath.Separator); d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(workDir, d, "go.mod")); err == nil {
			return true
		}
	}
	return false
}
