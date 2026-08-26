package validate

import (
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/ciconfig"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// Roles a CI step can be classified into, in the order they are emitted.
const (
	roleNone    = ""
	roleInstall = "install"
	roleFormat  = "format"
	roleLint    = "lint"
	roleTest    = "test"
)

// skipMarkers mark a step that runs in CI but has no business in a developer's
// inner loop: deploys, uploads, and machine provisioning. Matched case-folded
// against the command.
var skipMarkers = []string{
	"deploy", "publish", "release", "notify", "slack", "upload",
	"codecov", "coveralls", "sonar", "artifact",
	"docker push", "docker build", "docker login",
	"terraform", "kubectl", "helm ", "aws ", "gcloud ", "gsutil",
	"apt-get", "apt ", "brew ", "curl ", "wget ",
	"git push", "git tag", "goreleaser", "npm publish",
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
		// "apt-get install" and friends are removed by skipMarkers first.
		" install", "npm ci", "uv sync", "go mod download",
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
	roleInstall: {config.CmdInstall, "", 0},
	roleTest:    {roleTest, config.RoleGate, 300},
	roleLint:    {roleLint, config.RoleGate, 60},
	roleFormat:  {roleFormat, config.RoleAutofix, 30},
}

// emitOrder is the order commands are written to .chunk/config.json.
var emitOrder = []string{roleInstall, roleTest, roleLint, roleFormat}

// commandsFromCI derives validate commands from the repo's CircleCI config,
// which names the checks that actually gate the default branch. It returns nil
// when there is no config, when the config is dynamic, or when nothing in it
// classifies — in every case the caller falls back to filename detection.
//
// At most one command is kept per role: the first match wins, so a job's
// primary test step beats a later variant like an acceptance-only run.
func commandsFromCI(workDir string) []config.Command {
	res, err := ciconfig.Extract(workDir)
	if err != nil || !res.Usable() {
		return nil
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
			Run:     c.Command,
			Role:    spec.role,
			Timeout: spec.timeout,
		}
	}

	if len(picked) == 0 {
		return nil
	}

	out := make([]config.Command, 0, len(picked))
	for _, role := range emitOrder {
		if cmd, ok := picked[role]; ok {
			out = append(out, cmd)
		}
	}
	return out
}

// classify maps a CI step to a validate role, or roleNone if it is not
// something a developer should run before committing.
func classify(c ciconfig.Candidate) string {
	// A multi-line script is a CI procedure, not a gate a developer runs.
	if strings.Contains(strings.TrimSpace(c.Command), "\n") {
		return roleNone
	}
	// A command scoped to a subdirectory needs context this package does not
	// carry into .chunk/config.json, so it cannot be written verbatim.
	if c.WorkingDir != "" {
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

	if role := matchRole(cmd); role != roleNone {
		return role
	}
	// Fall back to the step's own label: "Run tests" around an opaque script.
	return matchRole(strings.ToLower(c.Step))
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
