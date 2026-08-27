package validate

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/ciconfig"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// Roles a CI step can be classified into, in the order they are emitted.
const (
	roleNone    = ""
	roleInstall = "install"
	// roleFormat is a formatter that rewrites the tree, so it becomes an
	// autofix. roleFormatCheck is the same tool asked only to report, which is
	// a gate: it cannot fix anything, and it must not occupy the autofix slot
	// the toolchain default fills.
	roleFormat      = "format"
	roleFormatCheck = "format-check"
	roleLint        = "lint"
	roleTest        = "test"
)

// skipMarkers mark a step that runs in CI but has no business in a developer's
// inner loop: deploys, uploads, and reporting. Matched case-folded as a
// substring, because the work is named rather than invoked by a fixed tool —
// `make deploy`, `npm run upload-coverage`.
var skipMarkers = []string{
	"deploy", "publish", "notify", "slack", "upload",
	"codecov", "coveralls", "sonar", "artifact",
	"docker push", "docker build", "docker login",
	"git push", "git tag", "npm publish",
	// "release" alone would swallow `cargo test --release` and every other
	// build-profile flag, so match only commands that are a release.
	"make release", "task release", "npm run release", "./release",
	"release.sh",
}

// skipTools provision the machine rather than check the code. Unlike
// skipMarkers these are matched against the leading word of each command in
// the step, not as substrings: `aws` appearing anywhere would drop
// `pytest tests/aws_client`, and dropping a real gate is the failure this
// package exists to prevent.
var skipTools = []string{
	"apt", "apt-get", "aws", "brew", "curl", "gcloud", "goreleaser",
	"gsutil", "helm", "kubectl", "sudo", "terraform", "wget",
}

// commandSeparators split a shell one-liner into the commands it runs, so the
// leading word of each can be checked against skipTools.
var commandSeparators = regexp.MustCompile(`&&|\|\||[;|]`)

// safeWorkingDir matches a relative path that can be written into a `cd`
// prefix as-is: no shell metacharacters, no absolute or home-relative root.
var safeWorkingDir = regexp.MustCompile(`^[A-Za-z0-9._][A-Za-z0-9._/-]*$`)

// toolInstallMarkers install a binary into the CI image rather than the
// project's dependencies, so they must not be taken for the install step.
var toolInstallMarkers = []string{
	"cargo install", "go install", "npm install -g", "npm i -g",
	"yarn global add", "pipx install",
}

// ciOnlyMarkers mark a step that cannot run outside a CircleCI container.
var ciOnlyMarkers = []string{
	"$bash_env", "${bash_env}", "$circle_", "${circle_", "~/project",
	"/home/circleci", "$circleci",
}

// roleMarkers classify a step by substring, tried in order. Install is first
// so `yarn install` is not read as a test, and format precedes lint so a
// formatter is not mistaken for a checker.
var roleMarkers = []struct {
	role    string
	markers []string
}{
	{roleInstall, []string{
		// Covers npm/yarn/pnpm/bun/bundle/pip/poetry install in one marker.
		// "apt-get install" and friends are removed by skipTools first.
		" install", "npm ci", "uv sync", "go mod download", "cargo fetch",
		"mod-download", "mod_download", "install-deps", "install_deps",
	}},
	{roleFormat, []string{
		"gofmt", "goimports", "prettier", "ruff format", "rustfmt",
		"cargo fmt", ":fmt", " fmt", roleFormat,
	}},
	{roleLint, []string{
		roleLint, "vet", "eslint", "flake8", "ruff check", "shellcheck",
		"mypy", "typecheck", "type-check", "tsc ", "clippy", "rubocop",
		"checkstyle", "spotless",
	}},
	{roleTest, []string{
		roleTest, "pytest", "rspec", "jest", "vitest", "mocha", "phpunit",
		"minitest", "junit",
	}},
}

// roleSpec is how a classified role becomes a config.Command.
var roleSpec = map[string]struct {
	name    string
	role    string
	timeout int
}{
	roleInstall:     {config.CmdInstall, "", 0},
	roleTest:        {roleTest, config.RoleGate, 300},
	roleLint:        {roleLint, config.RoleGate, 60},
	roleFormatCheck: {roleFormatCheck, config.RoleGate, 30},
	roleFormat:      {roleFormat, config.RoleAutofix, 30},
}

// emitOrder is the order commands are written to .chunk/config.json.
var emitOrder = []string{roleInstall, roleTest, roleLint, roleFormatCheck, roleFormat}

// commandsFromCI derives validate commands from the repo's CircleCI config,
// which names the checks that actually gate the default branch. Commands are
// empty when there is no config, when the config cannot be parsed, when it is
// dynamic, or when nothing in it classifies — in every case the caller falls
// back to filename detection, and Notes says why so `chunk init` can tell the
// user.
//
// At most one command is kept per role: the first match wins, so a job's
// primary test step beats a later variant like an acceptance-only run.
func commandsFromCI(workDir string) Detection {
	res, err := ciconfig.Extract(workDir)
	if err != nil {
		// A config that exists but cannot be read is the one failure worth
		// reporting: the user expects their CI gates, and falling back silently
		// looks like the config was read and found to hold nothing. A repo with
		// no config at all has nothing to explain.
		var cfgErr *ciconfig.ConfigError
		if errors.As(err, &cfgErr) {
			return Detection{
				Source: configSource(workDir, cfgErr.Path),
				Notes:  []string{fmt.Sprintf("the config could not be read: %v", cfgErr.Err)},
			}
		}
		return Detection{}
	}
	if res == nil {
		return Detection{}
	}

	det := Detection{Source: configSource(workDir, res.Path), Notes: ciNotes(res)}
	if res.Dynamic {
		return det
	}

	picked := make(map[string]config.Command, len(emitOrder))
	for _, c := range res.Candidates {
		role := classify(c)
		if role == roleNone {
			continue
		}
		if _, taken := picked[role]; taken {
			continue
		}
		spec := roleSpec[role]
		picked[role] = config.Command{
			Name:    spec.name,
			Run:     candidateRun(c),
			Role:    spec.role,
			Timeout: spec.timeout,
		}
	}

	for _, role := range emitOrder {
		if cmd, ok := picked[role]; ok {
			det.Commands = append(det.Commands, cmd)
		}
	}
	return det
}

// configSource renders the config path relative to the repo for display.
func configSource(workDir, path string) string {
	if rel, err := filepath.Rel(workDir, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return path
}

// ciNotes describes what the extractor could not resolve, so a surprising set
// of commands can be explained rather than silently trusted.
func ciNotes(res *ciconfig.Result) []string {
	var notes []string
	if res.Dynamic {
		notes = append(notes, "the config generates its real config at run time, so its checks are not visible")
	}
	if len(res.SkippedOrbs) > 0 {
		notes = append(notes, fmt.Sprintf("steps from orbs were not read: %s", strings.Join(res.SkippedOrbs, ", ")))
	}
	if res.Unresolved > 0 {
		notes = append(notes, fmt.Sprintf("%d step(s) referenced values that could not be resolved", res.Unresolved))
	}
	if res.Truncated > 0 {
		notes = append(notes, fmt.Sprintf("%d step(s) past the scan limit were not read", res.Truncated))
	}
	return notes
}

// candidateRun renders a candidate as something a developer can run from the
// repo root. A step-level working_directory narrows where the command runs, so
// it is preserved as a `cd` prefix rather than discarded: in a monorepo the
// subdirectory checks are the real gates.
func candidateRun(c ciconfig.Candidate) string {
	if c.WorkingDir == "" {
		return c.Command
	}
	// && binds more loosely than the operators inside the command, so a bare
	// prefix changes what the command means: `cd web && yarn lint || true`
	// parses as `(cd web && yarn lint) || true`, which turns a failed cd into a
	// pass, and `cd web && a; b` runs b from the repo root either way. A
	// subshell keeps the command's own precedence intact.
	if needsGrouping.MatchString(c.Command) {
		return "cd " + c.WorkingDir + " && ( " + c.Command + " )"
	}
	return "cd " + c.WorkingDir + " && " + c.Command
}

// needsGrouping matches a command carrying shell control operators — ; & && | ||
// — whose precedence a `cd &&` prefix would otherwise alter.
var needsGrouping = regexp.MustCompile(`[;&|]`)

// classify maps a CI step to a validate role, or roleNone if it is not
// something a developer should run before committing.
func classify(c ciconfig.Candidate) string {
	// A multi-line script is a CI procedure, not a gate a developer runs.
	if strings.Contains(strings.TrimSpace(c.Command), "\n") {
		return roleNone
	}
	// A step-level working_directory narrows where the command runs, and is
	// reproduced as a `cd` prefix — but only when the path is somewhere a
	// developer's checkout has too. A job-level working_directory is only where
	// checkout puts the repo, and does not narrow anything.
	if c.WorkingDir != "" && !usableWorkingDir(c.WorkingDir) {
		return roleNone
	}

	cmd := strings.ToLower(c.Command)
	for _, m := range ciOnlyMarkers {
		if strings.Contains(cmd, m) {
			return roleNone
		}
	}
	for _, m := range skipMarkers {
		if strings.Contains(cmd, m) {
			return roleNone
		}
	}
	for _, m := range toolInstallMarkers {
		if strings.Contains(cmd, m) {
			return roleNone
		}
	}
	if slices.ContainsFunc(leadingWords(cmd), func(w string) bool {
		return slices.Contains(skipTools, w)
	}) {
		return roleNone
	}

	if role := matchRole(cmd); role != roleNone {
		return refineFormat(role, cmd)
	}
	// Fall back to the step's own label: "Run tests" around an opaque script.
	label := strings.ToLower(c.Step)
	// The label is what classified the step, so it is also the only place the
	// check-vs-rewrite distinction can be read from.
	return refineFormat(matchRole(label), cmd+" "+label)
}

// formatCheckFlags mark a formatter told to report rather than rewrite:
// `prettier --check .`, `cargo fmt --check`, `gofmt -l .`, `ruff format --diff`.
// Short flags are matched as whole words so -l does not fire on -ldflags.
var formatCheckFlags = regexp.MustCompile(`(^|\s)(-l|-d|--check|--diff|--dry-run|--list-different)(\s|=|$)`)

// formatCheckNames catch a check-only formatter behind a task or script name,
// where there is no flag to read: `task ci:fmt-check`, `npm run format:check`.
var formatCheckNames = regexp.MustCompile(`check[_:.-]?(fmt|format)|(fmt|format)[_:.-]?check`)

// refineFormat splits the format role by whether the command rewrites the tree
// or only reports on it. Only a rewriting formatter can serve as an autofix; a
// check-only one is a gate, and classifying it as format would both give the
// user an autofix that cannot fix and suppress the toolchain default that can.
func refineFormat(role, text string) string {
	if role != roleFormat {
		return role
	}
	if formatCheckFlags.MatchString(text) || formatCheckNames.MatchString(text) {
		return roleFormatCheck
	}
	return role
}

// usableWorkingDir reports whether a step's working_directory names a path
// inside the repo that can be written into a `cd` prefix unquoted. An absolute
// path, a home-relative one, a path escaping the repo, or anything carrying
// shell syntax is rejected — a wrong directory would fail every run.
func usableWorkingDir(dir string) bool {
	if !safeWorkingDir.MatchString(dir) {
		return false
	}
	return !slices.Contains(strings.Split(dir, "/"), "..")
}

// leadingWords returns the first word of each command in a shell one-liner, so
// a tool can be recognized where it is invoked rather than wherever its name
// happens to appear.
func leadingWords(cmd string) []string {
	var out []string
	for _, part := range commandSeparators.Split(cmd, -1) {
		if fields := strings.Fields(part); len(fields) > 0 {
			out = append(out, fields[0])
		}
	}
	return out
}

func matchRole(s string) string {
	if s == "" {
		return roleNone
	}
	for _, rm := range roleMarkers {
		for _, m := range rm.markers {
			if strings.Contains(s, m) {
				return rm.role
			}
		}
	}
	return roleNone
}
