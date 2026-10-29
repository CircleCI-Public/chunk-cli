// Package clipboard puts text on the developer's clipboard from a terminal
// screen. The terminal's own escape sequence for it is blocked by many
// terminals, with nothing to say so, so where the machine has a clipboard
// tool, that is used as well.
package clipboard

import (
	"os"
	"os/exec"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// Copy is a command that puts text on the clipboard, both through the
// terminal and through the machine's clipboard tool when it has one.
func Copy(text string) tea.Cmd {
	return tea.Batch(tea.SetClipboard(text), viaTool(text))
}

// viaTool pipes text to the machine's clipboard tool, if there is one. Failing
// is quiet: the terminal's escape sequence went too, and the developer has no
// way to act on it.
func viaTool(text string) tea.Cmd {
	argv := tool(runtime.GOOS, hasInPath, os.Getenv)
	if argv == nil {
		return nil
	}
	return func() tea.Msg {
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Stdin = strings.NewReader(text)
		_ = cmd.Run()
		return nil
	}
}

// tool is the command that reads the clipboard's new text from its input on
// this system, or nil when it has none the screen can use. Over ssh the
// machine's clipboard is not the developer's, so only the terminal's
// sequence reaches it.
func tool(goos string, has func(string) bool, env func(string) string) []string {
	if env("SSH_CONNECTION") != "" || env("SSH_TTY") != "" {
		return nil
	}
	switch goos {
	case "darwin":
		return first(has, []string{"pbcopy"})
	case "windows":
		return first(has, []string{"clip.exe"})
	}
	// Elsewhere which tool there is depends on the display server.
	if env("WAYLAND_DISPLAY") != "" {
		if argv := first(has, []string{"wl-copy"}); argv != nil {
			return argv
		}
	}
	if env("DISPLAY") == "" {
		return nil
	}
	return first(has, []string{"xclip", "-selection", "clipboard"}, []string{"xsel", "--clipboard", "--input"})
}

// first is the first of the commands whose program is installed.
func first(has func(string) bool, argvs ...[]string) []string {
	for _, argv := range argvs {
		if has(argv[0]) {
			return argv
		}
	}
	return nil
}

func hasInPath(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
