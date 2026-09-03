// Package envctx carries an environment variable slice in a context so
// in-process callers (like the watch daemon) can override the process env
// without calling os.Setenv.
package envctx

import (
	"context"
	"os"
	"strings"
)

type envKey struct{}

// WithEnv returns a ctx that carries env (a slice of KEY=VALUE strings).
// Subsequent Getenv and Environ calls on that ctx consult env first.
func WithEnv(ctx context.Context, env []string) context.Context {
	if len(env) == 0 {
		return ctx
	}
	return context.WithValue(ctx, envKey{}, env)
}

// Getenv returns the value of key from the env slice stored in ctx.
// Falls back to os.Getenv when ctx carries no slice or the key is absent.
func Getenv(ctx context.Context, key string) string {
	if env, ok := ctx.Value(envKey{}).([]string); ok {
		prefix := key + "="
		for _, e := range env {
			if strings.HasPrefix(e, prefix) {
				return e[len(prefix):]
			}
		}
		return ""
	}
	return os.Getenv(key)
}

// Environ returns the env slice stored in ctx, or os.Environ() if none.
func Environ(ctx context.Context) []string {
	if env, ok := ctx.Value(envKey{}).([]string); ok {
		return env
	}
	return os.Environ()
}
