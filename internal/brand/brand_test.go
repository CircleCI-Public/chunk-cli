package brand

import (
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

func TestEnv(t *testing.T) {
	assert.Equal(t, Env("DAEMON_DIR"), "SMELT_DAEMON_DIR")
}

func TestLabel(t *testing.T) {
	assert.Equal(t, Label("factory"), "smelt.factory")
	assert.Equal(t, Label("sandbox"), "smelt.sandbox")
}

func TestDerivedNames(t *testing.T) {
	assert.Equal(t, Command, "smelt")
	assert.Equal(t, ProjectDir, ".smelt")
	assert.Equal(t, AppDir, "smelt")
	assert.Equal(t, KeychainPrefix, "smelt:")
	assert.Equal(t, BranchPrefix, "smelt/")
	assert.Equal(t, Dockerfile, "Dockerfile.smelt")
	assert.Equal(t, ImagePrefix, "smelt-")
}

// TestNothingNamesTheOldBrand guards against a derived name drifting back to the
// previous name, which would put it in CircleCI's locations.
func TestNothingNamesTheOldBrand(t *testing.T) {
	// Spelled in two parts so scripts/check-names.sh does not flag this file.
	oldName := "ch" + "unk"
	for _, s := range []string{
		Command, ProjectDir, HomeDir, AppDir, EnvPrefix, KeychainPrefix,
		KeychainFallbackUser, BranchPrefix, Dockerfile, ImagePrefix,
		Env("X"), Label("x"),
	} {
		assert.Assert(t, !strings.Contains(strings.ToLower(s), oldName), s)
	}
}
