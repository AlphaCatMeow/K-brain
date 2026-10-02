package ai

import "context"

// RuntimeDiagnostic carries routing facts only; headers and credentials are excluded.
type RuntimeDiagnostic struct {
	Kind        string
	Provider    string
	From        string
	To          string
	Endpoint    string
	Attempt     int
	TargetIndex int
	Error       string
}

type runtimeDiagnosticKey struct{}

func WithRuntimeDiagnosticObserver(ctx context.Context, observe func(RuntimeDiagnostic)) context.Context {
	return context.WithValue(ctx, runtimeDiagnosticKey{}, observe)
}

func ObserveRuntimeDiagnostic(ctx context.Context, event RuntimeDiagnostic) {
	if observe, ok := ctx.Value(runtimeDiagnosticKey{}).(func(RuntimeDiagnostic)); ok && observe != nil {
		observe(event)
	}
}
