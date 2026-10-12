package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/envspec"
)

func writeProjectConfig(t *testing.T, workDir, data string) {
	t.Helper()
	smeltDir := filepath.Join(workDir, ".smelt")
	assert.NilError(t, os.MkdirAll(smeltDir, 0o700))
	assert.NilError(t, os.WriteFile(filepath.Join(smeltDir, "config.json"), []byte(data), 0o600))
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

func TestCommandPlacementDefaultsRemote(t *testing.T) {
	assert.Assert(t, Command{}.RunsRemotely())
	assert.Assert(t, !Command{}.RunsLocally())
	assert.Assert(t, Command{Remote: true}.RunsRemotely())
	assert.Assert(t, Command{Local: true}.RunsLocally())
}

func TestLoadProjectConfigRejectsConflictingPlacement(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, `{"commands":[{"name":"test","run":"task test","local":true,"remote":true}]}`)

	_, err := LoadProjectConfig(dir)
	assert.ErrorContains(t, err, `command "test" cannot be both local and remote`)
}

func TestSaveProjectConfigRejectsConflictingPlacement(t *testing.T) {
	err := SaveProjectConfig(t.TempDir(), &ProjectConfig{Commands: []Command{{Name: "test", Local: true, Remote: true}}})
	assert.ErrorContains(t, err, `command "test" cannot be both local and remote`)
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
			{Name: CmdInstall, Run: "npm ci"},
			{Name: "test", Run: "npm test", Role: RoleGate},
			{Name: "format", Run: "npm run format", Role: RoleAutofix},
			{Name: "lint", Run: "npm run lint"},
			{Name: "local-gate", Run: "npm test", Role: RoleGate, Local: true},
			{Name: "test-changed", Run: "npm test --changed", Role: RoleGate, Remote: true},
		},
	}

	changed := cfg.MarkRemoteCommandsForSidecarSetup()
	assert.Assert(t, changed)
	assert.Assert(t, cfg.FindCommand(CmdInstall).Remote)
	assert.Assert(t, cfg.FindCommand("test").Remote)
	assert.Assert(t, !cfg.FindCommand("format").Remote)
	assert.Assert(t, !cfg.FindCommand("lint").Remote)
	assert.Assert(t, !cfg.FindCommand("local-gate").Remote)
	assert.Assert(t, cfg.FindCommand("local-gate").Local)
	assert.Assert(t, cfg.FindCommand("test-changed").Remote)

	assert.Assert(t, !cfg.MarkRemoteCommandsForSidecarSetup())
}

func TestMarkCommandRemote(t *testing.T) {
	newCfg := func() *ProjectConfig {
		return &ProjectConfig{
			Commands: []Command{
				{Name: CmdInstall, Run: "npm ci"},
				{Name: "test", Run: "npm test", Role: RoleGate},
				{Name: "format", Run: "npm run format", Role: RoleAutofix},
				{Name: "lint", Run: "npm run lint", Remote: true},
			},
		}
	}

	t.Run("named command", func(t *testing.T) {
		cfg := newCfg()
		cfg.FindCommand("test").Local = true
		changed, err := cfg.MarkCommandRemote("test")
		assert.NilError(t, err)
		assert.DeepEqual(t, changed, []string{"test"})
		assert.Assert(t, cfg.FindCommand("test").Remote)
		assert.Assert(t, !cfg.FindCommand("test").Local)
		// Untargeted commands keep whatever they had.
		assert.Assert(t, !cfg.FindCommand(CmdInstall).Remote)
	})

	// A named autofix command is marked: the caller asked for that one by name.
	t.Run("named autofix command", func(t *testing.T) {
		cfg := newCfg()
		changed, err := cfg.MarkCommandRemote("format")
		assert.NilError(t, err)
		assert.DeepEqual(t, changed, []string{"format"})
		assert.Assert(t, cfg.FindCommand("format").Remote)
	})

	// The unnamed sweep skips autofix commands. A formatter running on the sidecar
	// rewrites files there and the edits never come back to the working tree.
	t.Run("empty name skips autofix", func(t *testing.T) {
		cfg := newCfg()
		changed, err := cfg.MarkCommandRemote("")
		assert.NilError(t, err)
		// "lint" is already remote, so it is not reported as changed.
		assert.DeepEqual(t, changed, []string{CmdInstall, "test"})
		assert.Assert(t, !cfg.FindCommand("format").Remote)
		assert.Assert(t, cfg.FindCommand("lint").Remote)
	})

	t.Run("already remote reports no change", func(t *testing.T) {
		cfg := newCfg()
		changed, err := cfg.MarkCommandRemote("lint")
		assert.NilError(t, err)
		assert.Equal(t, len(changed), 0)
	})

	t.Run("unknown name", func(t *testing.T) {
		cfg := newCfg()
		_, err := cfg.MarkCommandRemote("nope")
		assert.Assert(t, errors.Is(err, ErrNoSuchCommand))
		// Nothing is written when the name misses.
		assert.Assert(t, !cfg.FindCommand("test").Remote)
	})

	t.Run("nil config", func(t *testing.T) {
		var cfg *ProjectConfig
		_, err := cfg.MarkCommandRemote("test")
		assert.Assert(t, errors.Is(err, ErrNoSuchCommand))
	})
}

// A misspelt rule fails in the direction nobody checks: the project believes it
// is waiting for its migrations and nothing ever says otherwise. So each of
// these is reported rather than ignored.
func TestLoadProjectConfigRejectsUnusablePathRules(t *testing.T) {
	for name, tc := range map[string]struct{ json, want string }{
		"a path":       {`{"asyncValidateInert":["docs/api.md"]}`, "looks like a path"},
		"a glob":       {`{"asyncValidateInert":["*.md"]}`, "looks like a glob"},
		"empty entry":  {`{"asyncValidateBlocking":["  "]}`, "has an empty entry"},
		"a bare dot":   {`{"asyncValidateBlocking":["."]}`, "not an extension or a file name"},
		"bare ext":     {`{"asyncValidateInert":["sql"]}`, `looks like an extension missing its dot; write ".sql"`},
		"bare ext cap": {`{"asyncValidateBlocking":["SQL"]}`, "missing its dot"},
		"contradicted": {`{"asyncValidateInert":[".sql"],"asyncValidateBlocking":[".sql"]}`, "in both"},
		// Whitespace is invisible in a config file, so it must not be the thing
		// that carries an entry past a check it would otherwise fail.
		"padded bare ext": {`{"asyncValidateInert":["sql  "]}`, `write ".sql"`},
		"padded path":     {`{"asyncValidateInert":[" docs/api.md"]}`, "looks like a path"},
		"padded contra":   {`{"asyncValidateInert":[".sql"],"asyncValidateBlocking":[" .sql "]}`, "in both"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeProjectConfig(t, dir, tc.json)
			_, err := LoadProjectConfig(dir)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

func TestLoadProjectConfigAcceptsPathRules(t *testing.T) {
	dir := t.TempDir()
	// mvnw has the shape of a bare extension and is a real file name, so it is
	// kept: the check names extensions rather than guessing at shapes.
	writeProjectConfig(t, dir, `{"asyncValidateInert":[".sql","NOTES"],"asyncValidateBlocking":[".md","Makefile","mvnw"]}`)

	cfg, err := LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, cfg.AsyncValidateInert, []string{".sql", "NOTES"})
	assert.DeepEqual(t, cfg.AsyncValidateBlocking, []string{".md", "Makefile", "mvnw"})
}

// A padded entry is stored trimmed, so the rules the daemon matches against are
// the ones the project meant rather than names no file has.
func TestLoadProjectConfigTrimsPathRules(t *testing.T) {
	dir := t.TempDir()
	writeProjectConfig(t, dir, `{"asyncValidateInert":[" .sql","NOTES  "],"asyncValidateBlocking":["\t.md "]}`)

	cfg, err := LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, cfg.AsyncValidateInert, []string{".sql", "NOTES"})
	assert.DeepEqual(t, cfg.AsyncValidateBlocking, []string{".md"})
}

// The trim reaches the saved file too: a config assembled in memory is written
// out normalised, not stored padded for the next load to clean up.
func TestSaveProjectConfigTrimsPathRules(t *testing.T) {
	dir := t.TempDir()
	err := SaveProjectConfig(dir, &ProjectConfig{AsyncValidateInert: []string{" .sql "}})
	assert.NilError(t, err)

	cfg, err := LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, cfg.AsyncValidateInert, []string{".sql"})
}

func TestFactoryConfigValidation(t *testing.T) {
	for _, backend := range []string{"", "circleci", "docker"} {
		cfg := &ProjectConfig{Factory: &FactoryConfig{Backend: backend, MaxAttempts: 2}}
		assert.NilError(t, cfg.Validate(), backend)
	}
	err := (&ProjectConfig{Factory: &FactoryConfig{Backend: "podman"}}).Validate()
	assert.ErrorContains(t, err, "factory.backend")
	err = (&ProjectConfig{Factory: &FactoryConfig{MaxAttempts: -1}}).Validate()
	assert.ErrorContains(t, err, "maxAttempts")
	err = (&ProjectConfig{Factory: &FactoryConfig{MaxConcurrent: -1}}).Validate()
	assert.ErrorContains(t, err, "maxConcurrent")
}

func TestFactoryConfigConcurrentRuns(t *testing.T) {
	var unset *FactoryConfig
	assert.Equal(t, unset.ConcurrentRuns(), 1)
	assert.Equal(t, (&FactoryConfig{}).ConcurrentRuns(), 1)
	assert.Equal(t, (&FactoryConfig{MaxConcurrent: 3}).ConcurrentRuns(), 3)
}

func TestFactoryConfigAuthor(t *testing.T) {
	var unset *FactoryConfig
	author, err := unset.CommitAuthor()
	assert.NilError(t, err)
	assert.Equal(t, author, CommitAuthor{})

	author, err = (&FactoryConfig{Author: "Ada Lovelace <ada@example.com>"}).CommitAuthor()
	assert.NilError(t, err)
	assert.Equal(t, author, CommitAuthor{Name: "Ada Lovelace", Email: "ada@example.com"})

	for _, bad := range []string{"ada@example.com", "Ada", "Ada <not an email>"} {
		err := (&ProjectConfig{Factory: &FactoryConfig{Author: bad}}).Validate()
		assert.ErrorContains(t, err, "factory.author", bad)
	}
}

func TestFactoryConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := &FactoryConfig{Backend: "docker", Image: "smelt-sandbox:local", MaxAttempts: 5, Log: true}
	assert.NilError(t, SaveProjectConfig(dir, &ProjectConfig{Factory: want}))
	got, err := LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.DeepEqual(t, got.Factory, want)

	writeProjectConfig(t, dir, `{"factory":{"backend":"nope"}}`)
	_, err = LoadProjectConfig(dir)
	assert.ErrorContains(t, err, "factory.backend")
}

func TestEffectiveTimeout(t *testing.T) {
	assert.Equal(t, Command{Timeout: 42}.EffectiveTimeout(), 42)
	assert.Equal(t, Command{}.EffectiveTimeout(), DefaultCommandTimeout)
}

func TestKeepLongerTimeouts(t *testing.T) {
	detected := []Command{
		{Name: "test", Run: "task ci:test", Timeout: DefaultTestTimeout},
		{Name: "lint", Run: "task ci:lint", Timeout: DefaultLintTimeout},
		{Name: "format", Run: "task fmt", Timeout: DefaultFormatTimeout},
		{Name: CmdInstall, Run: "task mod-download"},
	}
	previous := []Command{
		{Name: "lint", Run: "task lint", Timeout: 300}, // raised by hand: kept
		{Name: "format", Run: "task fmt", Timeout: 10}, // shorter: detection wins
		{Name: "gone", Run: "true", Timeout: 999},      // no longer detected: ignored
	}
	got := KeepLongerTimeouts(detected, previous)

	byName := map[string]Command{}
	for _, c := range got {
		byName[c.Name] = c
	}
	assert.Equal(t, len(got), len(detected))
	assert.Equal(t, byName["lint"].Timeout, 300)
	assert.Equal(t, byName["lint"].Run, "task ci:lint", "only the timeout is carried over")
	assert.Equal(t, byName["format"].Timeout, DefaultFormatTimeout)
	assert.Equal(t, byName["test"].Timeout, DefaultTestTimeout)
	assert.Equal(t, byName[CmdInstall].Timeout, 0)
	_, gone := byName["gone"]
	assert.Assert(t, !gone)
	assert.Equal(t, detected[1].Timeout, DefaultLintTimeout, "the input is not modified")
}

func TestValidateFactoryDockerfile(t *testing.T) {
	for _, tt := range []struct {
		path    string
		wantErr bool
	}{
		{"", false}, {"Dockerfile.smelt", false}, {"docker/Dockerfile.smelt", false},
		{"/etc/Dockerfile", true}, {"../Dockerfile", true}, {"a/../../Dockerfile", true},
	} {
		cfg := &ProjectConfig{Factory: &FactoryConfig{Dockerfile: tt.path}}
		err := cfg.Validate()
		if tt.wantErr {
			assert.ErrorContains(t, err, "factory.dockerfile")
		} else {
			assert.NilError(t, err)
		}
	}
}

func TestInstallCommand(t *testing.T) {
	var nilCfg *ProjectConfig
	assert.Equal(t, nilCfg.InstallCommand(), "")

	cfg := &ProjectConfig{Commands: []Command{{Name: "test", Run: "go test"}, {Name: CmdInstall, Run: "pnpm install"}}}
	assert.Equal(t, cfg.InstallCommand(), "pnpm install")

	cfg.Environment = &envspec.Environment{Setup: []envspec.Step{{Name: "install", Command: "pnpm install --frozen-lockfile"}}}
	assert.Equal(t, cfg.InstallCommand(), "pnpm install --frozen-lockfile")

	assert.Equal(t, (&ProjectConfig{}).InstallCommand(), "")
}
