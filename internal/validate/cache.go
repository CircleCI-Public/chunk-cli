package validate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// ResultCache is a read/write store for validate run outcomes.
type ResultCache interface {
	Get(key string) (CachedResult, bool)
	Put(key string, r CachedResult) error
}

// CachedResult records the timestamp of a successful validate run. Only
// successful runs are cached; failures are never stored so the agent always
// retries after a fix, even when the working tree has not changed.
type CachedResult struct {
	CachedAt time.Time `json:"cached_at"`
}

// CacheKey builds a content-addressed key from a name and ordered content
// parts. Parts are length-prefixed before hashing to prevent boundary
// collisions (["ab","c"] and ["a","bc"] produce different keys).
func CacheKey(name string, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = fmt.Fprintf(h, "%d:", len(p))
		_, _ = io.WriteString(h, p)
	}
	return name + "\x00" + hex.EncodeToString(h.Sum(nil))
}

// BuildCacheKey constructs the cache key for a validate run. It hashes the
// serialized commands, the HEAD commit SHA, and the working-tree status so
// the key changes whenever code or configuration changes.
// commandName is "" when all commands are run.
func BuildCacheKey(workDir, commandName string, commands any) string {
	cfgBytes, _ := json.Marshal(commands)
	return CacheKey(
		commandName,
		sha256hex(cfgBytes),
		strings.TrimSpace(gitOut(workDir, "rev-parse", "HEAD")),
		sha256hex([]byte(gitOut(workDir, "status", "--porcelain"))),
	)
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func gitOut(workDir string, args ...string) string {
	cmdArgs := append([]string{"-C", workDir}, args...)
	out, err := exec.Command("git", cmdArgs...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}
