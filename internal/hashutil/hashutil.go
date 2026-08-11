// Package hashutil builds digests out of string parts without boundary
// collisions. Cache keys are assembled from several independent values, so the
// hash has to distinguish where one value ends and the next begins.
package hashutil

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
)

// WritePart mixes s into h length-prefixed, so that concatenations of different
// parts cannot collide (["ab","c"] and ["a","bc"] hash differently).
func WritePart(h io.Writer, s string) {
	_, _ = fmt.Fprintf(h, "%d:", len(s))
	_, _ = io.WriteString(h, s)
}

// SumParts returns the hex-encoded SHA-256 of parts, each mixed in with
// WritePart so the split between them is part of the digest.
func SumParts(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		WritePart(h, p)
	}
	return hex.EncodeToString(h.Sum(nil))
}
