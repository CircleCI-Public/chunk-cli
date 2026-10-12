package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/harness/status"
	"github.com/smeltdev/smelt/internal/keyring/keyringtest"
)

func TestHarnessStatusJSONReflectsThePersistedSignal(t *testing.T) {
	isolateConfig(t)
	keyringtest.Install(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	t.Setenv("SMELT_CODEX_AUTH_JSON", "")
	t.Setenv("CODEX_API_KEY", "")
	// Turned off, so the test makes no request and reads no keychain.
	cfgDir := filepath.Join(os.Getenv(config.EnvXDGConfigHome), "smelt")
	assert.NilError(t, os.MkdirAll(cfgDir, 0o700))
	assert.NilError(t, os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"harness":{"claude":{"usage_probe":false}}}`), 0o600))
	t.Chdir(t.TempDir())

	dir, err := status.Dir()
	assert.NilError(t, err)
	resets := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	status.RecordSignal(dir, harness.Claude, harness.RateLimit{Status: "warning", Bucket: "5h", ResetsAt: &resets}, time.Now())

	out, err := runRoot(t, "harness", "status", "--json")
	assert.NilError(t, err, out)
	var rep status.Report
	assert.NilError(t, json.Unmarshal([]byte(out), &rep), out)
	assert.Equal(t, len(rep.Harnesses), 2)
	claude := rep.Harnesses[0]
	assert.Equal(t, claude.Name, harness.Claude)
	assert.Equal(t, len(claude.RateLimits), 1)
	assert.Equal(t, claude.RateLimits[0].Status, "warning")
	assert.Equal(t, claude.RateLimits[0].Source, "run")
	assert.Equal(t, rep.Harnesses[1].Auth, "missing")
}

func TestHarnessStatusProbeIsOffWhenTheUserSaysSo(t *testing.T) {
	isolateConfig(t)
	cfgDir := filepath.Join(os.Getenv(config.EnvXDGConfigHome), "smelt")
	assert.NilError(t, os.MkdirAll(cfgDir, 0o700))
	t.Chdir(t.TempDir())

	assert.Assert(t, harnessStatusDeps(t.Context()).UsageProbe != nil, "on by default")
	assert.NilError(t, os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"harness":{"claude":{"usage_probe":false}}}`), 0o600))
	assert.Assert(t, harnessStatusDeps(t.Context()).UsageProbe == nil)
}
