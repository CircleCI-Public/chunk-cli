package telemetry

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/segmentio/analytics-go/v3"

	"github.com/CircleCI-Public/chunk-cli/internal/telemetry/receiver"
)

// delegateDestination buffers events and, on Close, re-execs the chunk
// binary as a detached "receive-telemetry" subprocess to deliver them. This
// keeps Segment's network round trip off the CLI's exit path.
type delegateDestination struct {
	bin      string
	writeKey string
	endpoint string

	mu       sync.RWMutex
	messages []analytics.Track
}

func (d *delegateDestination) Enqueue(message analytics.Track) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.messages = append(d.messages, message)
	return nil
}

func (d *delegateDestination) Close() error {
	d.mu.RLock()
	defer d.mu.RUnlock()

	if len(d.messages) == 0 {
		return nil
	}

	buf, err := json.Marshal(d.messages)
	if err != nil {
		return err
	}

	_ = d.send(bytes.NewReader(buf))

	return nil
}

func (d *delegateDestination) send(in io.Reader) error {
	bin := d.bin
	if abs, err := filepath.Abs(bin); err == nil {
		bin = abs
	}

	//#nosec:G204 // This is the path of our own binary
	cmd := exec.Command(bin, "receive-telemetry")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // avoid signals sent to the parent (e.g. Ctrl-C) reaching this subprocess too
	cmd.Env = append(os.Environ(),
		receiver.EnvWriteKey+"="+d.writeKey,
		receiver.EnvTelemetryEndpoint+"="+d.endpoint,
	)
	if filepath.IsAbs(bin) {
		cmd.Dir = os.TempDir()
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

	err = cmd.Start()
	if err != nil {
		_ = stdin.Close()
		return err
	}

	_, _ = io.Copy(stdin, in)
	_ = stdin.Close()

	return cmd.Process.Release()
}
