package tools

import "context"

type processStartedKey struct{}

// WithProcessStarted lets the caller learn the ids of the processes and terminal sessions a
// tool call starts. Tools report them when the start succeeds, so the ids never come from the
// call's output text, which a foreground command controls.
func WithProcessStarted(ctx context.Context, record func(id string)) context.Context {
	return context.WithValue(ctx, processStartedKey{}, record)
}

func reportProcessStarted(ctx context.Context, id string) {
	if record, ok := ctx.Value(processStartedKey{}).(func(string)); ok && id != "" {
		record(id)
	}
}
