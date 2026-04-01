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

// defaultTestCommand is used when no toolchain is detected and Claude is unavailable.
const defaultTestCommand = "npm test"

// PackageManager holds the name and CI-safe install command for a detected package manager.
type PackageManager struct {
	Name           string
	InstallCommand string
}

// DetectCommands returns the full set of validate commands for the repo with metadata.
// For known toolchains it returns richer commands without calling Claude. Claude is
// only used as a fallback for unknown toolchains, and only when a client is provided.
func DetectCommands(ctx context.Context, claude *anthropic.Client, workDir string) ([]config.Command, error) {
	entries, _ := os.ReadDir(workDir)
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		files = append(files, e.Name())
	}

	has := make(map[string]bool, len(files))
	for _, f := range files {
		has[f] = true
	}

	isGo := has["go.mod"]

	switch {
	case has["Taskfile.yml"] || has["Taskfile.yaml"]:
		if isGo {
			return []config.Command{
				{Name: "test", Run: "task test", Role: config.RoleGate, FileExt: ".go", Timeout: 300, Limit: 3},
				{Name: "test-changed", Run: "task test -- {{CHANGED_PACKAGES}}", Role: config.RolePrecheck, FileExt: ".go", Timeout: 300, Limit: 3},
				{Name: "lint", Run: "task lint", Role: config.RoleGate, FileExt: ".go", Timeout: 60, Limit: 3},
				{Name: "format", Run: "task fmt", Role: config.RoleAutofix, Always: true, Timeout: 30, Limit: 3},
			}, nil
		}
		return []config.Command{
			{Name: "test", Run: "task test", Role: config.RoleGate, Timeout: 300, Limit: 3},
		}, nil

	case has["Makefile"]:
		if isGo {
			return []config.Command{
				{Name: "test", Run: "make test", Role: config.RoleGate, FileExt: ".go", Timeout: 300, Limit: 3},
				{Name: "lint", Run: "make lint", Role: config.RoleGate, FileExt: ".go", Timeout: 60, Limit: 3},
			}, nil
		}
		return []config.Command{
			{Name: "test", Run: "make test", Role: config.RoleGate, Timeout: 300, Limit: 3},
		}, nil

	case isGo:
		return []config.Command{
			{Name: "test", Run: "go test ./...", Role: config.RoleGate, FileExt: ".go", Timeout: 300, Limit: 3},
			{Name: "lint", Run: "golangci-lint run ./...", Role: config.RoleGate, FileExt: ".go", Timeout: 60, Limit: 3},
			{Name: "format", Run: "gofmt -w .", Role: config.RoleAutofix, Always: true, Timeout: 30, Limit: 3},
		}, nil

	case has["Cargo.toml"]:
		return []config.Command{
			{Name: "test", Run: "cargo test", Role: config.RoleGate, Timeout: 300, Limit: 3},
		}, nil

	case has["pyproject.toml"]:
		return []config.Command{
			{Name: "test", Run: "pytest", Role: config.RoleGate, Timeout: 300, Limit: 3},
		}, nil

	case has["package.json"]:
		pm := DetectPackageManager(workDir)
		testCmd := defaultTestCommand
		if pm != nil {
			testCmd = pm.Name + " test"
		}
		return []config.Command{
			{Name: "test", Run: testCmd, Role: config.RoleGate, Timeout: 300, Limit: 3},
		}, nil
	}

	// Monorepo with no root package.json but a detectable package manager in subdirs.
	if pm := DetectPackageManager(workDir); pm != nil {
		return []config.Command{
			{Name: "test", Run: pm.Name + " test", Role: config.RoleGate, Timeout: 300, Limit: 3},
		}, nil
	}

	// Unknown toolchain — ask Claude
	if claude == nil {
		return []config.Command{{Name: "test", Run: defaultTestCommand, Role: config.RoleGate}}, nil
	}

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
		pmHint, gatherRepoContext(workDir, files),
	)

	resp, err := claude.Ask(ctx, config.ValidationModel, 64, prompt,
		"Respond with ONLY a shell command string. No explanation, no reasoning, no markdown, no preamble. Output the command and nothing else.")
	if err != nil {
		return nil, fmt.Errorf("detect test command: %w", err)
	}

	result := strings.TrimSpace(resp)
	if result == "" {
		result = defaultTestCommand
	}
	return []config.Command{{Name: "test", Run: result, Role: config.RoleGate}}, nil
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

	// Check root first, then one level deep for monorepos.
	searchDirs := []string{workDir}
	if entries, err := os.ReadDir(workDir); err == nil {
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				searchDirs = append(searchDirs, filepath.Join(workDir, e.Name()))
			}
		}
	}

	for _, lf := range lockfiles {
		for _, dir := range searchDirs {
			if _, err := os.Stat(filepath.Join(dir, lf.file)); err == nil {
				pm := lf.pm
				return &pm
			}
		}
	}
	return nil
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
		".chunk/config.json",
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
