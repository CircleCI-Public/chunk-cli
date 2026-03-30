package sandbox

import (
	"fmt"
	"strings"
)

// dangerousEnvVars maps environment variable names that can alter sandbox
// behavior in surprising or security-relevant ways to a short explanation.
var dangerousEnvVars = map[string]string{
	"LD_PRELOAD":      "forces a shared library into every process — can execute arbitrary code",
	"LD_LIBRARY_PATH": "changes shared library search path — can substitute malicious libraries",
	"PATH":            "overwrites command search path — host PATH will likely break a Linux sandbox",
}

// DangerousEnvWarnings returns warning strings for any env var names in vars
// that are known to be dangerous to forward into a sandbox.
func DangerousEnvWarnings(vars map[string]string) []string {
	var warnings []string
	for name := range vars {
		if reason, ok := dangerousEnvVars[name]; ok {
			warnings = append(warnings, fmt.Sprintf("Forwarding %s — %s", name, reason))
		}
	}
	return warnings
}

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
