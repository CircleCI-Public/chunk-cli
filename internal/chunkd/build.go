package chunkd

import (
	"fmt"
	"os"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/version"
)

// executableAtStartup is the running binary as it was when this process
// started. The daemon outlives rebuilds of the binary that launched it, and
// stat'ing the executable at request time would describe whatever has since
// been written over it - so a stale daemon would answer every ping with the
// identity of the build that replaced it and never be recognised as stale.
var executableAtStartup = statExecutable()

// BuildID identifies the binary this process was started from.
//
// The daemon serves snapshots shaped by the code it was started from: a field
// added to SidecarState since then is absent rather than wrong, so a newer
// client renders a well-formed view of stale data with nothing to say why. A
// sidecar owned by a session, for instance, arrives from a pre-session daemon
// looking like a sidecar nobody owns. Comparing this on every ping is what makes
// that visible instead of silent.
//
// The version is read per call rather than frozen alongside the file, because
// main sets it after package initialisation has run.
func BuildID() string {
	return buildID(version.Value, executableAtStartup.path, executableAtStartup.size, executableAtStartup.mod)
}

// executable describes the file a process is running from.
type executable struct {
	path string
	size int64
	mod  time.Time
}

func statExecutable() executable {
	path, err := os.Executable()
	if err != nil {
		return executable{}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return executable{path: path}
	}
	return executable{path: path, size: fi.Size(), mod: fi.ModTime()}
}

// buildID formats the identity.
//
// The version alone will not do: every local build reports the same development
// version, so the executable's path, size and modification time come along to
// tell two of them apart. Path is included so a dev build and an installed one
// are never mistaken for each other.
//
// Split out so it can be tested without standing up binaries on disk.
func buildID(ver, exe string, size int64, mod time.Time) string {
	if exe == "" {
		return ver
	}
	return fmt.Sprintf("%s|%s|%d|%d", ver, exe, size, mod.UnixNano())
}
