package agent

import (
	"context"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type userMessageObserverKey struct{}
type userMessageIDKey struct{}
type turnSnapshotKey struct{}

// WithTurnSnapshot attaches request-only text to the user message of the next turn.
// The model sees it after the user's words; history and transcripts keep only what the
// user typed. Desktop slash commands use it to send an expanded prompt template while
// the conversation still shows "/name args".
func WithTurnSnapshot(ctx context.Context, snapshot ai.PromptSnapshot) context.Context {
	return context.WithValue(ctx, turnSnapshotKey{}, snapshot)
}

func turnSnapshot(ctx context.Context) (ai.PromptSnapshot, bool) {
	snapshot, ok := ctx.Value(turnSnapshotKey{}).(ai.PromptSnapshot)
	return snapshot, ok && snapshot.Text != ""
}

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
