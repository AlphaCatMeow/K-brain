package main

import (
	"context"

	"github.com/Stack-Cairn/K-brain/internal/backend"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

// Verification uses the same registered production adapters, client ownership,
// scheduler and asset sink as ordinary jobs. No synchronous probe can issue
// evidence. The receipt guard is rechecked on recovery before any submission.
func resolveVerifiedImageAdapter(ctx context.Context, settings *backend.ImageSettingsManager, verification *backend.ImageVerificationManager, scope imaging.Scope, target imaging.TargetSnapshot) (imaging.Adapter, error) {
	if target.VerificationID != "" {
		if verification == nil {
			return nil, &imaging.ImageError{Code: imaging.CodeUnavailable}
		}
		if err := verification.CheckExecution(ctx, scope, target); err != nil {
			return nil, err
		}
	}
	return resolveImageAdapter(ctx, settings, scope, target)
}
