package secrets

import "strings"

// Resolver resolves secret reference URIs to plaintext values.
type Resolver interface {
	Resolve(ref string) (string, error)
}

// IsSecretRef reports whether value is a secret reference (op:// prefix).
func IsSecretRef(value string) bool {
	return strings.HasPrefix(value, "op://")
}

// ResolveAll resolves op:// references in a map. Non-reference values pass through unchanged.
// If resolver is nil, uses OpResolver. Returns the original map if no references are found.
func ResolveAll(vars map[string]string, resolver Resolver) (map[string]string, error) {
	if len(vars) == 0 {
		return vars, nil
	}

	// Fast path: check if any refs exist before allocating.
	hasRef := false
	for _, v := range vars {
		if IsSecretRef(v) {
			hasRef = true
			break
		}
	}
	if !hasRef {
		return vars, nil
	}

	if resolver == nil {
		resolver = &OpResolver{}
	}

	out := make(map[string]string, len(vars))
	for k, v := range vars {
		if IsSecretRef(v) {
			resolved, err := resolver.Resolve(v)
			if err != nil {
				return nil, &resolveError{key: k, cause: err}
			}
			out[k] = resolved
		} else {
			out[k] = v
		}
	}
	return out, nil
}

type resolveError struct {
	key   string
	cause error
}

func (e *resolveError) Error() string {
	return "resolve secret for " + e.key + ": " + e.cause.Error()
}

func (e *resolveError) Unwrap() error {
	return e.cause
}
