package sandbox

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ParseEnvPairs parses a slice of KEY=VALUE strings and returns a map.
// Returns an error if any entry does not contain "=".
func ParseEnvPairs(pairs []string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(pairs))
	for _, pair := range pairs {
		idx := strings.IndexByte(pair, '=')
		if idx < 0 {
			return nil, fmt.Errorf("%q is not a KEY=VALUE pair", pair)
		}
		key := pair[:idx]
		val := pair[idx+1:]
		result[key] = val
	}
	return result, nil
}

// ParseEnvFile reads dotenv-format KEY=VALUE lines from r.
// Supports blank lines, # comments, optional "export " prefix, and optional
// single/double quoting of values. No variable interpolation.
func ParseEnvFile(r io.Reader) (map[string]string, error) {
	result := make(map[string]string)
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimSpace(line)
		idx := strings.IndexByte(line, '=')
		if idx < 0 {
			return nil, fmt.Errorf("invalid line in env file: %q", line)
		}
		key := strings.TrimSpace(line[:idx])
		val := line[idx+1:]
		val = unquote(val)
		result[key] = val
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read env file: %w", err)
	}
	return result, nil
}

// unquote strips a matching pair of leading/trailing single or double quotes.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// LoadEnvFile reads .env.local from dir. Returns nil, nil if the file does not
// exist. Returns an error for permission or parse failures.
func LoadEnvFile(dir string) (map[string]string, error) {
	path := filepath.Join(dir, ".env.local")
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	vars, err := ParseEnvFile(f)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return vars, nil
}

// MergeEnv merges maps left to right; later layers win on duplicate keys.
func MergeEnv(layers ...map[string]string) map[string]string {
	result := make(map[string]string)
	for _, layer := range layers {
		for k, v := range layer {
			result[k] = v
		}
	}
	return result
}
