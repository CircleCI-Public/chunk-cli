package cmd

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/hook"
)

// readStdinEvent reads and parses the stdin JSON event for hook commands.
func readStdinEvent() map[string]interface{} {
	event, err := hook.ReadStdinJSON(os.Stdin)
	if err != nil {
		return map[string]interface{}{}
	}
	return event
}

// appendWriter opens, writes, and closes a file on each Write call.
// Avoids holding a file handle open for the process lifetime.
type appendWriter struct {
	path string
}

func (w *appendWriter) Write(p []byte) (int, error) {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer f.Close() //nolint:errcheck // best-effort log write
	return f.Write(p)
}

func initHookLog() {
	logDir, err := config.AppState()
	if err != nil {
		slog.SetDefault(slog.New(slog.NewJSONHandler(io.Discard, nil)))
		return
	}
	_ = os.MkdirAll(logDir, 0o755)
	w := &appendWriter{path: filepath.Join(logDir, "hook.log")}
	slog.SetDefault(slog.New(slog.NewJSONHandler(w, nil)))
}

const maxStderrCapture = 4096

// limitedBuffer captures up to a fixed number of bytes, silently
// discarding anything beyond the limit.
type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		return n, nil
	}
	if n > remaining {
		p = p[:remaining]
	}
	_, err := b.Buffer.Write(p)
	// Report the full len so the MultiWriter doesn't treat it as a short write.
	return n, err
}

func randHex5() string {
	var b [3]byte // 3 bytes → 6 hex chars, we trim to 5
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b)[:5]
}

// wrapRunE wraps all RunE functions in the command tree to capture
// exit code and stderr in the hook log.
func wrapRunE(cmd *cobra.Command) {
	if cmd.RunE != nil {
		original := cmd.RunE
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			id := randHex5()
			stderr := &limitedBuffer{limit: maxStderrCapture}
			cmd.SetErr(io.MultiWriter(cmd.ErrOrStderr(), stderr))

			slog.Info("invoked", "id", id, "cmd", cmd.CommandPath(), "args", args)
			err := original(cmd, args)

			attrs := []any{"id", id, "cmd", cmd.CommandPath(), "exit", 0}
			if err != nil {
				attrs[5] = 1
				attrs = append(attrs, "error", err.Error())
			}
			if stderr.Len() > 0 {
				attrs = append(attrs, "stderr", stderr.String())
			}
			slog.Info("completed", attrs...)
			return err
		}
	}
	for _, child := range cmd.Commands() {
		wrapRunE(child)
	}
}
