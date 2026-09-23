package gitutil

import (
	"path/filepath"
	"strings"
)

// gitOptsWithValue are git's global options that take their value as the next
// word, so the word after them is not the subcommand.
var gitOptsWithValue = map[string]bool{
	"-C":             true,
	"-c":             true,
	"--git-dir":      true,
	"--work-tree":    true,
	"--namespace":    true,
	"--config-env":   true,
	"--super-prefix": true,
}

// envOptsWithValue are env(1) options that consume the next word as their
// value, so that word is not the command being run.
var envOptsWithValue = map[string]bool{
	"-u":             true,
	"--unset":        true,
	"-C":             true,
	"--chdir":        true,
	"-S":             true,
	"--split-string": true,
}

// IsCommitCommand reports whether a shell command line runs git commit in any
// of its parts: "git commit", "cd x && git commit", "git -C dir commit",
// "FOO=1 git commit".
//
// It is a word-level scan, not a shell parser. It leans towards yes, because
// the caller is a commit gate: a false positive costs one extra validation run,
// while a false negative lets a commit through unchecked.
func IsCommitCommand(line string) bool {
	for _, part := range splitShellCommands(line) {
		if runsGitCommit(strings.Fields(part)) {
			return true
		}
	}
	return false
}

// splitShellCommands splits a command line on the operators that separate one
// command from the next.
func splitShellCommands(line string) []string {
	return strings.FieldsFunc(line, func(r rune) bool {
		switch r {
		case '&', '|', ';', '\n', '(', ')', '{', '}':
			return true
		}
		return false
	})
}

// runsGitCommit reports whether words, one simple command, invoke git commit.
func runsGitCommit(words []string) bool {
	// Leading VAR=value assignments and an env wrapper (with its own flags)
	// do not change which program runs.
	seenEnv := false
	for len(words) > 0 {
		w := words[0]
		if w == "env" {
			seenEnv = true
			words = words[1:]
			continue
		}
		if isAssignment(w) {
			words = words[1:]
			continue
		}
		if seenEnv && strings.HasPrefix(w, "-") {
			words = words[1:]
			if envOptsWithValue[w] && len(words) > 0 {
				words = words[1:]
			}
			continue
		}
		break
	}
	if len(words) == 0 || filepath.Base(words[0]) != "git" {
		return false
	}
	words = words[1:]
	for len(words) > 0 && strings.HasPrefix(words[0], "-") {
		if gitOptsWithValue[words[0]] {
			words = words[1:]
		}
		if len(words) > 0 {
			words = words[1:]
		}
	}
	return len(words) > 0 && words[0] == "commit"
}

func isAssignment(word string) bool {
	name, _, ok := strings.Cut(word, "=")
	return ok && name != "" && !strings.HasPrefix(name, "-")
}
