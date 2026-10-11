package oauth

import (
	"regexp"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/config"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestLoadOrCreateDeviceID_Creates(t *testing.T) {
	t.Setenv(config.EnvXDGStateHome, t.TempDir())

	id, err := LoadOrCreateDeviceID()
	assert.NilError(t, err)
	assert.Assert(t, uuidPattern.MatchString(id), "expected UUID v4, got %q", id)
}

func TestLoadOrCreateDeviceID_Reuses(t *testing.T) {
	t.Setenv(config.EnvXDGStateHome, t.TempDir())

	id1, err := LoadOrCreateDeviceID()
	assert.NilError(t, err)

	id2, err := LoadOrCreateDeviceID()
	assert.NilError(t, err)

	assert.Equal(t, id1, id2)
}
