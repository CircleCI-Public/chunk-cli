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

// defaultTestCommand is used by the Node.js case when no lock file narrows the package manager.
const defaultTestCommand = "npm test"

// PackageManager holds the name and CI-safe install command for a detected package manager.
type PackageManager struct {
	Name           string
	InstallCommand string
}

// DetectedCommands holds the results of auto-detection, split by command type.
type DetectedCommands struct {
	Fix      []config.FixCommand
	Validate []config.ValidateCommand
}

// DetectCommands returns the detected fix and validate commands for the repo.
// For known toolchains it returns commands without calling Claude. Claude is
// only used as a fallback for unknown toolchains, and only when a client is provided.
func DetectCommands(ctx context.Context, claude *anthropic.Client, workDir string) (*DetectedCommands, error) {
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
			return &DetectedCommands{
				Fix: []config.FixCommand{
					{Name: "format", Run: "task fmt", Timeout: 30},
				},
				Validate: []config.ValidateCommand{
					{Name: "test", Run: "task test", Timeout: 300},
					{Name: "lint", Run: "task lint", Timeout: 60},
				},
			}, nil
		}
		return &DetectedCommands{
			Validate: []config.ValidateCommand{
				{Name: "test", Run: "task test", Timeout: 300},
			},
		}, nil

	case has["Makefile"]:
		if isGo {
			return &DetectedCommands{
				Validate: []config.ValidateCommand{
					{Name: "test", Run: "make test", Timeout: 300},
					{Name: "lint", Run: "make lint", Timeout: 60},
				},
			}, nil
		}
		return &DetectedCommands{
			Validate: []config.ValidateCommand{
				{Name: "test", Run: "make test", Timeout: 300},
			},
		}, nil

	case isGo:
		return &DetectedCommands{
			Fix: []config.FixCommand{
				{Name: "format", Run: "gofmt -w .", Timeout: 30},
			},
			Validate: []config.ValidateCommand{
				{Name: "test", Run: "go test ./...", Timeout: 300},
				{Name: "lint", Run: "golangci-lint run ./...", Timeout: 60},
			},
		}, nil

	case has["Cargo.toml"]:
		return &DetectedCommands{
			Validate: []config.ValidateCommand{
				{Name: "test", Run: "cargo test", Timeout: 300},
			},
		}, nil

	case has["pyproject.toml"], has["requirements.txt"], has["setup.py"], has["Pipfile"]:
		return &DetectedCommands{
			Validate: []config.ValidateCommand{
				{Name: "test", Run: "pytest", Timeout: 300},
			},
		}, nil

	case has["Gemfile"]:
		return &DetectedCommands{
			Validate: []config.ValidateCommand{
				{Name: "test", Run: "bundle exec rake test", Timeout: 300},
			},
		}, nil

	case has["pom.xml"]:
		return &DetectedCommands{
			Validate: []config.ValidateCommand{
				{Name: "test", Run: "mvn test", Timeout: 300},
			},
		}, nil

	case has["build.gradle"], has["build.gradle.kts"]:
		gradleCmd := "gradle test"
		if has["gradlew"] {
			gradleCmd = "./gradlew test"
		}
		return &DetectedCommands{
			Validate: []config.ValidateCommand{
				{Name: "test", Run: gradleCmd, Timeout: 300},
			},
		}, nil

	case has["package.json"]:
		pm := DetectPackageManager(workDir)
		testCmd := defaultTestCommand
		if pm != nil {
			testCmd = pm.Name + " test"
		}
		return &DetectedCommands{
			Validate: []config.ValidateCommand{
				{Name: "test", Run: testCmd, Timeout: 300},
			},
		}, nil
	}

	// Monorepo with no root package.json but a detectable package manager in subdirs.
	if pm := DetectPackageManager(workDir); pm != nil {
		return &DetectedCommands{
			Validate: []config.ValidateCommand{
				{Name: "test", Run: pm.Name + " test", Timeout: 300},
			},
		}, nil
	}

	// Unknown toolchain — ask Claude
	if claude == nil {
		return nil, nil
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
		return nil, nil
	}
	return &DetectedCommands{
		Validate: []config.ValidateCommand{{Name: "test", Run: result}},
	}, nil
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
