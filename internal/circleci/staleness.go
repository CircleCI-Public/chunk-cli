package circleci

import (
	"errors"
	"net/http"
	"strings"
)

// SidecarOutOfDate reports whether err is the API refusing to talk to a sidecar
// because the sidecar itself is too old.
//
// The API returns 410 Gone for two unrelated conditions with opposite remedies:
// this CLI being too old for the API, and a sidecar being too old for the API.
// Telling someone to upgrade the CLI when the sidecar is the stale one sends
// them to a version that fails identically, so the two must be told apart.
//
// Matching on the server's own wording is a compromise: the V3 error envelope
// carries no machine-readable code, so there is nothing better to key off until
// the API supplies one.
func SidecarOutOfDate(err error) bool {
	var se *StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusGone {
		return false
	}
	return strings.Contains(strings.ToLower(se.ServerMessage), "sidecar is out of date")
}

// SidecarGone reports whether err is a 404 from an operation addressed at a
// specific sidecar, meaning that sidecar no longer exists: it was deleted, or
// it expired server-side.
//
// Only call this on errors from sidecar-scoped requests. A 404 from anything
// else means a missing route, not a missing sidecar.
func SidecarGone(err error) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	return se.StatusCode == http.StatusNotFound
}
