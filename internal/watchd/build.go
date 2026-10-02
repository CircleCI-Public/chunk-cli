package watchd

import (
	"fmt"
	"os"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/version"
)

// buildIdentity is captured as the process starts. The daemon outlives
// rebuilds of the binary it was launched from, and stat'ing the executable at
// request time would report whatever has since been written over it - so a
// stale daemon would answer every ping with the identity of the build that
// replaced it and never be recognised as stale.
var buildIdentity = currentBuildID()

// BuildID identifies the binary this process was started from.
//
// The daemon serves snapshots shaped by the code it was started from: a field
// added to SidecarState since then is absent rather than wrong, so a newer
// client renders a well-formed view of stale data with nothing to say why. A
// sidecar owned by a session, for instance, arrives from a pre-session daemon
// looking like a sidecar nobody owns. Comparing this on every ping is what makes
// that visible instead of silent.
func BuildID() string { return buildIdentity }

// currentBuildID reads the running executable's identity.
//
// The version alone will not do: every local build reports the same development
// version, so the executable's path, size and modification time come along to
// tell two of them apart. Path is included so a dev build and an installed one
// are never mistaken for each other.
func currentBuildID() string {
	exe, err := os.Executable()
	if err != nil {
		return buildID(version.Value, "", 0, time.Time{})
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return buildID(version.Value, exe, 0, time.Time{})
	}
	return buildID(version.Value, exe, fi.Size(), fi.ModTime())
}

// buildID formats the identity. Split out from currentBuildID so it can be
// tested without standing up binaries on disk.
func buildID(ver, exe string, size int64, mod time.Time) string {
	if exe == "" {
		return ver
	}
	return fmt.Sprintf("%s|%s|%d|%d", ver, exe, size, mod.UnixNano())
}
