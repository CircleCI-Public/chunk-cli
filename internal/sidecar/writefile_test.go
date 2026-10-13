package sidecar

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func localSession(t *testing.T) *Session {
	t.Helper()
	keyFile, pubKey := fakes.GenerateSSHKeypair(t)
	sshSrv := fakes.NewSSHServer(t, pubKey)
	sshSrv.RunLocally()
	return &Session{
		URL:          sshSrv.Addr(),
		IdentityFile: keyFile,
		KnownHosts:   filepath.Join(t.TempDir(), "known_hosts"),
	}
}

// The data is larger than Linux allows one argument to be, which is why it
// goes on stdin.
func TestWriteFileOverSSHCreatesTheDirectoryAndWritesLargeData(t *testing.T) {
	file := filepath.Join(t.TempDir(), "it's", "nested", "history.md")
	data := []byte(strings.Repeat("comment\n", 64*1024))

	err := writeFileOverSSH(context.Background(), localSession(t), file, data)

	assert.NilError(t, err)
	got, err := os.ReadFile(file)
	assert.NilError(t, err)
	assert.Equal(t, len(got), len(data))
}

func TestWriteFileOverSSHReportsAFailedWrite(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	assert.NilError(t, os.WriteFile(blocker, nil, 0o644))

	err := writeFileOverSSH(context.Background(), localSession(t), filepath.Join(blocker, "history.md"), []byte("x"))

	assert.ErrorContains(t, err, "write "+filepath.Join(blocker, "history.md"))
}
