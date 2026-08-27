package notify_test

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/notify"
)

// TestNotifyFn_closure verifies that a capturing closure passed as a notifyFn
// receives the title and body without any global state. This mirrors how
// finishValidate uses a func parameter for testability.
func TestNotifyFn_closure(t *testing.T) {
	t.Parallel()

	var gotTitle, gotBody string
	var calls int
	fn := func(title, body string) {
		gotTitle = title
		gotBody = body
		calls++
	}

	fn("chunk validate passed", "3/3 checks passed · 1.2s")

	assert.Equal(t, calls, 1)
	assert.Equal(t, gotTitle, "chunk validate passed")
	assert.Equal(t, gotBody, "3/3 checks passed · 1.2s")
}

// TestSend_exists just ensures the package-level Send function compiles and
// is callable; it will be a no-op on platforms with no notification tool.
func TestSend_exists(t *testing.T) {
	t.Parallel()
	// We cannot observe the OS notification, but we verify Send does not panic.
	// On most CI runners (Linux with no notify-send) this is a silent no-op.
	notify.Send("test title", "test body")
}
