package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
)

func TestSkillsInstall(t *testing.T) {
	env := testenv.NewTestEnv(t)

	claudeDir := filepath.Join(env.HomeDir, ".claude")
	err := os.MkdirAll(claudeDir, 0o755)
	assert.NilError(t, err)

	result := binary.RunCLI(t, []string{"skills", "install"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	for _, name := range []string{"chunk-review", "chunk-testing-gaps", "debug-ci-failures"} {
		skillFile := filepath.Join(claudeDir, "skills", name, "SKILL.md")
		info, err := os.Stat(skillFile)
		assert.NilError(t, err, "expected skill %s to exist", name)
		assert.Assert(t, info.Size() > 0, "expected skill %s to be non-empty", name)
	}
}

func TestSkillsList(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"skills", "list"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "chunk-review"),
		"expected 'chunk-review' in output, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "chunk-testing-gaps"),
		"expected 'chunk-testing-gaps' in output, got: %s", combined)
}
