package sidecar

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"strings"
)

// FileWriter writes data to path on a sidecar, creating its directory.
type FileWriter func(ctx context.Context, entry *PoolEntry, path string, data []byte) error

// WriteFile writes data to path on the entry's sidecar. The data goes over SSH
// on stdin rather than in the command, so its size is not bounded by the
// remote shell's argument limit.
func WriteFile(ctx context.Context, entry *PoolEntry, path string, data []byte) error {
	session, err := OpenSession(ctx, entry.Client, entry.ID, false)
	if err != nil {
		return err
	}
	return writeFileOverSSH(ctx, session, path, data)
}

func writeFileOverSSH(ctx context.Context, session *Session, file string, data []byte) error {
	script := fmt.Sprintf("mkdir -p %s && cat > %s", ShellEscape(path.Dir(file)), ShellEscape(file))
	res, err := ExecOverSSH(ctx, session, script, bytes.NewReader(data), nil)
	if err != nil {
		return fmt.Errorf("write %s: %w", file, err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("write %s: exit %d: %s", file, res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return nil
}
