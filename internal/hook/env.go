package hook

import (
	"os"
	"path/filepath"
	"strings"
)

// IsEnabled checks whether a specific command is enabled.
// Resolution: CHUNK_HOOK_ENABLE_{NAME} > CHUNK_HOOK_ENABLE > false.
func IsEnabled(name string) bool {
	perCmd := os.Getenv("CHUNK_HOOK_ENABLE_" + strings.ToUpper(name))
	if perCmd != "" {
		return isTruthy(perCmd)
	}
	global := os.Getenv("CHUNK_HOOK_ENABLE")
	return isTruthy(global)
}

func isTruthy(val string) bool {
	switch strings.ToLower(val) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// ResolveProject resolves the --project flag to an absolute path.
func ResolveProject(flagValue string) string {
	if flagValue == "" {
		dir := os.Getenv("CLAUDE_PROJECT_DIR")
		if dir != "" {
			return dir
		}
		dir, _ = os.Getwd()
		return dir
	}

	if filepath.IsAbs(flagValue) {
		return flagValue
	}

	root := os.Getenv("CHUNK_HOOK_PROJECT_ROOT")
	if root != "" {
		return filepath.Join(root, flagValue)
	}

	cwd, _ := os.Getwd()
	return filepath.Join(cwd, flagValue)
}

// SentinelsDir returns the sentinel directory from the environment.
func SentinelsDir() string {
	return os.Getenv("CHUNK_HOOK_SENTINELS_DIR")
}
