package closer

import (
	"errors"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestErrorHandler(t *testing.T) {
	t.Run("close error surfaces when in is nil", func(t *testing.T) {
		sentinel := errors.New("close failed")
		called := false

		var err error
		ErrorHandler(closerFunc(func() error {
			called = true
			return sentinel
		}), &err)

		assert.Check(t, called)
		assert.Check(t, cmp.ErrorIs(err, sentinel))
	})

	t.Run("close error ignored when in already set", func(t *testing.T) {
		original := errors.New("original")
		closeErr := errors.New("close failed")

		err := original
		ErrorHandler(closerFunc(func() error {
			return closeErr
		}), &err)

		assert.Check(t, cmp.ErrorIs(err, original))
	})

	t.Run("no error when close succeeds", func(t *testing.T) {
		var err error
		ErrorHandler(closerFunc(func() error {
			return nil
		}), &err)

		assert.NilError(t, err)
	})
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }
