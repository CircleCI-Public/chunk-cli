package sandbox

import (
	"fmt"
	"strings"
)

// ResolveEnvVars parses a comma-separated list of environment variable names,
// looks each up via lookup, and returns a map of name→value. Returns nil when
// spec is empty. Returns an error if any named variable is not set.
func ResolveEnvVars(spec string, lookup func(string) (string, bool)) (map[string]string, error) {
	if spec == "" {
		return nil, nil
	}

	result := make(map[string]string)
	for _, part := range strings.Split(spec, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		val, ok := lookup(name)
		if !ok {
			return nil, fmt.Errorf("environment variable %q is not set", name)
		}
		result[name] = val
	}

	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}
