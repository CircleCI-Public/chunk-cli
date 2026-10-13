package cmd

import (
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/factory"
)

// Bootstrap sits beside build, not under it. Registered on the build command
// instead it still compiles, and answers at `chunk factory build bootstrap`,
// which is not what the docs or the nothing-to-check suggestion name.
func TestFactoryBootstrapIsRegisteredUnderFactory(t *testing.T) {
	cmd, _, err := newFactoryCmd().Find([]string{"bootstrap"})
	assert.NilError(t, err)
	assert.Equal(t, cmd.Name(), "bootstrap")
}

// An explicit --template is a name to look up, even when it is empty. Gating
// on the value instead of the flag sends `--template ""` down the sidecar path,
// where it boots an environment and runs Claude.
func TestFactoryBootstrapEmptyTemplateIsUnknown(t *testing.T) {
	t.Chdir(t.TempDir())
	cmd := newFactoryBootstrapCmd()
	cmd.SetArgs([]string{"--template", "", "--output", filepath.Join(t.TempDir(), "reviews")})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true

	err := cmd.Execute()

	assert.ErrorIs(t, err, factory.ErrUnknownTemplate)
}
