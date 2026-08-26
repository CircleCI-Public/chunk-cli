package validate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// defaultTestCommand is used by the Node.js case when no lock file narrows the package manager.
const defaultTestCommand = "npm test"

// Sources a detection can come from, for display in `chunk init`.
const (
	sourceLayout = "the repository layout"
	sourceClaude = "Claude"
)

// PackageManager holds the name and CI-safe install command for a detected package manager.
type PackageManager struct {
	Name           string
	InstallCommand string
}

// Detection is the outcome of validate-command detection: the commands, where
// they came from, and anything detection could not resolve. Provenance matters
// to the user — commands lifted from a CircleCI config can look nothing like
// the toolchain defaults, and a bare list gives no way to tell why.
type Detection struct {
	Commands []config.Command
	Source   string   // human-readable origin, empty when nothing was detected
	Notes    []string // what detection could not resolve
}

// DetectCommands returns the full set of validate commands for the repo with metadata.
//
// A checked-in CircleCI config is preferred over everything else: it names the
// checks that actually gate the branch, where root filenames only suggest a
// toolchain. Repos whose real build system is outranked by a stray manifest —
// a bazel monorepo containing a package.json, say — are misdetected otherwise.
//
// Failing that, known toolchains return richer commands without calling Claude.
// Claude is only used as a fallback for unknown toolchains, and only when a
// client is provided.
func DetectCommands(ctx context.Context, claude *anthropic.Client, workDir string) (Detection, error) {
	entries, _ := os.ReadDir(workDir)
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		files = append(files, e.Name())
	}

	has := make(map[string]bool, len(files))
	for _, f := range files {
		has[f] = true
	}

	ci := commandsFromCI(workDir)
	if len(ci.Commands) > 0 {
		// CI is authoritative for the gates it names, but formatting is an
		// autofix rather than a gate: a config usually checks formatting by
		// diffing the tree, never by naming a command that rewrites it. Taking
		// CI literally there would leave the user with no formatter at all, so
		// fill only the autofix roles from the toolchain default.
		ci.Commands = withAutofixDefaults(ci.Commands, commandsFromFilenames(workDir, has))
		return ci, nil
	}

	if cmds := commandsFromFilenames(workDir, has); len(cmds) > 0 {
		return Detection{Commands: cmds, Source: sourceLayout, Notes: fallbackNotes(ci)}, nil
	}

	// Unknown toolchain — ask Claude
	if claude == nil {
		return Detection{}, nil
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
		return Detection{}, fmt.Errorf("detect test command: %w", err)
	}

	result := strings.TrimSpace(resp)
	if result == "" {
		return Detection{}, nil
	}
	return Detection{
		Commands: []config.Command{{Name: "test", Run: result, Role: config.RoleGate}},
		Source:   sourceClaude,
		Notes:    fallbackNotes(ci),
	}, nil
}

// withAutofixDefaults adds the autofix commands from defaults that ci does not
// already provide, leaving every gate ci named untouched.
func withAutofixDefaults(ci, defaults []config.Command) []config.Command {
	for _, d := range defaults {
		if d.Role != config.RoleAutofix {
			continue
		}
		if slices.ContainsFunc(ci, func(c config.Command) bool { return c.Name == d.Name }) {
			continue
		}
		ci = append(ci, d)
	}
	return ci
}

// fallbackNotes explains why a CircleCI config that exists was not used, so a
// user who expected their CI gates is not left guessing. ci is the unusable
// result detection already produced; an empty Source means there was no config
// at all and nothing needs explaining.
func fallbackNotes(ci Detection) []string {
	if ci.Source == "" {
		return nil
	}
	return append(ci.Notes, fmt.Sprintf("no runnable checks were found in %s", ci.Source))
}

// commandsFromFilenames guesses commands from the repo's root filenames. It is
// the fallback for repos with no usable CircleCI config, and the source of
// autofix commands CI configs do not name.
func commandsFromFilenames(workDir string, has map[string]bool) []config.Command {
	isGo := has["go.mod"]

	switch {
	case has["Taskfile.yml"] || has["Taskfile.yaml"]:
		if isGo {
			return []config.Command{
				{Name: "test", Run: "task test", Role: config.RoleGate, Timeout: 300},
				{Name: "lint", Run: "task lint", Role: config.RoleGate, Timeout: 60},
				{Name: "format", Run: "task fmt", Role: config.RoleAutofix, Timeout: 30},
			}
		}
		return []config.Command{
			{Name: "test", Run: "task test", Role: config.RoleGate, Timeout: 300},
		}

	case has["Makefile"]:
		if isGo {
			return []config.Command{
				{Name: "test", Run: "make test", Role: config.RoleGate, Timeout: 300},
				{Name: "lint", Run: "make lint", Role: config.RoleGate, Timeout: 60},
			}
		}
		return []config.Command{
			{Name: "test", Run: "make test", Role: config.RoleGate, Timeout: 300},
		}

	case isGo:
		return []config.Command{
			{Name: "test", Run: "go test ./...", Role: config.RoleGate, Timeout: 300},
			{Name: "lint", Run: "golangci-lint run ./...", Role: config.RoleGate, Timeout: 60},
			{Name: "format", Run: "gofmt -w .", Role: config.RoleAutofix, Timeout: 30},
		}

	case has["Cargo.toml"]:
		return []config.Command{
			{Name: "test", Run: "cargo test", Role: config.RoleGate, Timeout: 300},
		}

	case has["pyproject.toml"], has["requirements.txt"], has["setup.py"], has["Pipfile"]:
		return []config.Command{
			{Name: "test", Run: "pytest", Role: config.RoleGate, Timeout: 300},
		}

	case has["Gemfile"]:
		// Assumes Rake-based test task (Rails default). RSpec/Minitest-only stacks may need manual adjustment.
		return []config.Command{
			{Name: "test", Run: "bundle exec rake test", Role: config.RoleGate, Timeout: 300},
		}

	case has["pom.xml"]:
		return []config.Command{
			{Name: "test", Run: "mvn test", Role: config.RoleGate, Timeout: 300},
		}

	case has["build.gradle"], has["build.gradle.kts"]:
		gradleCmd := "gradle test"
		if has["gradlew"] {
			gradleCmd = "./gradlew test"
		}
		return []config.Command{
			{Name: "test", Run: gradleCmd, Role: config.RoleGate, Timeout: 300},
		}

	case has["package.json"]:
		pm := DetectPackageManager(workDir)
		testCmd := defaultTestCommand
		if pm != nil {
			testCmd = pm.Name + " test"
		}
		return []config.Command{
			{Name: "test", Run: testCmd, Role: config.RoleGate, Timeout: 300},
		}
	}

	// Monorepo with no root package.json but a detectable package manager in subdirs.
	if pm := DetectPackageManager(workDir); pm != nil {
		return []config.Command{
			{Name: "test", Run: pm.Name + " test", Role: config.RoleGate, Timeout: 300},
		}
	}

	return nil
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
