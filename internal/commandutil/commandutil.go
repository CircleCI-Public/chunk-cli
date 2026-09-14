package commandutil

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
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
// paths whose source files appear in `git diff HEAD`.
// Expands to "./..." when no .go files changed.
func ExpandCommand(workDir, command string) string {
	if !strings.Contains(command, "{{CHANGED_PACKAGES}}") {
		return command
	}

	out, err := exec.Command("git", "-C", workDir, "diff", "HEAD", "--name-only").Output()
	if err != nil {
		return strings.ReplaceAll(command, "{{CHANGED_PACKAGES}}", "./...")
	}

	seen := map[string]bool{}
	var pkgs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" || !strings.HasSuffix(line, ".go") {
			continue
		}
		pkg := "./" + filepath.Dir(line)
		if !seen[pkg] {
			seen[pkg] = true
			pkgs = append(pkgs, pkg)
		}
	}

	expanded := "./..."
	if len(pkgs) > 0 {
		expanded = strings.Join(pkgs, " ")
	}
	return strings.ReplaceAll(command, "{{CHANGED_PACKAGES}}", expanded)
}
