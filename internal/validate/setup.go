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
		return withDefaults(ci, commandsFromFilenames(workDir, has)), nil
	}

	if cmds := commandsFromFilenames(workDir, has); len(cmds) > 0 {
		return Detection{Commands: cmds, Source: sourceLayout, Notes: fallbackNotes(ci)}, nil
	}

	// Unknown toolchain — ask Claude. With no client there is nothing left to
	// try, but an unusable CircleCI config still needs explaining: this is the
	// case where the user ends up with no commands at all.
	if claude == nil {
		return Detection{Notes: fallbackNotes(ci)}, nil
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
		return Detection{Notes: fallbackNotes(ci)}, nil
	}
	return Detection{
		Commands: []config.Command{{Name: "test", Run: result, Role: config.RoleGate}},
		Source:   sourceClaude,
		Notes:    fallbackNotes(ci),
	}, nil
}

// withDefaults fills the roles a CircleCI config left unnamed from the
// toolchain defaults, leaving every command CI did name untouched.
//
// CI is authoritative for the gates it names, with two exceptions. Formatting
// is an autofix rather than a gate: a config usually checks formatting by
// diffing the tree, never by naming a command that rewrites it, so taking CI
// literally there would leave the user with no formatter at all. A check-only
// formatter from CI is emitted as "format-check", so it does not match the
// default's "format" name and the real fixer is still added alongside it.
//
// The test gate is filled too, because losing it is not deference to CI — it is
// a config that validates nothing. A suite can reach CI through a multi-line
// script, an orb-provided job, or a step whose wording trips a skip marker, and
// none of those classify, so a config naming only lint and format would
// otherwise be written out test-less. Gates other than test stay unnamed when
// CI does not name them: a lint gate the repo never runs is a tool that may not
// even be installed, and it would fail every validate run.
func withDefaults(ci Detection, defaults []config.Command) Detection {
	names := func(cmds []config.Command, name string) bool {
		return slices.ContainsFunc(cmds, func(c config.Command) bool { return c.Name == name })
	}
	for _, d := range defaults {
		if d.Role != config.RoleAutofix && d.Name != roleTest {
			continue
		}
		if names(ci.Commands, d.Name) {
			continue
		}
		if d.Name == roleTest {
			ci.Notes = append(ci.Notes, fmt.Sprintf(
				"no test command was found in %s, so `%s` comes from the repository layout", ci.Source, d.Run))
		}
		ci.Commands = append(ci.Commands, d)
	}
	// Neither source named a test. Nothing can be filled in, but the user is
	// about to get a config that gates on lint alone, and should hear it here
	// rather than discover it the first time validate passes on a broken tree.
	if !names(ci.Commands, roleTest) {
		ci.Notes = append(ci.Notes, fmt.Sprintf(
			"no test command was found in %s, and the repository layout does not suggest one", ci.Source))
	}
	sortByEmitOrder(ci.Commands)
	return ci
}

// sortByEmitOrder puts a backfilled command where CI's own would have gone:
// install before the gates it installs for, and the formatter last.
func sortByEmitOrder(cmds []config.Command) {
	index := func(name string) int {
		for i, role := range emitOrder {
			if roleSpec[role].name == name {
				return i
			}
		}
		return len(emitOrder)
	}
	slices.SortStableFunc(cmds, func(a, b config.Command) int {
		return index(a.Name) - index(b.Name)
	})
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
