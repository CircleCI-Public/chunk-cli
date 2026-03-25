package hook

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const envMarker = "# chunk-hook env"

// shellQuote escapes a string for safe embedding inside single quotes
// in POSIX shell. The single quote itself is handled by ending the
// current string, inserting an escaped quote, and reopening.
func shellQuote(s string) string {
	return strings.ReplaceAll(s, "'", `'\''`)
}

// defaultShellStartupFiles returns the shell startup files for the
// current user's shell. Both login and interactive files are updated
// so config applies to new terminal tabs as well as subshells.
func defaultShellStartupFiles() []string {
	shell := filepath.Base(os.Getenv("SHELL"))
	home, _ := os.UserHomeDir()

	switch shell {
	case "zsh":
		return []string{
			filepath.Join(home, ".zprofile"),
			filepath.Join(home, ".zshrc"),
		}
	case "bash":
		login := filepath.Join(home, ".profile")
		if runtime.GOOS == "darwin" {
			login = filepath.Join(home, ".bash_profile")
		}
		return []string{login, filepath.Join(home, ".bashrc")}
	default:
		return []string{filepath.Join(home, ".profile")}
	}
}

// ensureLoginSourcing adds (or updates) a sourcing block for envFile
// in each of the given shell startup files. If startupFiles is nil,
// the auto-detected defaults are used. Returns the list of files updated.
func ensureLoginSourcing(envFile string, startupFiles []string) []string {
	if startupFiles == nil {
		startupFiles = defaultShellStartupFiles()
	}
	sourceLine := "if [ -f '" + shellQuote(envFile) + "' ]; then . '" + shellQuote(envFile) + "'; fi"

	updated := make([]string, 0, len(startupFiles))
	for _, f := range startupFiles {
		upsertManagedBlock(f, envMarker, sourceLine)
		updated = append(updated, f)
	}
	return updated
}

// upsertManagedBlock idempotently inserts or updates a two-line block
// (markerLine + valueLine) in a shell startup file. If markerLine
// already exists, the marker and the line immediately following it are
// replaced. Otherwise the block is appended.
func upsertManagedBlock(filePath, markerLine, valueLine string) {
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		dir := filepath.Dir(filePath)
		_ = os.MkdirAll(dir, 0o755)
		_ = os.WriteFile(filePath, []byte("\n"+markerLine+"\n"+valueLine+"\n"), 0o644)
		return
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return
	}

	lines := strings.Split(string(data), "\n")
	var result []string
	found := false
	skipNext := false

	for _, line := range lines {
		if skipNext {
			skipNext = false
			continue
		}
		if line == markerLine {
			result = append(result, markerLine, valueLine)
			found = true
			skipNext = true
			continue
		}
		result = append(result, line)
	}

	if !found {
		if len(result) > 0 && result[len(result)-1] != "" {
			result = append(result, "")
		}
		result = append(result, markerLine, valueLine)
	}

	_ = os.WriteFile(filePath, []byte(strings.Join(result, "\n")), 0o644)
}
