package ciconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

// writeConfig writes body to .circleci/<name> in a fresh temp dir.
func writeConfig(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".circleci"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".circleci", name), []byte(body), 0o644))
	return dir
}

// commands flattens a result to just the command strings, for terse asserts.
func commands(r *Result) []string {
	out := make([]string, 0, len(r.Candidates))
	for _, c := range r.Candidates {
		out = append(out, c.Command)
	}
	return out
}

func TestExtractMissingConfig(t *testing.T) {
	_, err := Extract(t.TempDir())
	assert.Assert(t, errors.Is(err, ErrNotFound), "got: %v", err)
}

func TestExtractReadsYamlExtension(t *testing.T) {
	dir := writeConfig(t, "config.yaml", `
version: 2.1
jobs:
  test:
    steps:
      - run: go test ./...
workflows:
  main:
    jobs:
      - test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, commands(res), []string{"go test ./..."})
}

func TestExtractRunStepForms(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
jobs:
  test:
    steps:
      - checkout
      - run: yarn install --frozen-lockfile
      - run:
          name: Run tests
          command: yarn test
      - run:
          name: Dev server
          command: yarn start
          background: true
      - store_test_results:
          path: reports
workflows:
  main:
    jobs:
      - test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)

	assert.DeepEqual(t, commands(res), []string{
		"yarn install --frozen-lockfile",
		"yarn test",
	})
	assert.Equal(t, res.Candidates[1].Step, "Run tests")
	assert.Equal(t, res.Candidates[1].Job, "test")
	// The unnamed step keeps an empty name rather than inventing one.
	assert.Equal(t, res.Candidates[0].Step, "")
}

func TestExtractDynamicConfig(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
setup: true
orbs:
  continuation: circleci/continuation@1.0.0
jobs:
  setup:
    steps:
      - run: ./generate-config.sh
workflows:
  setup:
    jobs:
      - setup
`)
	res, err := Extract(dir)
	assert.NilError(t, err)

	assert.Assert(t, res.Dynamic)
	// An empty list here means "could not tell", not "no gates".
	assert.Equal(t, len(res.Candidates), 0)
}

func TestExtractSkipsNonGateJobs(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
jobs:
  test:
    steps:
      - run: pytest
  hold:
    steps:
      - run: echo never
  deploy:
    steps:
      - run: ./deploy.sh
  release:
    steps:
      - run: ./release.sh
  nightly:
    steps:
      - run: ./nightly.sh
workflows:
  main:
    jobs:
      - test
      - hold:
          type: approval
      - deploy:
          requires: [hold]
          filters:
            branches:
              only: main
      - release:
          filters:
            branches:
              only: /release-.*/
      - nightly:
          filters:
            branches:
              ignore: main
`)
	res, err := Extract(dir)
	assert.NilError(t, err)

	// deploy survives: it is filtered *to* main, which is a default branch.
	assert.DeepEqual(t, commands(res), []string{"pytest", "./deploy.sh"})
}

func TestExtractBranchFilterLists(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
jobs:
  a:
    steps:
      - run: echo a
  b:
    steps:
      - run: echo b
workflows:
  main:
    jobs:
      - a:
          filters:
            branches:
              only: [develop, master]
      - b:
          filters:
            branches:
              only: [develop, staging]
`)
	res, err := Extract(dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, commands(res), []string{"echo a"})
}

func TestExtractExpandsCustomCommands(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
commands:
  install:
    parameters:
      pkg-manager:
        type: string
        default: yarn
    steps:
      - run:
          name: Install
          command: << parameters.pkg-manager >> install --frozen-lockfile
  suite:
    parameters:
      target:
        type: string
    steps:
      - run: bazel test <<parameters.target>>
jobs:
  test:
    steps:
      - install
      - suite:
          target: "//..."
workflows:
  main:
    jobs:
      - test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)

	assert.DeepEqual(t, commands(res), []string{
		"yarn install --frozen-lockfile",
		"bazel test //...",
	})
	assert.Equal(t, res.Unresolved, 0)
}

func TestExtractCountsUnresolvedParameters(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
commands:
  suite:
    parameters:
      target:
        type: string
    steps:
      - run: bazel test << parameters.target >>
jobs:
  test:
    steps:
      - suite:
          target:
            - "//a/..."
            - "//b/..."
workflows:
  main:
    jobs:
      - test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)

	// A non-scalar parameter cannot be substituted, so the step is reported
	// rather than emitted with a literal << parameters.target >> in it.
	assert.Equal(t, len(res.Candidates), 0)
	assert.Equal(t, res.Unresolved, 1)
}

func TestExtractKeepsJobsWithAMalformedBranchFilter(t *testing.T) {
	for _, tc := range []struct {
		name    string
		filters string
	}{
		{"ignore", `ignore: ["/release-[/"]`},
		{"only", `only: ["/release-[/"]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeConfig(t, "config.yml", `
version: 2.1
jobs:
  test:
    steps:
      - run: go test ./...
workflows:
  main:
    jobs:
      - test:
          filters:
            branches:
              `+tc.filters+`
`)
			res, err := Extract(dir)
			assert.NilError(t, err)

			// An unparseable filter says nothing about the default branch, so
			// it must not be the reason detection finds nothing.
			assert.Equal(t, len(res.Candidates), 1)
		})
	}
}

func TestExtractSkipsOutcomeConditionalSteps(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
jobs:
  test:
    steps:
      - run:
          name: Show test failures
          when: on_fail
          command: ./scripts/dump-logs.sh
      - run:
          name: Upload artifacts
          when: always
          command: ./scripts/upload.sh
      - run: go test ./...
workflows:
  main:
    jobs:
      - test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)

	// The log dumper runs only on red and the uploader is cleanup; neither
	// gates the build, so neither may take the test role from go test.
	assert.Equal(t, len(res.Candidates), 1)
	assert.Equal(t, res.Candidates[0].Command, "go test ./...")
}

func TestExtractCountsPipelineLevelReferences(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
parameters:
  workers:
    type: integer
    default: 4
jobs:
  test:
    steps:
      - run: pytest -n << pipeline.parameters.workers >>
      - run: echo "building << pipeline.git.branch >>"
      - run: tox -e << matrix.env >>
      - run: go test ./...
workflows:
  main:
    jobs:
      - test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)

	// Only CircleCI resolves pipeline, git and matrix references. Emitting one
	// verbatim would put a bash heredoc operator in the command we run, so the
	// steps are reported instead.
	assert.Equal(t, res.Unresolved, 3)
	assert.Equal(t, len(res.Candidates), 1)
	assert.Equal(t, res.Candidates[0].Command, "go test ./...")
}

func TestExtractKeepsHeredocCommands(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
jobs:
  test:
    steps:
      - run: |
          cat <<EOF > cfg
          key = value
          EOF
          go test ./...
workflows:
  main:
    jobs:
      - test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)

	// A real heredoc delimiter is not an interpolation.
	assert.Equal(t, res.Unresolved, 0)
	assert.Equal(t, len(res.Candidates), 1)
}

func TestExtractRecordsOrbSteps(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
orbs:
  node: circleci/node@5.0.0
  slack: circleci/slack@4.0.0
jobs:
  test:
    steps:
      - checkout
      - node/install-packages
      - run: yarn test
      - slack/notify:
          event: fail
workflows:
  main:
    jobs:
      - test
      - node/test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)

	assert.DeepEqual(t, commands(res), []string{"yarn test"})
	// Both in-job orb steps and orb-provided jobs are reported as blind spots.
	assert.DeepEqual(t, res.SkippedOrbs, []string{
		"node/install-packages",
		"node/test",
		"slack/notify",
	})
}

func TestExtractWalksStepsPassedToOrbCommands(t *testing.T) {
	// go/with-cache and friends take the caller's own steps as a parameter.
	// The orb's steps are opaque, but the wrapped ones are the real gates.
	dir := writeConfig(t, "config.yml", `
version: 2.1
orbs:
  go: circleci/go@4.0.0
jobs:
  test:
    steps:
      - checkout
      - run: task mod-download
      - go/with-cache:
          golangci-lint: true
          steps:
            - run: task ci:lint
            - store_test_results:
                path: ./test-reports
            - run:
                name: Run tests
                command: task ci:test
workflows:
  main:
    jobs:
      - test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)

	assert.DeepEqual(t, commands(res), []string{
		"task mod-download",
		"task ci:lint",
		"task ci:test",
	})
	// The orb is still reported: its own steps remain unknown.
	assert.DeepEqual(t, res.SkippedOrbs, []string{"go/with-cache"})
}

func TestExtractWalksStepsPassedToCustomCommands(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
commands:
  with-setup:
    parameters:
      steps:
        type: steps
    steps:
      - run: ./setup.sh
      - steps: << parameters.steps >>
jobs:
  test:
    steps:
      - with-setup:
          steps:
            - run: pytest
workflows:
  main:
    jobs:
      - test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, commands(res), []string{"./setup.sh", "pytest"})
}

func TestExtractWalksConditionalSteps(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
jobs:
  test:
    steps:
      - when:
          condition: << pipeline.parameters.run-lint >>
          steps:
            - run: golangci-lint run ./...
      - unless:
          condition: false
          steps:
            - run: go test ./...
workflows:
  main:
    jobs:
      - test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, commands(res), []string{
		"golangci-lint run ./...",
		"go test ./...",
	})
}

func TestExtractResolvesAnchors(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
shared: &shared
  steps:
    - run: make test
    - run: make lint
jobs:
  test:
    <<: *shared
workflows:
  main:
    jobs:
      - test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, commands(res), []string{"make test", "make lint"})
}

func TestExtractDeduplicatesAndIgnoresWorkflowVersion(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2
jobs:
  test:
    steps:
      - run: make test
  test-again:
    steps:
      - run: make test
workflows:
  version: 2
  main:
    jobs:
      - test
      - test-again
`)
	res, err := Extract(dir)
	assert.NilError(t, err)
	// `workflows: version: 2` is a scalar, not a workflow, and the repeated
	// command collapses to one candidate.
	assert.DeepEqual(t, commands(res), []string{"make test"})
}

func TestExtractStopsCommandRecursion(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
commands:
  a:
    steps:
      - run: echo a
      - b
  b:
    steps:
      - run: echo b
      - a
jobs:
  test:
    steps:
      - a
workflows:
  main:
    jobs:
      - test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, commands(res), []string{"echo a", "echo b"})
}

func TestExtractSubstitutesJobParameters(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
jobs:
  test:
    parameters:
      python:
        type: string
        default: "3.11"
      suite:
        type: string
        default: unit
    steps:
      - run: pytest --python=<< parameters.python >> tests/<< parameters.suite >>
workflows:
  main:
    jobs:
      - test:
          name: test-3.12-integration
          python: "3.12"
          suite: integration
      - test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)

	// The override wins for the first entry; the bare entry falls back to
	// the declared defaults. Both are kept — different commands.
	assert.DeepEqual(t, commands(res), []string{
		"pytest --python=3.12 tests/integration",
		"pytest --python=3.11 tests/unit",
	})
	assert.Equal(t, res.Unresolved, 0)
}

func TestExtractForwardsJobParametersIntoCustomCommands(t *testing.T) {
	// A job passing its own parameter through to a command it invokes. Without
	// resolving the value first the reference reached the command body intact
	// and every step in the job was discarded as unresolved.
	dir := writeConfig(t, "config.yml", `
version: 2.1
commands:
  run-suite:
    parameters:
      suite:
        type: string
    steps:
      - run: pytest tests/<< parameters.suite >>
jobs:
  test:
    parameters:
      suite:
        type: string
        default: unit
    steps:
      - run-suite:
          suite: << parameters.suite >>
workflows:
  main:
    jobs:
      - test:
          suite: integration
      - test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, commands(res), []string{
		"pytest tests/integration",
		"pytest tests/unit",
	})
	assert.Equal(t, res.Unresolved, 0)
}

func TestExtractSkipsScheduledWorkflows(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
jobs:
  nightly:
    steps:
      - run: make test-everything
  test:
    steps:
      - run: go test ./...
workflows:
  nightly:
    triggers:
      - schedule:
          cron: "0 0 * * *"
          filters:
            branches:
              only:
                - main
    jobs:
      - nightly
  ci:
    jobs:
      - test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)

	// A cron workflow does not gate a push, so its jobs are not candidates.
	assert.DeepEqual(t, commands(res), []string{"go test ./..."})
}

func TestExtractWalksWorkflowsInDocumentOrder(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
jobs:
  first:
    steps:
      - run: echo first
  second:
    steps:
      - run: echo second
workflows:
  zz-declared-first:
    jobs:
      - first
  aa-declared-second:
    jobs:
      - second
`)
	res, err := Extract(dir)
	assert.NilError(t, err)

	// Callers keep the first candidate per role, so the order has to follow
	// the file rather than the workflow names.
	assert.DeepEqual(t, commands(res), []string{"echo first", "echo second"})
}

func TestExtractWalksPreAndPostSteps(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
jobs:
  test:
    steps:
      - run: go test ./...
workflows:
  main:
    jobs:
      - test:
          pre-steps:
            - run: go mod download
          post-steps:
            - run: ./upload-coverage.sh
`)
	res, err := Extract(dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, commands(res), []string{
		"go mod download",
		"go test ./...",
		"./upload-coverage.sh",
	})
}

func TestExtractRecordsStepWorkingDirectory(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2.1
jobs:
  test:
    steps:
      - run:
          command: pytest
          working_directory: services/api
      - run:
          command: pytest
          working_directory: web
workflows:
  main:
    jobs:
      - test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)

	// Same command, different directories — two distinct pieces of work, so
	// dedupe must not collapse them.
	assert.Equal(t, len(res.Candidates), 2)
	assert.Equal(t, res.Candidates[0].WorkingDir, "services/api")
	assert.Equal(t, res.Candidates[1].WorkingDir, "web")
}

func TestExtractIgnoresJobWorkingDirectory(t *testing.T) {
	// A job-level working_directory is where checkout puts the repo, so it is
	// the repo root — not a narrowing of where the command runs. Treating it
	// as one would silently discard every step in configs that set it, which
	// is common boilerplate.
	dir := writeConfig(t, "config.yml", `
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
	res, err := Extract(dir)
	assert.NilError(t, err)

	assert.DeepEqual(t, commands(res), []string{
		"yarn install --frozen-lockfile",
		"yarn test",
	})
	assert.Equal(t, res.Candidates[0].WorkingDir, "")
}

func TestExtractLegacyConfigWithoutWorkflows(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2
jobs:
  build:
    steps:
      - run: make test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)
	// CircleCI 2.0 runs the "build" job implicitly when there is no workflow.
	assert.DeepEqual(t, commands(res), []string{"make test"})
}

func TestExtractLegacyConfigWithoutBuildJob(t *testing.T) {
	dir := writeConfig(t, "config.yml", `
version: 2
jobs:
  something-else:
    steps:
      - run: make test
`)
	res, err := Extract(dir)
	assert.NilError(t, err)
	assert.Equal(t, len(res.Candidates), 0)
}

func TestExtractTruncatesRunawayConfig(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("version: 2.1\njobs:\n  test:\n    steps:\n")
	for i := range maxCandidates + 50 {
		fmt.Fprintf(&sb, "      - run: echo step-%d\n", i)
	}
	sb.WriteString("workflows:\n  main:\n    jobs:\n      - test\n")

	res, err := Extract(writeConfig(t, "config.yml", sb.String()))
	assert.NilError(t, err)

	assert.Equal(t, len(res.Candidates), maxCandidates)
	assert.Equal(t, res.Truncated, 50)
}

func TestExtractMalformedYAML(t *testing.T) {
	dir := writeConfig(t, "config.yml", "jobs:\n  test:\n   - bad\n  indent\n")
	_, err := Extract(dir)
	assert.ErrorContains(t, err, "parse")
}

func TestExtractEmptyConfig(t *testing.T) {
	dir := writeConfig(t, "config.yml", "version: 2.1\n")
	res, err := Extract(dir)
	assert.NilError(t, err)
	assert.Equal(t, len(res.Candidates), 0)
}
