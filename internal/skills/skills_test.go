package skills_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/skills"
)

// fakeClaude writes an executable shell script to binDir named "claude" whose
// behaviour is controlled by the responses map: each key is a space-joined
// arg string (e.g. "plugin install circleci --yes --scope user") and each
// value is the stdout to emit (exit 0). Any unrecognised invocation exits 1.
func fakeClaude(t *testing.T, responses map[string]string) string {
	t.Helper()
	binDir := t.TempDir()

	script := "#!/bin/sh\nARGS=\"$*\"\ncase \"$ARGS\" in\n"
	for args, out := range responses {
		script += fmt.Sprintf("  %q) echo %q; exit 0;;\n", args, out)
	}
	script += "  *) echo \"unexpected: $ARGS\" >&2; exit 1;;\nesac\n"

	path := filepath.Join(binDir, "claude")
	assert.NilError(t, os.WriteFile(path, []byte(script), 0o755))
	return binDir
}

func withFakeClaude(t *testing.T, binDir string) {
	t.Helper()
	orig := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+orig)
}

func TestInstallSuccess(t *testing.T) {
	binDir := fakeClaude(t, map[string]string{
		"plugin install circleci --yes --scope project": "Installing plugin \"circleci\"...✔ Successfully installed plugin: circleci@circleci-claude-marketplace (scope: project)",
	})
	withFakeClaude(t, binDir)

	results := skills.Install(skills.ScopeProject)
	assert.Equal(t, len(results), 1)
	r := results[0]
	assert.Equal(t, r.Agent, "claude")
	assert.Assert(t, !r.Skipped)
	assert.Equal(t, len(r.Installed), 1)
	assert.Equal(t, r.Installed[0], "circleci")
	assert.Equal(t, len(r.Updated), 0)
	assert.Equal(t, len(r.Errors), 0)
}

func TestInstallUserScope(t *testing.T) {
	binDir := fakeClaude(t, map[string]string{
		"plugin install circleci --yes --scope user": "Installing plugin \"circleci\"...✔ Successfully installed plugin: circleci@circleci-claude-marketplace (scope: user)",
	})
	withFakeClaude(t, binDir)

	results := skills.Install(skills.ScopeUser)
	assert.Equal(t, len(results), 1)
	r := results[0]
	assert.Assert(t, !r.Skipped)
	assert.Equal(t, len(r.Installed), 1)
}

func TestInstallAlreadyInstalled(t *testing.T) {
	binDir := fakeClaude(t, map[string]string{
		"plugin install circleci --yes --scope project": "Installing plugin \"circleci\"...✔ Plugin \"circleci@circleci-claude-marketplace\" is already installed (scope: project)",
	})
	withFakeClaude(t, binDir)

	results := skills.Install(skills.ScopeProject)
	r := results[0]
	assert.Assert(t, !r.Skipped)
	assert.Equal(t, len(r.Installed), 0)
	assert.Equal(t, len(r.Updated), 1)
	assert.Equal(t, r.Updated[0], "circleci")
}

func TestInstallSkipsWhenCLINotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // isolated PATH with no claude

	results := skills.Install(skills.ScopeProject)
	assert.Equal(t, len(results), 1)
	assert.Assert(t, results[0].Skipped)
	assert.Equal(t, len(results[0].Installed), 0)
}

func TestInstallSurfacesErrors(t *testing.T) {
	// Fake claude exits non-zero for install.
	binDir := t.TempDir()
	script := "#!/bin/sh\necho 'install failed' >&2; exit 1\n"
	assert.NilError(t, os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755))
	withFakeClaude(t, binDir)

	results := skills.Install(skills.ScopeProject)
	r := results[0]
	assert.Assert(t, !r.Skipped)
	assert.Equal(t, len(r.Errors), 1)
	assert.Equal(t, len(r.Installed), 0)
}

func TestInstallByNameDelegatesToInstall(t *testing.T) {
	binDir := fakeClaude(t, map[string]string{
		"plugin install circleci --yes --scope project": "✔ Successfully installed plugin: circleci@circleci-claude-marketplace (scope: project)",
	})
	withFakeClaude(t, binDir)

	// InstallByName is a compatibility shim — ignores skill names, just installs the plugin.
	results := skills.InstallByName(skills.ScopeProject, "/some/dir", "chunk-sidecar", "chunk-sidecar-setup")
	assert.Equal(t, len(results), 1)
	assert.Equal(t, len(results[0].Installed), 1)
}

func TestStatusCurrent(t *testing.T) {
	pluginList := []map[string]any{
		{"id": "circleci@circleci-claude-marketplace", "scope": "project", "enabled": true},
	}
	listJSON, _ := json.Marshal(pluginList)
	binDir := fakeClaude(t, map[string]string{
		"plugin list --json": string(listJSON),
	})
	withFakeClaude(t, binDir)

	statuses := skills.Status(skills.ScopeProject, "")
	assert.Equal(t, len(statuses), 1)
	s := statuses[0]
	assert.Equal(t, s.Agent, "claude")
	assert.Assert(t, s.Available)
	assert.Equal(t, len(s.Skills), 1)
	assert.Equal(t, s.Skills[0].State, skills.StateCurrent)
}

func TestStatusMissingWhenNotInstalled(t *testing.T) {
	binDir := fakeClaude(t, map[string]string{
		"plugin list --json": "[]",
	})
	withFakeClaude(t, binDir)

	statuses := skills.Status(skills.ScopeProject, "")
	s := statuses[0]
	assert.Assert(t, s.Available)
	assert.Equal(t, s.Skills[0].State, skills.StateMissing)
}

func TestStatusSkipsWhenCLINotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	statuses := skills.Status(skills.ScopeProject, "")
	assert.Equal(t, len(statuses), 1)
	assert.Assert(t, !statuses[0].Available)
	assert.Equal(t, statuses[0].Skills[0].State, skills.StateMissing)
}

func TestStatusScopeFiltering(t *testing.T) {
	// Plugin installed at user scope — querying project scope should show missing.
	pluginList := []map[string]any{
		{"id": "circleci@circleci-claude-marketplace", "scope": "user", "enabled": true},
	}
	listJSON, _ := json.Marshal(pluginList)
	binDir := fakeClaude(t, map[string]string{
		"plugin list --json": string(listJSON),
	})
	withFakeClaude(t, binDir)

	statuses := skills.Status(skills.ScopeProject, "")
	assert.Equal(t, statuses[0].Skills[0].State, skills.StateMissing)

	// Same data, querying user scope should show current.
	statuses = skills.Status(skills.ScopeUser, "")
	assert.Equal(t, statuses[0].Skills[0].State, skills.StateCurrent)
}

func TestAllSkillsHaveNameAndDescription(t *testing.T) {
	for _, s := range skills.All {
		assert.Assert(t, s.Name != "", "skill should have a name")
		assert.Assert(t, s.Description != "", "skill %s should have a description", s.Name)
	}
}
