package validate

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestWithInstallCommand(t *testing.T) {
	test := config.Command{Name: "test", Run: "npm test"}

	t.Run("prepends the lock file's install command", func(t *testing.T) {
		dir := t.TempDir()
		assert.NilError(t, os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{}"), 0o644))

		got, pm := WithInstallCommand(dir, []config.Command{test})
		assert.Assert(t, pm != nil)
		assert.Equal(t, pm.Name, "npm")
		assert.DeepEqual(t, got, []config.Command{{Name: config.CmdInstall, Run: "npm ci"}, test})
	})

	t.Run("keeps an install command detection found", func(t *testing.T) {
		dir := t.TempDir()
		assert.NilError(t, os.WriteFile(filepath.Join(dir, "package-lock.json"), []byte("{}"), 0o644))
		commands := []config.Command{{Name: config.CmdInstall, Run: "make deps"}, test}

		got, pm := WithInstallCommand(dir, commands)
		assert.Assert(t, pm == nil)
		assert.DeepEqual(t, got, commands)
	})

	t.Run("adds nothing without a lock file", func(t *testing.T) {
		got, pm := WithInstallCommand(t.TempDir(), []config.Command{test})
		assert.Assert(t, pm == nil)
		assert.DeepEqual(t, got, []config.Command{test})
	})
}
