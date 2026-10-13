package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/backend"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func TestImageRuntimeMaintenanceCloseOrderAndRetry(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	settings := backend.NewSettingsStore(config.Default(), func(*config.Config) error { return nil })
	runtime, err := openImageRuntime(ctx, root, settings, true)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.maintenance == nil {
		t.Fatal("runtime did not start maintenance")
	}
	if err = runtime.maintenance.Close(ctx); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	ticks := make(chan time.Time, 1)
	var once sync.Once
	runtime.maintenance = startImageMaintenanceWithOptions(ctx, imageMaintenanceOptions{
		ticks:   ticks,
		prune:   func(context.Context) error { close(entered); <-release; return nil },
		collect: func(context.Context) error { t.Error("GC ran during shutdown"); return nil },
	})
	defer func() { once.Do(func() { close(release) }); _ = runtime.Close(ctx) }()
	ticks <- time.Now()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("maintenance did not start")
	}
	deadline, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	err = runtime.Close(deadline)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("maintenance timeout: %v", err)
	}
	select {
	case <-runtime.scheduler.Done():
		t.Fatal("scheduler closed before maintenance exited")
	default:
	}
	if _, err = runtime.store.CreateProject(ctx, imaging.Scope{OwnerID: "local", BackendID: "local"}, "still open", ""); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	closeCtx, closeCancel := context.WithTimeout(ctx, 3*time.Second)
	defer closeCancel()
	if err = runtime.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
	if err = runtime.Close(closeCtx); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
	reopened, err := openImageRuntime(ctx, root, settings, true)
	if err != nil {
		t.Fatalf("writer lock leaked: %v", err)
	}
	if err = reopened.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
}
