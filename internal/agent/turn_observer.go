package agent

import (
	"context"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type userMessageObserverKey struct{}
type userMessageIDKey struct{}

func WithUserMessageID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userMessageIDKey{}, id)
}

// WithUserMessageObserver observes the durable turn identity before model or tool execution.
func WithUserMessageObserver(ctx context.Context, observe func(ai.Message) error) context.Context {
	return context.WithValue(ctx, userMessageObserverKey{}, observe)
}

func observeUserMessage(ctx context.Context, message ai.Message) error {
	if observe, ok := ctx.Value(userMessageObserverKey{}).(func(ai.Message) error); ok {
		return observe(message)
	}
	return nil
}
