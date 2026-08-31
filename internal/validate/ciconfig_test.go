package validate

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/ciconfig"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// writeCI writes a CircleCI config into dir.
func writeCI(t *testing.T, dir, body string) {
	t.Helper()
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".circleci"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".circleci", "config.yml"), []byte(body), 0o644))
}

// runs flattens commands to "name=run" pairs for terse asserts.
func runs(cmds []config.Command) []string {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, c.Name+"="+c.Run)
	}
	return out
}

func TestCommandsFromCIClassifiesRoles(t *testing.T) {
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
jobs:
  build:
    steps:
      - checkout
      - run: yarn install --frozen-lockfile
      - run:
          name: Format
          command: yarn prettier --write .
      - run: yarn eslint .
      - run: yarn test
workflows:
  main:
    jobs:
      - build
`)
	got := commandsFromCI(dir).Commands

	// Emitted in install, test, lint, format order regardless of config order.
	assert.DeepEqual(t, runs(got), []string{
		"install=yarn install --frozen-lockfile",
		"test=yarn test",
		"lint=yarn eslint .",
		"format=yarn prettier --write .",
	})

	assert.Equal(t, got[1].Role, config.RoleGate)
	assert.Equal(t, got[1].Timeout, 300)
	assert.Equal(t, got[2].Role, config.RoleGate)
	assert.Equal(t, got[3].Role, config.RoleAutofix)
	// The install step is not a gate and carries no role.
	assert.Equal(t, got[0].Role, "")
}

func TestCommandsFromCIKeepsFirstPerRole(t *testing.T) {
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
jobs:
  test:
    steps:
      - run: task ci:test
  acceptance:
    steps:
      - run: task ci:test -- ./acceptance/...
workflows:
  main:
    jobs:
      - test
      - acceptance
`)
	assert.DeepEqual(t, runs(commandsFromCI(dir).Commands), []string{"test=task ci:test"})
}

func TestCommandsFromCISkipsNonInnerLoopSteps(t *testing.T) {
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
jobs:
  build:
    steps:
      - run: go test ./...
      - run: ./deploy.sh production
      - run: docker push example/app:latest
      - run: bash <(curl -s https://codecov.io/bash)
      - run: aws s3 cp dist s3://bucket
      - run: terraform apply -auto-approve
workflows:
  main:
    jobs:
      - build
`)
	assert.DeepEqual(t, runs(commandsFromCI(dir).Commands), []string{"test=go test ./..."})
}

func TestCommandsFromCISkipsUnrunnableSteps(t *testing.T) {
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
jobs:
  build:
    steps:
      - run:
          command: pytest
          working_directory: /home/circleci/project/api
  other:
    steps:
      - run:
          name: Run tests
          command: |
            mkdir -p reports
            pytest --junitxml=reports/out.xml
      - run: echo 'export PATH=/x:$PATH' >> "$BASH_ENV"
workflows:
  main:
    jobs:
      - build
      - other
`)
	// An absolute working directory, a multi-line script, and a step that only
	// works inside a CircleCI container are all unusable verbatim.
	assert.Equal(t, len(commandsFromCI(dir).Commands), 0)
}

func TestCommandsFromCIKeepsSubdirectoryStepsWithACdPrefix(t *testing.T) {
	// A monorepo's real gates live in subdirectories. Dropping them left
	// detection with nothing and sent it back to guessing from root filenames.
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
jobs:
  build:
    steps:
      - run:
          name: Frontend tests
          working_directory: frontend
          command: yarn test
      - run:
          name: Lint
          working_directory: services/api
          command: ruff check .
workflows:
  main:
    jobs:
      - build
`)
	assert.DeepEqual(t, runs(commandsFromCI(dir).Commands), []string{
		"test=cd frontend && yarn test",
		"lint=cd services/api && ruff check .",
	})
}

func TestCommandsFromCIGroupsSubdirectoryCommandsWithOperators(t *testing.T) {
	// `cd web && yarn lint || true` parses as `(cd web && yarn lint) || true`,
	// so a failed cd reports a pass; `cd web && a; b` runs b from the root
	// whatever cd did. The command has to keep its own precedence.
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
jobs:
  build:
    steps:
      - run:
          name: Lint
          working_directory: web
          command: yarn lint || true
      - run:
          name: Tests
          working_directory: web
          command: yarn build; yarn test
workflows:
  main:
    jobs:
      - build
`)
	assert.DeepEqual(t, runs(commandsFromCI(dir).Commands), []string{
		"test=cd web && ( yarn build; yarn test )",
		"lint=cd web && ( yarn lint || true )",
	})
}

func TestCommandsFromCIRejectsWorkingDirectoriesOutsideTheRepo(t *testing.T) {
	for _, dirName := range []string{"~/other", "/opt/build", "../sibling", "$HOME/x", "a dir"} {
		t.Run(dirName, func(t *testing.T) {
			dir := t.TempDir()
			writeCI(t, dir, `
version: 2.1
jobs:
  build:
    steps:
      - run:
          working_directory: `+dirName+`
          command: pytest
workflows:
  main:
    jobs:
      - build
`)
			assert.Equal(t, len(commandsFromCI(dir).Commands), 0)
		})
	}
}

func TestCommandsFromCIIgnoresScheduledWorkflows(t *testing.T) {
	// A nightly is not a branch gate, and is routinely a slower superset of the
	// real checks. Sorting workflows by name let this one win on "a" < "z".
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
jobs:
  nightly:
    steps:
      - run: make test-everything
  test:
    steps:
      - run: go test ./...
workflows:
  a-nightly:
    triggers:
      - schedule:
          cron: "0 0 * * *"
          filters:
            branches:
              only: [main]
    jobs:
      - nightly
  z-ci:
    jobs:
      - test
`)
	assert.DeepEqual(t, runs(commandsFromCI(dir).Commands), []string{"test=go test ./..."})
}

func TestCommandsFromCIPrefersTheFirstWorkflowInTheFile(t *testing.T) {
	// Document order, not alphabetical: a config's primary workflow is
	// conventionally written first.
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
jobs:
  primary:
    steps:
      - run: go test ./...
  extended:
    steps:
      - run: go test -tags=extended ./...
workflows:
  zz-primary:
    jobs:
      - primary
  aa-extended:
    jobs:
      - extended
`)
	assert.DeepEqual(t, runs(commandsFromCI(dir).Commands), []string{"test=go test ./..."})
}

func TestCommandsFromCIKeepsToolNamesInsideArguments(t *testing.T) {
	// Provisioning tools are skipped where they are invoked, not wherever their
	// name appears: matching "aws " anywhere dropped a real test command.
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
jobs:
  build:
    steps:
      - run: pytest tests/aws_client tests/helm_chart
      - run: aws s3 cp dist s3://bucket
      - run: mkdir -p reports && curl -sSL https://example.com/x | sh
workflows:
  main:
    jobs:
      - build
`)
	assert.DeepEqual(t, runs(commandsFromCI(dir).Commands), []string{
		"test=pytest tests/aws_client tests/helm_chart",
	})
}

func TestCommandsFromCIReportsWhatItCouldNotRead(t *testing.T) {
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
orbs:
  node: circleci/node@5
jobs:
  build:
    steps:
      - node/install-packages
      - run: yarn test
workflows:
  main:
    jobs:
      - build
`)
	det := commandsFromCI(dir)
	assert.Equal(t, det.Source, ".circleci/config.yml")
	assert.DeepEqual(t, det.Notes, []string{
		"steps from orbs were not read: node/install-packages",
	})
}

func TestDetectCommandsExplainsAnUnusableCIConfig(t *testing.T) {
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644))
	writeCI(t, dir, "version: 2.1\nsetup: true\n")

	det, err := DetectCommands(context.Background(), nil, dir)
	assert.NilError(t, err)
	assert.Equal(t, det.Source, sourceLayout)
	assert.DeepEqual(t, det.Notes, []string{
		"the config generates its real config at run time, so its checks are not visible",
		"no runnable checks were found in .circleci/config.yml",
	})
}

func TestDetectCommandsKeepsTheToolchainFormatter(t *testing.T) {
	// CI gates formatting by diffing the tree, so it names no command that
	// rewrites files. Taking it literally left the user with no formatter.
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "Taskfile.yml"), []byte("version: '3'\n"), 0o644))
	writeCI(t, dir, `
version: 2.1
jobs:
  build:
    steps:
      - run: task ci:test
      - run: task ci:lint
      - run: task ci:diff
workflows:
  main:
    jobs:
      - build
`)
	det, err := DetectCommands(context.Background(), nil, dir)
	assert.NilError(t, err)
	assert.Equal(t, det.Source, ".circleci/config.yml")

	// The gates come from CI; only the missing autofix falls back.
	assert.DeepEqual(t, runs(det.Commands), []string{
		"test=task ci:test",
		"lint=task ci:lint",
		"format=task fmt",
	})
}

func TestDetectCommandsDoesNotOverrideACIFormatter(t *testing.T) {
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644))
	writeCI(t, dir, `
version: 2.1
jobs:
  build:
    steps:
      - run: go test ./...
      - run: goimports -w .
workflows:
  main:
    jobs:
      - build
`)
	det, err := DetectCommands(context.Background(), nil, dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, runs(det.Commands), []string{
		"test=go test ./...",
		"format=goimports -w .",
	})
}

func TestDetectCommandsExplainsAnUnparseableConfig(t *testing.T) {
	// Falling back silently reads as "your config was read and had nothing in
	// it", which is exactly the surprise Notes exists to prevent.
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644))
	writeCI(t, dir, "version: 2.1\njobs:\n  test:\n   steps:\n  - run: go test ./...\n\t- bad\n")

	det := commandsFromCI(dir)
	assert.Equal(t, det.Source, ".circleci/config.yml")
	assert.Equal(t, len(det.Commands), 0)
	assert.Equal(t, len(det.Notes), 1)
	assert.Assert(t, strings.Contains(det.Notes[0], "could not be read"), det.Notes[0])

	// The whole path through detection keeps the explanation and still falls
	// back to the toolchain.
	full, err := DetectCommands(context.Background(), nil, dir)
	assert.NilError(t, err)
	assert.Equal(t, full.Source, sourceLayout)
	assert.Assert(t, len(full.Commands) > 0)
	assert.Assert(t, strings.Contains(strings.Join(full.Notes, "\n"), "could not be read"),
		strings.Join(full.Notes, "\n"))
	assert.Assert(t, strings.Contains(strings.Join(full.Notes, "\n"), ".circleci/config.yml"),
		strings.Join(full.Notes, "\n"))
}

func TestDetectCommandsKeepsFormatterAlongsideACheckOnlyOne(t *testing.T) {
	// A check-only formatter is a real gate, but it cannot fix anything. If it
	// takes the format slot the user gets an autofix that always fails and the
	// toolchain's real fixer is suppressed.
	for _, tc := range []struct {
		name  string
		step  string
		check string
	}{
		{"prettier", "prettier --check .", "prettier --check ."},
		{"cargo", "cargo fmt --check", "cargo fmt --check"},
		{"gofmt list", "test -z \"$(gofmt -l .)\"", "test -z \"$(gofmt -l .)\""},
		{"named task", "task ci:fmt-check", "task ci:fmt-check"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			assert.NilError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644))
			writeCI(t, dir, `
version: 2.1
jobs:
  build:
    steps:
      - run: go test ./...
      - run: `+tc.step+`
workflows:
  main:
    jobs:
      - build
`)
			det, err := DetectCommands(context.Background(), nil, dir)
			assert.NilError(t, err)
			assert.DeepEqual(t, runs(det.Commands), []string{
				"test=go test ./...",
				"format-check=" + tc.check,
				"format=gofmt -w .",
			})
		})
	}
}

func TestClassifyFormatCheckDoesNotFireOnRewritingFormatters(t *testing.T) {
	// The check detection reads flags, so it must not misread a flag that
	// happens to contain one of them.
	for _, cmd := range []string{
		"gofmt -w .", "goimports -w .", "cargo fmt", "task fmt",
		"prettier --write .", "ruff format .", "rustfmt src/main.rs",
		"go test -ldflags=-s ./... && task fmt",
	} {
		assert.Equal(t, refineFormat(roleFormat, cmd), roleFormat, cmd)
	}
	for _, cmd := range []string{
		"prettier --check .", "cargo fmt --check", "gofmt -l .",
		"ruff format --diff", "prettier --list-different .",
		"task format:check", "npm run check-format", "gofmt -d .",
	} {
		assert.Equal(t, refineFormat(roleFormat, cmd), roleFormatCheck, cmd)
	}
}

func TestCommandsFromCIKeepsJobsWithAWorkingDirectory(t *testing.T) {
	// working_directory at job level is standard boilerplate for the checkout
	// path. Reading it as a subdirectory silently disabled detection.
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
jobs:
  test:
    working_directory: ~/repo
    steps:
      - run: yarn install --frozen-lockfile
      - run: yarn test
workflows:
  main:
    jobs:
      - test
`)
	assert.DeepEqual(t, runs(commandsFromCI(dir).Commands), []string{
		"install=yarn install --frozen-lockfile",
		"test=yarn test",
	})
}

func TestCommandsFromCIKeepsBuildProfileFlags(t *testing.T) {
	// "release" as a bare skip marker swallowed `cargo test --release`.
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
jobs:
  test:
    steps:
      - run: cargo test --release
      - run: ./release.sh
workflows:
  main:
    jobs:
      - test
`)
	assert.DeepEqual(t, runs(commandsFromCI(dir).Commands), []string{"test=cargo test --release"})
}

func TestCommandsFromCIIgnoresToolInstalls(t *testing.T) {
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
jobs:
  test:
    steps:
      - run: cargo install cargo-nextest
      - run: cargo fetch --locked
      - run: cargo nextest run
workflows:
  main:
    jobs:
      - test
`)
	// Installing a test binary is not the project's dependency install, so it
	// must not claim the install slot.
	got := runs(commandsFromCI(dir).Commands)
	assert.Assert(t, !slices.Contains(got, "install=cargo install cargo-nextest"), "got: %v", got)
}

func TestCommandsFromCIFallsBackWhenUnavailable(t *testing.T) {
	t.Run("no config", func(t *testing.T) {
		assert.Assert(t, len(commandsFromCI(t.TempDir()).Commands) == 0)
	})

	t.Run("dynamic config", func(t *testing.T) {
		dir := t.TempDir()
		writeCI(t, dir, `
version: 2.1
setup: true
jobs:
  setup:
    steps:
      - run: ./generate.sh
workflows:
  setup:
    jobs:
      - setup
`)
		assert.Assert(t, len(commandsFromCI(dir).Commands) == 0)
	})

	t.Run("nothing classifies", func(t *testing.T) {
		dir := t.TempDir()
		writeCI(t, dir, `
version: 2.1
jobs:
  build:
    steps:
      - run: ./deploy.sh
workflows:
  main:
    jobs:
      - build
`)
		assert.Assert(t, len(commandsFromCI(dir).Commands) == 0)
	})
}

func TestCommandsFromCIUsesStepNameWhenCommandIsOpaque(t *testing.T) {
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
jobs:
  build:
    steps:
      - run:
          name: Run unit tests
          command: ./scripts/ci.sh
workflows:
  main:
    jobs:
      - build
`)
	assert.DeepEqual(t, runs(commandsFromCI(dir).Commands), []string{"test=./scripts/ci.sh"})
}

func TestDetectCommandsPrefersCIConfig(t *testing.T) {
	// The reported failure: a bazel/java repo whose stray manifests outrank
	// its real build system in filename detection.
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), []byte(""), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte("[project]\n"), 0o644))
	writeCI(t, dir, `
version: 2.1
jobs:
  test:
    steps:
      - run: bazel test //...
workflows:
  main:
    jobs:
      - test
`)
	det, err := DetectCommands(context.Background(), nil, dir)
	assert.NilError(t, err)

	// Without the CircleCI config this would have been "pnpm test".
	assert.DeepEqual(t, runs(det.Commands), []string{"test=bazel test //..."})
}

func TestDetectCommandsFallsBackToFilenames(t *testing.T) {
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644))

	det, err := DetectCommands(context.Background(), nil, dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, runs(det.Commands), []string{
		"test=go test ./...",
		"lint=golangci-lint run ./...",
		"format=gofmt -w .",
	})
}

func TestDetectCommandsIgnoresUnusableCIConfig(t *testing.T) {
	// A dynamic config must not suppress filename detection.
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644))
	writeCI(t, dir, "version: 2.1\nsetup: true\n")

	det, err := DetectCommands(context.Background(), nil, dir)
	assert.NilError(t, err)
	assert.Equal(t, det.Commands[0].Run, "go test ./...")
}

func TestDetectCommandsKeepsATestGateCIDoesNotName(t *testing.T) {
	// A suite behind a multi-line script does not classify. Trusting CI for the
	// rest and dropping test would write a config that validates nothing.
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644))
	writeCI(t, dir, `
version: 2.1
jobs:
  build:
    steps:
      - run: golangci-lint run ./...
      - run:
          name: Suite
          command: |
            ./scripts/prepare.sh
            go test ./...
workflows:
  main:
    jobs:
      - build
`)
	det, err := DetectCommands(context.Background(), nil, dir)
	assert.NilError(t, err)
	assert.Equal(t, det.Source, ".circleci/config.yml")

	// The backfilled test sorts ahead of CI's lint, as CI's own test would have.
	assert.DeepEqual(t, runs(det.Commands), []string{
		"test=go test ./...",
		"lint=golangci-lint run ./...",
		"format=gofmt -w .",
	})
	// And the substitution is stated rather than silent.
	assert.Assert(t, slices.ContainsFunc(det.Notes, func(n string) bool {
		return strings.Contains(n, "no test command was found in .circleci/config.yml")
	}), strings.Join(det.Notes, "\n"))
}

func TestDetectCommandsDoesNotInventGatesBeyondTest(t *testing.T) {
	// CI naming no lint gate is a real answer: golangci-lint may not even be
	// installed, and a gate that always fails is worse than no gate.
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644))
	writeCI(t, dir, `
version: 2.1
jobs:
  build:
    steps:
      - run: go test ./...
workflows:
  main:
    jobs:
      - build
`)
	det, err := DetectCommands(context.Background(), nil, dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, runs(det.Commands), []string{
		"test=go test ./...",
		"format=gofmt -w .",
	})
	assert.Equal(t, len(det.Notes), 0)
}

func TestDetectCommandsSaysWhenNothingNamesATest(t *testing.T) {
	// CI gates on lint only and the toolchain is unknown, so there is nothing to
	// fill the test slot with. The gap is stated instead of shipped silently.
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
jobs:
  build:
    steps:
      - run: shellcheck ./bin/*
workflows:
  main:
    jobs:
      - build
`)
	det, err := DetectCommands(context.Background(), nil, dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, runs(det.Commands), []string{"lint=shellcheck ./bin/*"})
	assert.DeepEqual(t, det.Notes, []string{
		"no test command was found in .circleci/config.yml, and the repository layout does not suggest one",
	})
}

func TestDetectCommandsExplainsAnUnusableConfigWithNothingToFallBackOn(t *testing.T) {
	// Unknown toolchain and no Claude client: the user gets no commands at all,
	// which is precisely when the config needs explaining.
	dir := t.TempDir()
	writeCI(t, dir, "version: 2.1\nsetup: true\n")

	det, err := DetectCommands(context.Background(), nil, dir)
	assert.NilError(t, err)
	assert.Equal(t, len(det.Commands), 0)
	assert.DeepEqual(t, det.Notes, []string{
		"the config generates its real config at run time, so its checks are not visible",
		"no runnable checks were found in .circleci/config.yml",
	})
}

func TestDetectCommandsExplainsAnUnusableConfigWhenClaudeAnswersEmpty(t *testing.T) {
	dir := t.TempDir()
	writeCI(t, dir, "version: 2.1\nsetup: true\n")

	srv := httptest.NewServer(fakes.NewFakeAnthropic(""))
	defer srv.Close()
	claude, err := anthropic.New(anthropic.Config{APIKey: "sk-ant-fake", BaseURL: srv.URL})
	assert.NilError(t, err)

	det, err := DetectCommands(context.Background(), claude, dir)
	assert.NilError(t, err)
	assert.Equal(t, len(det.Commands), 0)
	assert.DeepEqual(t, det.Notes, []string{
		"the config generates its real config at run time, so its checks are not visible",
		"no runnable checks were found in .circleci/config.yml",
	})
}

func TestClassifySkipsCircleCICLISteps(t *testing.T) {
	// The CLI is pipeline infrastructure: the split idiom needs the binary plus
	// CIRCLE_NODE_TOTAL and CIRCLE_NODE_INDEX, so locally it is either not found
	// or splits nothing.
	for _, cmd := range []string{
		"gotestsum -- $(go list ./... | circleci tests split --split-by=timings)",
		`pytest $(circleci tests glob "tests/**/*.py")`,
		"circleci tests run --command='go test ./...'",
		"circleci-agent step halt",
	} {
		assert.Equal(t, classify(ciconfig.Candidate{Command: cmd}), roleNone, cmd)
	}
}

func TestClassifyKeepsCircleCIInsideAPath(t *testing.T) {
	// The tool has to be recognized where it is invoked; as a bare substring it
	// would drop a suite that merely has a circleci-named test directory.
	assert.Equal(t, classify(ciconfig.Candidate{Command: "pytest tests/circleci_client"}), roleTest)
}

func TestClassifyKeepsGatesWritingUnderAnArtifactsPath(t *testing.T) {
	// "artifact" as a bare substring dropped these, and writing a junit or
	// coverage report under an artifacts path is what a test job normally does.
	for cmd, want := range map[string]string{
		"mkdir -p /tmp/artifacts && pytest --junitxml=/tmp/artifacts/results.xml": roleTest,
		"go test -coverprofile=/tmp/artifacts/cover.out ./...":                    roleTest,
		"yarn eslint . --output-file /tmp/artifacts/eslint.json":                  roleLint,
	} {
		assert.Equal(t, classify(ciconfig.Candidate{Command: cmd}), want, cmd)
	}
}

func TestClassifySkipsArtifactShipping(t *testing.T) {
	for _, cmd := range []string{
		"make store-artifacts",
		"./scripts/collect_artifacts.sh",
		"task archive-artifact",
	} {
		assert.Equal(t, classify(ciconfig.Candidate{Command: cmd}), roleNone, cmd)
	}
}

func TestCommandsFromCINamesTheBranchWhenNoJobGatesIt(t *testing.T) {
	// A develop-default repo used to get "no runnable checks were found", which
	// reads as "your config holds nothing" rather than "nothing in it runs on
	// the branch I looked at".
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
jobs:
  test:
    steps:
      - run: pytest
workflows:
  main:
    jobs:
      - test:
          filters:
            branches:
              only: [develop]
`)
	det := commandsFromCI(dir)
	assert.Equal(t, len(det.Commands), 0)
	assert.DeepEqual(t, det.Notes, []string{"no job in the config runs on main or master"})
}

func TestCommandsFromCIUsesTheRepoDefaultBranch(t *testing.T) {
	// The same config, in a repo that really does default to develop.
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
jobs:
  test:
    steps:
      - run: pytest
workflows:
  main:
    jobs:
      - test:
          filters:
            branches:
              only: [develop]
`)
	initRepoWithDefaultBranch(t, dir, "develop")

	det := commandsFromCI(dir)
	assert.DeepEqual(t, runs(det.Commands), []string{"test=pytest"})
	assert.Equal(t, len(det.Notes), 0)
}

// initRepoWithDefaultBranch makes dir a git repo whose origin/HEAD points at
// branch, which is what DefaultBranchIn reads.
func initRepoWithDefaultBranch(t *testing.T, dir, branch string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "--initial-branch=" + branch},
		{"remote", "add", "origin", "https://example.com/x/y.git"},
		{"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/" + branch},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		assert.NilError(t, err, string(out))
	}
}
