package notify_test

import (
	"testing"

	"github.com/CircleCI-Public/chunk-cli/internal/notify"
	"gotest.tools/v3/assert"
)

type spySender struct {
	title, body string
	calls       int
}

func (s *spySender) Send(title, body string) {
	s.title = title
	s.body = body
	s.calls++
}

func TestSend_routesTitleAndBody(t *testing.T) {
	spy := &spySender{}
	orig := notify.DefaultSender
	notify.DefaultSender = spy
	t.Cleanup(func() { notify.DefaultSender = orig })

	notify.Send("chunk validate", "Passed: 3/3  1.2s")

	assert.Equal(t, spy.calls, 1)
	assert.Equal(t, spy.title, "chunk validate")
	assert.Equal(t, spy.body, "Passed: 3/3  1.2s")
}

func TestSend_failure(t *testing.T) {
	spy := &spySender{}
	orig := notify.DefaultSender
	notify.DefaultSender = spy
	t.Cleanup(func() { notify.DefaultSender = orig })

	notify.Send("chunk validate", "Failed: 1/3  2.1s")

	assert.Equal(t, spy.calls, 1)
	assert.Equal(t, spy.title, "chunk validate")
	assert.Equal(t, spy.body, "Failed: 1/3  2.1s")
}
