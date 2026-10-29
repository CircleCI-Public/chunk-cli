package watch

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

// A header that stayed green over a dead daemon would hide that plans cannot
// be built.
func TestRenderHeader_flagsAnUnreachableDaemon(t *testing.T) {
	m := New().WithConnection(chunkd.Connection{})
	m.width = 100
	m.daemonErr = errors.New("connect to chunk daemon: no such file")

	header := ansi.Strip(m.renderHeader(m.styles()))
	assert.Check(t, cmp.Contains(header, "local unreachable"))
}

// A daemon that answers again clears the header.
func TestDaemonCheckClearsTheError(t *testing.T) {
	m := New().WithConnection(chunkd.Connection{})
	m.width = 100
	m.checkDaemon = func() error { return nil }
	m.daemonErr = errors.New("gone")

	next, tick := m.Update(m.pingDaemon())
	m = next.(Model)
	assert.Check(t, m.daemonErr == nil)
	assert.Check(t, tick != nil, "the next check is scheduled")
	assert.Check(t, cmp.Contains(ansi.Strip(m.renderHeader(m.styles())), "✓ local"))
}

// The header shows no clock.
func TestRenderHeader_hasNoClock(t *testing.T) {
	m := New()
	m.width = 100

	header := m.renderHeader(m.styles())

	assert.Assert(t, !regexp.MustCompile(`\d{2}:\d{2}:\d{2}`).MatchString(header), header)
}

func TestRenderHeader_namesAnUpdate(t *testing.T) {
	m := New().WithConnection(chunkd.Connection{})
	m.width = 100
	next, _ := m.Update(updateCheckMsg{latest: "v9.9.9", upgradeCmd: "chunk upgrade"})
	m = next.(Model)

	header := ansi.Strip(m.renderHeader(m.styles()))
	assert.Check(t, cmp.Contains(header, "↑ v9.9.9 chunk upgrade"))
	assert.Check(t, ansi.StringWidth(strings.TrimSuffix(header, "\n")) <= 100, header)
}
