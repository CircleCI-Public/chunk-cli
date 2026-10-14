package settings

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// ConfirmFunc asks the user whether to apply diff to the file at path, which
// is relative to the project directory.
type ConfirmFunc func(path, diff string) (bool, error)

// Outcome is what writing an agent's hook file did.
type Outcome int

const (
	// Created means the file did not exist and was written.
	Created Outcome = iota
	// Updated means the user agreed to merge chunk's hooks into the file.
	Updated
	// UpToDate means the file already had chunk's hooks.
	UpToDate
	// Declined means the user chose to leave the file as it was.
	Declined
	// WroteExample means the user could not be asked, so the file was left as
	// it was and chunk's version written beside it.
	WroteExample
)

// WriteResult says what writing an agent's hook file did.
type WriteResult struct {
	Outcome Outcome
	// Path is the file, relative to the project directory.
	Path string
	// Example is the file written beside Path, for WroteExample.
	Example string
	// ConfirmErr is why the user could not be asked, for WroteExample.
	ConfirmErr error
}

// agentFile is a hook config file for one agent.
type agentFile struct {
	// dir and name place the file, as dir/name under the project.
	dir, name string
	// example is the file written beside it when the user cannot be asked
	// about changing it.
	example string
	build   func([]config.Command) ([]byte, error)
	merge   func(existing, generated []byte) (*MergeResult, error)
}

var (
	claudeFile = agentFile{dir: ".claude", name: "settings.json", example: "settings.example.json", build: Build, merge: Merge}
	codexFile  = agentFile{dir: ".codex", name: "hooks.json", example: "hooks.example.json", build: BuildCodex, merge: MergeCodex}
)

// WriteClaude writes .claude/settings.json for the project, with hooks that
// run commands. When the file already exists, it merges chunk's hooks in and
// asks before applying the change; when the user cannot be asked, it writes
// settings.example.json instead.
func WriteClaude(dir string, commands []config.Command, confirm ConfirmFunc) (WriteResult, error) {
	return writeAgentFile(dir, claudeFile, commands, confirm)
}

// WriteCodex writes .codex/hooks.json for the project, the same way
// WriteClaude writes .claude/settings.json.
func WriteCodex(dir string, commands []config.Command, confirm ConfirmFunc) (WriteResult, error) {
	return writeAgentFile(dir, codexFile, commands, confirm)
}

// CodexWanted reports whether the project should have Codex hooks: Codex is
// installed on this machine, or the project already has a .codex directory.
// homeDir is the user's home directory, where an installed Codex keeps its
// settings.
func CodexWanted(dir, homeDir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ".codex")); err == nil {
		return true
	}
	if _, err := exec.LookPath("codex"); err == nil {
		return true
	}
	if homeDir == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(homeDir, ".codex"))
	return err == nil
}

func writeAgentFile(projectDir string, f agentFile, commands []config.Command, confirm ConfirmFunc) (WriteResult, error) {
	rel := f.dir + "/" + f.name
	res := WriteResult{Path: rel}

	generated, err := f.build(commands)
	if err != nil {
		return res, fmt.Errorf("build %s: %w", rel, err)
	}

	dir := filepath.Join(projectDir, f.dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return res, fmt.Errorf("create %s directory: %w", f.dir, err)
	}

	path := filepath.Join(dir, f.name)
	existing, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(path, withTrailingNewline(generated), 0o644); err != nil {
			return res, fmt.Errorf("write %s: %w", rel, err)
		}
		res.Outcome = Created
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("read %s: %w", rel, err)
	}

	merged, err := f.merge(existing, generated)
	if err != nil {
		return res, fmt.Errorf("merge %s: %w", rel, err)
	}
	if !merged.Changed {
		res.Outcome = UpToDate
		return res, nil
	}

	apply, err := confirm(rel, Diff(merged.Original, merged.Merged))
	if err != nil {
		res.Outcome = WroteExample
		res.Example = f.dir + "/" + f.example
		res.ConfirmErr = err
		if err := os.WriteFile(filepath.Join(dir, f.example), withTrailingNewline(generated), 0o644); err != nil {
			return res, fmt.Errorf("write %s: %w", res.Example, err)
		}
		return res, nil
	}
	if !apply {
		res.Outcome = Declined
		return res, nil
	}

	if err := os.WriteFile(path, withTrailingNewline(merged.Merged), 0o644); err != nil {
		return res, fmt.Errorf("write %s: %w", rel, err)
	}
	res.Outcome = Updated
	return res, nil
}

// withTrailingNewline returns a copy of data with a trailing newline appended.
// Uses a copy to avoid mutating the original slice's backing array.
func withTrailingNewline(data []byte) []byte {
	buf := make([]byte, len(data)+1)
	copy(buf, data)
	buf[len(data)] = '\n'
	return buf
}
