package validate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CachedResult is the sentinel file format for validate command caching.
type CachedResult struct {
	Status      string `json:"status"` // "pass" or "fail"
	ExitCode    int    `json:"exitCode"`
	Output      string `json:"output"`
	ContentHash string `json:"contentHash"`
	Timestamp   int64  `json:"timestamp"`
}

const maxOutputBytes = 50 * 1024

func cacheDir(workDir string) string {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		abs = workDir
	}
	h := sha256.Sum256([]byte(abs))
	return filepath.Join(os.TempDir(), "chunk-run", "cache", fmt.Sprintf("%x", h[:8]))
}

func cachePath(workDir, name string) string {
	safe := safeName(name)
	return filepath.Join(cacheDir(workDir), safe+".json")
}

func safeName(name string) string {
	var b strings.Builder
	for _, c := range name {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b.WriteRune(c)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func computeContentHash(workDir, fileExt string) string {
	h := sha256.New()

	head, err := exec.Command("git", "-C", workDir, "rev-parse", "HEAD").Output()
	if err != nil {
		h.Write([]byte("no-head"))
	} else {
		h.Write([]byte(strings.TrimSpace(string(head))))
	}

	h.Write([]byte("\n"))

	diffArgs := []string{"-C", workDir, "diff", "HEAD"}
	if fileExt != "" {
		diffArgs = append(diffArgs, "--", "*"+fileExt)
	}
	diff, err := exec.Command("git", diffArgs...).Output()
	if err == nil {
		h.Write(diff)
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}

// CheckCache reads a cached result and returns it if the content hash still matches.
// fileExt optionally scopes the content hash to files matching the extension.
func CheckCache(workDir, name, fileExt string) *CachedResult {
	data, err := os.ReadFile(cachePath(workDir, name))
	if err != nil {
		return nil
	}
	var cr CachedResult
	if err := json.Unmarshal(data, &cr); err != nil {
		return nil
	}
	current := computeContentHash(workDir, fileExt)
	if cr.ContentHash != current {
		return nil
	}
	return &cr
}

// WriteCache writes a result sentinel to the cache directory.
// fileExt optionally scopes the content hash to files matching the extension.
func WriteCache(workDir, name, fileExt string, exitCode int, output string) error {
	dir := cacheDir(workDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	if len(output) > maxOutputBytes {
		output = output[len(output)-maxOutputBytes:]
	}

	status := "pass"
	if exitCode != 0 {
		status = "fail"
	}

	cr := CachedResult{
		Status:      status,
		ExitCode:    exitCode,
		Output:      output,
		ContentHash: computeContentHash(workDir, fileExt),
		Timestamp:   time.Now().UnixMilli(),
	}

	data, err := json.MarshalIndent(cr, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(cachePath(workDir, name), data, 0o644)
}
