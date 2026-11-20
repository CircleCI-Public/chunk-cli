package server

import (
	"context"

	"github.com/CircleCI-Public/chunk-cli/internal/factory"
)

// PlanConfig is what the daemon builds factory plans with. Every field is a
// test seam: left nil, the daemon uses the real thing.
type PlanConfig struct {
	// StartChat starts a chat session; factory.StartAgent when nil.
	StartChat func(ctx context.Context, cfg factory.AgentConfig, message string) (AgentSession, error)
	// OpenChatRecord opens a repository's chat record;
	// factory.OpenChatSession when nil.
	OpenChatRecord func(root string) (SessionRecord, error)
}

// Option customizes RunDaemon.
type Option func(*daemonOptions)

type daemonOptions struct {
	plans PlanConfig
}

// WithPlans configures how the daemon builds factory plans.
func WithPlans(cfg PlanConfig) Option {
	return func(o *daemonOptions) { o.plans = cfg }
}

// apiError is a refusal carrying the HTTP status the route should answer with.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }
