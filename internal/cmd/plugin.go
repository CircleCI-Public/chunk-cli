package cmd

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/smeltdev/smelt/internal/brand"
	"github.com/smeltdev/smelt/internal/version"
)

// pluginPrefix is what a program on PATH is named to be run as `smelt <name>`.
const pluginPrefix = brand.Command + "-"

// pluginNamePattern is what a plugin's name may look like. It keeps path
// separators and dots out, so the name cannot leave PATH's directories.
var pluginNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// pluginName is the plugin a command line asks for: its first argument, when
// that is a well-formed name.
func pluginName(args []string) (string, bool) {
	if len(args) == 0 || !pluginNamePattern.MatchString(args[0]) {
		return "", false
	}
	return args[0], true
}

// findPlugin is the executable smelt-<name> in the first directory of path
// that has one, or "". Only absolute directories count: an empty or relative
// entry would make the current directory a source of programs.
func findPlugin(name, path string) string {
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, pluginPrefix+name)
		if isExecutableFile(candidate) {
			return candidate
		}
	}
	return ""
}

func isExecutableFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// listPlugins is the names of the plugins on path, sorted. A name that is a
// built-in command is left out, since the built-in wins.
func listPlugins(root *cobra.Command, path string) []string {
	var names []string
	for _, dir := range filepath.SplitList(path) {
		if !filepath.IsAbs(dir) {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name, ok := strings.CutPrefix(e.Name(), pluginPrefix)
			if !ok || !pluginNamePattern.MatchString(name) || isBuiltin(root, name) {
				continue
			}
			if findPlugin(name, path) != "" {
				names = append(names, name)
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// isBuiltin reports whether name is a command of root's.
func isBuiltin(root *cobra.Command, name string) bool {
	cmd, _, err := root.Find([]string{name})
	return err == nil && cmd != root
}

// pluginsHelp is the help section listing the plugins on path, or "".
func pluginsHelp(root *cobra.Command, path string) string {
	names := listPlugins(root, path)
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nPlugins:\n")
	for _, n := range names {
		fmt.Fprintf(&b, "  %s %s\n", brand.Command, n)
	}
	return b.String()
}

// exitCodeError ends the process with a plugin's exit code, its output
// already written.
type exitCodeError struct{ code int }

func (e exitCodeError) Error() string { return fmt.Sprintf("plugin exited with code %d", e.code) }

// ExitCode is the code the process exits with.
func (e exitCodeError) ExitCode() int { return e.code }

// dispatchPlugin runs the plugin args name, when args name one and no
// built-in command of that name exists. It reports whether it ran one.
func dispatchPlugin(root *cobra.Command, args []string, path string) (bool, error) {
	name, ok := pluginName(args)
	if !ok || isBuiltin(root, name) {
		return false, nil
	}
	bin := findPlugin(name, path)
	if bin == "" {
		return false, nil
	}
	return true, runPlugin(bin, args[1:], root)
}

// runPlugin runs the program bin with args on the caller's terminal, and
// passes its exit code on.
func runPlugin(bin string, args []string, root *cobra.Command) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find the running %s: %w", brand.Command, err)
	}
	c := exec.Command(bin, args...) //nolint:gosec // bin was found on PATH by findPlugin
	c.Stdin, c.Stdout, c.Stderr = root.InOrStdin(), root.OutOrStdout(), root.ErrOrStderr()
	c.Env = append(os.Environ(), brand.Env("BIN")+"="+self, brand.Env("VERSION")+"="+version.Value)
	err = c.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &exit):
		return exitCodeError{code: exit.ExitCode()}
	}
	return fmt.Errorf("run %s: %w", filepath.Base(bin), err)
}

// usageError reports a mistake in how a command was called, which cobra
// found before the command started.
func usageError(err error, args []string) error {
	msg := err.Error()
	if name, ok := pluginName(args); ok && strings.HasPrefix(msg, "unknown command") {
		msg += fmt.Sprintf("\nNo %s%s plugin is installed either.", pluginPrefix, name)
	}
	return newUserError(msg).
		withCode("command.invalid_args").
		withExitCode(ExitBadArgs).
		withoutDetail()
}
