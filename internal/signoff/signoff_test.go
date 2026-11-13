package signoff_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/signoff"
)

// recorder is a Post that keeps the runs it is given, and an exec function
// that keeps the commands it was asked to run and answers each by name.
type recorder struct {
	mu     sync.Mutex
	posted []circleci.CheckRun
	ran    []string
	// exec answers a command by its name; nil means exit 0.
	exec func(name string) (exitCode int, err error)
}

func (r *recorder) post(_ context.Context, run circleci.CheckRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.posted = append(r.posted, run)
	return nil
}

// execFn runs a script of the form "cd 'dir' && <name>".
func (r *recorder) execFn(_ context.Context, script string) (string, string, int, error) {
	_, name, _ := strings.Cut(script, " && ")
	r.mu.Lock()
	r.ran = append(r.ran, name)
	answer := r.exec
	r.mu.Unlock()
	if answer == nil {
		return "", "", 0, nil
	}
	exitCode, err := answer(name)
	return "", "", exitCode, err
}

func (r *recorder) names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var names []string
	for _, run := range r.posted {
		names = append(names, run.Name)
	}
	return names
}

func (r *recorder) options(names ...string) signoff.Options {
	var commands []config.Command
	for _, n := range names {
		commands = append(commands, config.Command{Name: n, Run: n})
	}
	return signoff.Options{
		Commands: commands,
		Key:      "github.com/o/r@tree:abc",
		Exec:     r.execFn,
		Dir:      "/work",
		Post:     r.post,
		Streams:  iostream.Streams{Out: &strings.Builder{}, Err: &strings.Builder{}},
	}
}

func TestRun(t *testing.T) {
	t.Parallel()

	t.Run("exec error posts nothing for that command", func(t *testing.T) {
		t.Parallel()
		r := &recorder{exec: func(name string) (int, error) {
			if name == "b" {
				return 0, errors.New("connection reset")
			}
			return 0, nil
		}}

		res, err := signoff.Run(context.Background(), r.options("a", "b", "c"))

		assert.Check(t, cmp.DeepEqual(r.names(), []string{"a", "c"})) // b's exec errored
		assert.Check(t, cmp.DeepEqual(r.ran, []string{"a", "b", "c"}))
		assert.Check(t, err != nil)
		assert.Check(t, cmp.ErrorContains(err, "connection reset"))
		assert.Check(t, cmp.Equal(res.Posted, 2))
		assert.Check(t, cmp.Equal(res.Passed, 2))
	})

	t.Run("cancelled context posts nothing for the interrupted command", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		r := &recorder{}
		// Ctrl-C arrives while b runs; b then reports that it was cut short, as a
		// command killed by the stop would.
		r.exec = func(name string) (int, error) {
			if name == "b" {
				cancel()
				return 130, nil
			}
			return 0, nil
		}

		_, err := signoff.Run(ctx, r.options("a", "b", "c"))

		assert.Check(t, cmp.DeepEqual(r.names(), []string{"a"}))
		assert.Check(t, cmp.DeepEqual(r.ran, []string{"a", "b"}))
		assert.Check(t, cmp.ErrorIs(err, context.Canceled))
	})

	t.Run("cancelled context posts nothing when exec fails", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		r := &recorder{}
		r.exec = func(name string) (int, error) {
			cancel()
			return 0, ctx.Err()
		}

		_, err := signoff.Run(ctx, r.options("a", "b"))

		assert.Check(t, cmp.Len(r.names(), 0))
		assert.Check(t, cmp.DeepEqual(r.ran, []string{"a"}))
		assert.Check(t, cmp.ErrorIs(err, context.Canceled))
	})
}
