package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/envspec"
)

func writeProjectConfig(t *testing.T, workDir, data string) {
	t.Helper()
	chunkDir := filepath.Join(workDir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o700))
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(data), 0o600))
}

// Configs written before the test command stopped being persisted still carry a
// "test" step; loading must drop it so it is never run as a setup step.
func TestLoadProjectConfigDropsTestStep(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, `{
	  "environment": {
	    "stack": "go",
	    "setup": [
	      {"name": "install", "command": "go mod download"},
	      {"name": "test", "command": "go test -p 1 ./..."}
	    ],
	    "image": "cimg/go",
	    "image_version": "1.26.2"
	  }
	}`)

	cfg, err := LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.Equal(t, len(cfg.Environment.Setup), 1)
	assert.Equal(t, cfg.Environment.Setup[0].Command, "go mod download")
	assert.Equal(t, cfg.Environment.SetupStep(envspec.StepTest), "")

	// And it is not written back out on the next save.
	assert.NilError(t, SaveProjectConfig(dir, cfg))
	reloaded, err := LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.Equal(t, len(reloaded.Environment.Setup), 1)
	assert.Equal(t, reloaded.Environment.ImageVersion, "1.26.2")
}

func TestLoadProjectConfigWithoutEnvironment(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, `{"commands":[{"name":"test","run":"task test"}]}`)

	cfg, err := LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.Assert(t, cfg.Environment == nil)
	assert.Equal(t, len(cfg.Commands), 1)
}

func readProjectConfig(t *testing.T, workDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(workDir, ".chunk", "config.json"))
	assert.NilError(t, err)
	return string(data)
}

// The reported case: keys hand-added to individual commands, plus an unknown key
// at the top level, must survive a write that only touches something else.
func TestSaveProjectConfigPreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, `{
  "commands": [
    {"name": "format", "run": "task fmt", "fileExt": "go", "always": true},
    {"name": "test", "run": "task test && task lint", "fileExt": "go", "limit": 5}
  ],
  "orgID": "org-1",
  "myTool": {"nested": [1, 2]}
}`)

	cfg, err := LoadProjectConfig(dir)
	assert.NilError(t, err)
	cfg.Validation = &ValidationConfig{SidecarImage: "snap-123"}
	assert.NilError(t, SaveProjectConfig(dir, cfg))

	got := readProjectConfig(t, dir)
	for _, want := range []string{
		`"fileExt": "go"`,
		`"always": true`,
		`"limit": 5`,
		`"myTool"`,
		`"nested"`,
		`"sidecarImage": "snap-123"`,
		`"orgID": "org-1"`,
		// Shell operators stay readable rather than being escaped to &.
		`"run": "task test && task lint"`,
	} {
		assert.Assert(t, strings.Contains(got, want), "missing %s in:\n%s", want, got)
	}

	// And the preserved keys are still there on the next round trip.
	reloaded, err := LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.Equal(t, len(reloaded.Commands), 2)
	assert.NilError(t, SaveProjectConfig(dir, reloaded))
	assert.Equal(t, readProjectConfig(t, dir), got)
}

// Preservation must not undo the deliberate drop of the test setup step.
func TestSaveProjectConfigDoesNotResurrectTestStep(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, `{
  "environment": {
    "stack": "go",
    "setup": [
      {"name": "install", "command": "go mod download", "cache": true},
      {"name": "test", "command": "go test ./..."}
    ],
    "image": "cimg/go",
    "image_version": "1.26.2"
  }
}`)

	cfg, err := LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.NilError(t, SaveProjectConfig(dir, cfg))

	got := readProjectConfig(t, dir)
	assert.Assert(t, strings.Contains(got, `"cache": true`), "unknown key on the kept step should survive:\n%s", got)
	assert.Assert(t, !strings.Contains(got, "go test ./..."), "test step should stay dropped:\n%s", got)
}

// A modeled value the caller clears must be removed, not restored from disk.
func TestSaveProjectConfigDeletesClearedValue(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, `{"orgID": "org-1", "keepMe": "yes"}`)

	cfg, err := LoadProjectConfig(dir)
	assert.NilError(t, err)
	cfg.OrgID = ""
	assert.NilError(t, SaveProjectConfig(dir, cfg))

	got := readProjectConfig(t, dir)
	assert.Assert(t, !strings.Contains(got, "org-1"), "cleared orgID should be gone:\n%s", got)
	assert.Assert(t, strings.Contains(got, `"keepMe": "yes"`), "unknown key should survive:\n%s", got)
}

// With nothing to preserve the output must be byte-identical to a plain marshal,
// so no user sees a reformatting diff.
func TestSaveProjectConfigFormatUnchanged(t *testing.T) {
	dir := t.TempDir()
	cfg := &ProjectConfig{
		Commands: []Command{{Name: "test", Run: "go test ./...", Timeout: 30}},
		OrgID:    "org-1",
	}
	assert.NilError(t, SaveProjectConfig(dir, cfg))
	plain, err := marshalIndent(cfg)
	assert.NilError(t, err)
	assert.Equal(t, readProjectConfig(t, dir), string(plain)+"\n")

	// Saving again over the file it just wrote changes nothing.
	assert.NilError(t, SaveProjectConfig(dir, cfg))
	assert.Equal(t, readProjectConfig(t, dir), string(plain)+"\n")
}

func TestSaveProjectConfigUnparseableExistingFile(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, `{"orgID": `)

	// SaveProjectConfig itself falls back to a plain marshal, which is what lets
	// `chunk init --force` replace a config nobody can fix by hand. Refusing is
	// the caller's job, via LoadProjectConfigForUpdate.
	assert.NilError(t, SaveProjectConfig(dir, &ProjectConfig{OrgID: "org-1"}))
	assert.Equal(t, readProjectConfig(t, dir), "{\n  \"orgID\": \"org-1\"\n}\n")
}

func TestLoadProjectConfigDistinguishesMissingFromMalformed(t *testing.T) {
	missing := t.TempDir()
	_, err := LoadProjectConfig(missing)
	assert.Assert(t, errors.Is(err, fs.ErrNotExist))
	assert.Assert(t, !errors.Is(err, ErrParseProjectConfig))

	malformed := t.TempDir()
	writeProjectConfig(t, malformed, `{"commands": [`)
	_, err = LoadProjectConfig(malformed)
	assert.Assert(t, errors.Is(err, ErrParseProjectConfig))
}

func TestLoadProjectConfigForUpdate(t *testing.T) {
	missing := t.TempDir()
	cfg, err := LoadProjectConfigForUpdate(missing)
	assert.NilError(t, err)
	assert.Assert(t, !cfg.HasCommands())

	malformed := t.TempDir()
	writeProjectConfig(t, malformed, `{"commands": [`)
	_, err = LoadProjectConfigForUpdate(malformed)
	assert.Assert(t, errors.Is(err, ErrParseProjectConfig))
}

// A write path must not replace a config it could not read.
func TestSaveCommandRefusesMalformedConfig(t *testing.T) {
	dir := t.TempDir()
	const broken = `{"commands": [`
	writeProjectConfig(t, dir, broken)

	err := SaveCommand(dir, "test", "go test ./...")
	assert.Assert(t, errors.Is(err, ErrParseProjectConfig))
	assert.Equal(t, readProjectConfig(t, dir), broken)
}

func TestUnknownProjectConfigKeys(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, `{
  "commands": [{"name": "test", "run": "task test", "fileExt": "go", "limit": 5}],
  "myTool": 1
}`)
	assert.DeepEqual(t, UnknownProjectConfigKeys(dir), []string{
		"commands[].fileExt", "commands[].limit", "myTool",
	})

	assert.Equal(t, len(UnknownProjectConfigKeys(t.TempDir())), 0)
}

func TestHasSidecarImage(t *testing.T) {
	assert.Assert(t, !(*ProjectConfig)(nil).HasSidecarImage())

	cfg := &ProjectConfig{}
	assert.Assert(t, !cfg.HasSidecarImage())

	cfg.Validation = &ValidationConfig{}
	assert.Assert(t, !cfg.HasSidecarImage())

	cfg.Validation.SidecarImage = "snap-123"
	assert.Assert(t, cfg.HasSidecarImage())
}

func TestMarkRemoteCommandsForSidecarSetup(t *testing.T) {
	cfg := &ProjectConfig{
		Commands: []Command{
			{Name: "install", Run: "npm ci"},
			{Name: "test", Run: "npm test", Role: RoleGate},
			{Name: "format", Run: "npm run format", Role: RoleAutofix},
			{Name: "lint", Run: "npm run lint"},
			{Name: "test-changed", Run: "npm test --changed", Role: RoleGate, Remote: true},
		},
	}

	changed := cfg.MarkRemoteCommandsForSidecarSetup()
	assert.Assert(t, changed)
	assert.Assert(t, cfg.FindCommand("install").Remote)
	assert.Assert(t, cfg.FindCommand("test").Remote)
	assert.Assert(t, !cfg.FindCommand("format").Remote)
	assert.Assert(t, !cfg.FindCommand("lint").Remote)
	assert.Assert(t, cfg.FindCommand("test-changed").Remote)

	assert.Assert(t, !cfg.MarkRemoteCommandsForSidecarSetup())
}
