package backend

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func TestImageReviewStaleTargetDoesNotBlockOtherTargets(t *testing.T) {
	a := imageTestTarget()
	a.ID = "it_stale"
	a.Enabled = true
	b := imageTestTarget()
	b.ID = "it_healthy"
	b.Enabled = true
	cfg := config.Default()
	cfg.Image = &imaging.ImageSettings{Connections: []imaging.ImageConnection{imageTestConnection()}, Targets: []imaging.ImageTarget{a, b}}
	store := NewSettingsStore(cfg, func(*config.Config) error { return nil })
	m, err := NewImageSettingsManager(store, filepath.Join(t.TempDir(), "secrets.json"), ImageSettingsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.ResolveTarget(context.Background(), imageTestScope(), b.ID); err != nil {
		t.Fatalf("healthy baseline: %v", err)
	}
	store.mu.Lock()
	store.cfg.Image.Targets[0].Verification.BindingDigest = "stale-after-credential-rotation"
	store.mu.Unlock()
	if _, err = m.ResolveTarget(context.Background(), imageTestScope(), a.ID); err == nil {
		t.Fatal("stale target remained executable")
	}
	public, err := m.ImageSettings(imageTestScope())
	if err != nil {
		t.Fatal(err)
	}
	if !public.Targets[1].Enabled {
		t.Fatal("healthy target unexpectedly disabled in public settings")
	}
	if _, err = m.ResolveTarget(context.Background(), imageTestScope(), b.ID); err != nil {
		t.Fatalf("healthy target rejected because another target is stale: %v", err)
	}
}
