package tools

import (
	"context"
	"encoding/json"
)

type ReviewHooks struct {
	Before func(context.Context, string, json.RawMessage) (context.Context, error)
	After  func(context.Context, string, json.RawMessage, Result) Result
}
type reviewKey struct{}

func WithReview(ctx context.Context, hooks ReviewHooks) context.Context {
	return context.WithValue(ctx, reviewKey{}, hooks)
}
