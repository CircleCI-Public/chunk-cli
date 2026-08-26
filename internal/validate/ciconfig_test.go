package validate

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
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
	got := commandsFromCI(dir)

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
	assert.DeepEqual(t, runs(commandsFromCI(dir)), []string{"test=task ci:test"})
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
	assert.DeepEqual(t, runs(commandsFromCI(dir)), []string{"test=go test ./..."})
}

func TestCommandsFromCISkipsUnrunnableSteps(t *testing.T) {
	dir := t.TempDir()
	writeCI(t, dir, `
version: 2.1
jobs:
  build:
    working_directory: ~/repo/services/api
    steps:
      - run: pytest
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
	// A subdirectory-scoped command, a multi-line script, and a step that
	// only works inside a CircleCI container are all unusable verbatim.
	assert.Equal(t, len(commandsFromCI(dir)), 0)
}

func TestCommandsFromCIFallsBackWhenUnavailable(t *testing.T) {
	t.Run("no config", func(t *testing.T) {
		assert.Assert(t, commandsFromCI(t.TempDir()) == nil)
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
		assert.Assert(t, commandsFromCI(dir) == nil)
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
		assert.Assert(t, commandsFromCI(dir) == nil)
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
	assert.DeepEqual(t, runs(commandsFromCI(dir)), []string{"test=./scripts/ci.sh"})
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
	got, err := DetectCommands(context.Background(), nil, dir)
	assert.NilError(t, err)

	// Without the CircleCI config this would have been "pnpm test".
	assert.DeepEqual(t, runs(got), []string{"test=bazel test //..."})
}

func TestDetectCommandsFallsBackToFilenames(t *testing.T) {
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644))

	got, err := DetectCommands(context.Background(), nil, dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, runs(got), []string{
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

	got, err := DetectCommands(context.Background(), nil, dir)
	assert.NilError(t, err)
	assert.Equal(t, got[0].Run, "go test ./...")
}
