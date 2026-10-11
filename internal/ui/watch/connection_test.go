package watch

import (
	"errors"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/smeltd"
)

// The rows under a dead daemon are the last thing it said. A header that stayed
// green would let them pass for live ones.
func TestRenderHeader_flagsAnUnreachableRemoteDaemon(t *testing.T) {
	m := sessionModel("", nil).WithConnection(smeltd.Connection{Remote: "10.0.0.5:7777"})
	m.width = 100
	m.daemonErr = errors.New("remote smelt daemon at 10.0.0.5:7777 unreachable: dial tcp: refused")

	header := m.renderHeader(m.styles())
	assert.Assert(t, strings.Contains(header, "remote 10.0.0.5:7777 unreachable"), header)
}
