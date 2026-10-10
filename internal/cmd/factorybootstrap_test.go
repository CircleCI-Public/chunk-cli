package cmd

import (
	"testing"

	"gotest.tools/v3/assert"
)

// Bootstrap sits beside build, not under it. Registered on the build command
// instead it still compiles, and answers at `chunk factory build bootstrap`,
// which is not what the docs or the nothing-to-check suggestion name.
func TestFactoryBootstrapIsRegisteredUnderFactory(t *testing.T) {
	cmd, _, err := newFactoryCmd().Find([]string{"bootstrap"})
	assert.NilError(t, err)
	assert.Equal(t, cmd.Name(), "bootstrap")
}
