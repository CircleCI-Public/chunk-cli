package watch

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

// The rows under a dead daemon are the last thing it said. A header that stayed
// green would let them pass for live ones.
func TestRenderHeader_flagsAnUnreachableRemoteDaemon(t *testing.T) {
	m := sessModel().WithConnection(chunkd.Connection{Remote: "10.0.0.5:7777"})
	m.width = 100
	m.daemonErr = errors.New("remote chunk daemon at 10.0.0.5:7777 unreachable: dial tcp: refused")

	header := m.renderHeader(m.styles())
	assert.Assert(t, strings.Contains(header, "remote 10.0.0.5:7777 unreachable"), header)
}

// The header's clock only moved when the user poked the TUI (scrolling,
// pressing a key), which made it look live when it wasn't. It's gone now.
func TestRenderHeader_hasNoClock(t *testing.T) {
	m := sessModel()
	m.width = 100

	header := m.renderHeader(m.styles())

	assert.Assert(t, !regexp.MustCompile(`\d{2}:\d{2}:\d{2}`).MatchString(header), header)
}
