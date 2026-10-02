package tools

import "context"

type runIdentityKey struct{}

type RunIdentity struct {
	ConversationID string
	RunID          string
}

func WithRunIdentity(ctx context.Context, identity RunIdentity) context.Context {
	return context.WithValue(ctx, runIdentityKey{}, identity)
}

func RunIdentityFromContext(ctx context.Context) (RunIdentity, bool) {
	identity, ok := ctx.Value(runIdentityKey{}).(RunIdentity)
	if !ok || identity.ConversationID == "" || identity.RunID == "" {
		return RunIdentity{}, false
	}
	return identity, true
}
