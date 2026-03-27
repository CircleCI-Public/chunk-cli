package validate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// PackageManager holds the name and CI-safe install command for a detected package manager.
type PackageManager struct {
	Name           string
	InstallCommand string
}

// DetectTestCommand returns the test command for the repo, using Claude if needed.
func DetectTestCommand(ctx context.Context, claude *anthropic.Client, workDir string) (string, error) {
	entries, _ := os.ReadDir(workDir)
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		files = append(files, e.Name())
	}

	if cmd := detectTestCommandFromFiles(files); cmd != "" {
		return cmd, nil
	}

	repoCtx := gatherRepoContext(workDir, files)
	pm := DetectPackageManager(workDir)

	var pmHint string
	if pm != nil {
		pmHint = fmt.Sprintf("Detected package manager: %s. Use %s to run tests (e.g. `%s test`).\n\n", pm.Name, pm.Name, pm.Name)
	}

	prompt := fmt.Sprintf(
		"You are analyzing a software repository to determine how tests are run.\n\n"+
			"%s%s\n\n"+
			"Based on the above, output ONLY the shell command used to run the test suite — "+
			"nothing else. No explanation, no markdown. Just the command string.",
		pmHint, repoCtx,
	)

	resp, err := claude.Ask(ctx, config.ValidationModel, 64, prompt)
	if err != nil {
		return "", fmt.Errorf("detect test command: %w", err)
	}

	result := strings.TrimSpace(resp)
	if result == "" {
		return "npm test", nil
	}
	return result, nil
}

// DetectPackageManager returns the detected package manager and its CI-safe install command, or nil.
func DetectPackageManager(workDir string) *PackageManager {
	lockfiles := []struct {
		file string
		pm   PackageManager
	}{
		{"pnpm-lock.yaml", PackageManager{"pnpm", "pnpm install --frozen-lockfile"}},
		{"yarn.lock", PackageManager{"yarn", "yarn install --frozen-lockfile"}},
		{"bun.lock", PackageManager{"bun", "bun install --frozen-lockfile"}},
		{"bun.lockb", PackageManager{"bun", "bun install --frozen-lockfile"}},
		{"package-lock.json", PackageManager{"npm", "npm ci"}},
	}

	for _, lf := range lockfiles {
		if _, err := os.Stat(filepath.Join(workDir, lf.file)); err == nil {
			return &lf.pm
		}
	}
	return nil
}

func detectTestCommandFromFiles(files []string) string {
	has := make(map[string]bool, len(files))
	for _, f := range files {
		has[f] = true
	}

	switch {
	case has["Taskfile.yml"] || has["Taskfile.yaml"]:
		return "task test"
	case has["Makefile"]:
		return "make test"
	case has["go.mod"]:
		return "go test ./..."
	case has["Cargo.toml"]:
		return "cargo test"
	case has["pyproject.toml"]:
		return "pytest"
	case has["package.json"]:
		return "npm test"
	default:
		return ""
	}
}

func gatherRepoContext(workDir string, rootFiles []string) string {
	var parts []string
	parts = append(parts, "Root files:\n"+strings.Join(rootFiles, "\n"))

	candidates := []string{
		"package.json",
		"Makefile",
		"go.mod",
		"pom.xml",
		"build.gradle",
		"build.gradle.kts",
		"pyproject.toml",
		"setup.py",
		"pytest.ini",
		"Cargo.toml",
		"Taskfile.yml",
		"Taskfile.yaml",
		".chunk/hook/config.yml",
		".npmrc",
		".yarnrc",
		".yarnrc.yml",
		"requirements.txt",
		"requirements-dev.txt",
		"requirements-test.txt",
		"Pipfile",
		"Gemfile",
		"go.sum",
		"project.clj",
		"deps.edn",
	}

	const maxBytes = 4000
	for _, rel := range candidates {
		full := filepath.Join(workDir, rel)
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		content := string(data)
		if len(content) > maxBytes {
			content = content[:maxBytes]
		}
		parts = append(parts, fmt.Sprintf("\n--- %s ---\n%s", rel, content))
	}

	return strings.Join(parts, "\n")
}
