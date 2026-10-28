package telemetry

import (
	"context"
	"maps"
)

type propsKey struct{}

// WithProperties attaches props to ctx, so every event Track reports from ctx
// or a context derived from it carries them. Properties attached by an outer
// call are kept unless props names them again, so related events can share an
// identifier to be joined on.
func WithProperties(ctx context.Context, props map[string]any) context.Context {
	merged := maps.Clone(propertiesFrom(ctx))
	if merged == nil {
		merged = make(map[string]any, len(props))
	}
	maps.Copy(merged, props)
	return context.WithValue(ctx, propsKey{}, merged)
}

func propertiesFrom(ctx context.Context) map[string]any {
	props, _ := ctx.Value(propsKey{}).(map[string]any)
	return props
}

// Track reports eventName with the Sender attached to ctx, carrying props and
// any properties WithProperties attached to ctx. Like the rest of telemetry
// it is best-effort: with no Sender it does nothing, and a failed enqueue is
// ignored rather than breaking what the user was doing. A nil ctx does
// nothing too.
//
// Name events Object + Action in snake_case, past tense (plan_approved), and
// keep properties flat and free of anything the user wrote: IDs, counts and
// fixed values only.
func Track(ctx context.Context, eventName string, props map[string]any) {
	if ctx == nil {
		return
	}
	s := FromContext(ctx)
	if s == nil {
		return
	}
	merged := maps.Clone(propertiesFrom(ctx))
	if merged == nil {
		merged = make(map[string]any, len(props))
	}
	maps.Copy(merged, props)
	_ = s.Track(eventName, merged)
}
