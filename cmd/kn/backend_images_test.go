package main

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/backend"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func TestImageRuntimePersistentAssetsAndSingleWriter(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cfg := config.Default()
	settings := backend.NewSettingsStore(cfg, func(value *config.Config) error {
		return value.SaveFile(filepath.Join(root, "config.json"))
	})
	runtime, err := openImageRuntime(ctx, root, settings, true)
	if err != nil {
		t.Fatal(err)
	}
	closeRuntime := func(r *imageRuntime) {
		t.Helper()
		closeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := r.Close(closeCtx); err != nil {
			t.Error(err)
		}
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			closeRuntime(runtime)
		}
	})
	if _, err := openImageRuntime(ctx, root, settings, true); err == nil {
		t.Fatal("second runtime acquired active writer")
	}
	scope := imaging.Scope{OwnerID: "local", BackendID: "local"}
	project, err := runtime.store.CreateProject(ctx, scope, "持久项目", "")
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	if err := png.Encode(&raw, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	asset, err := runtime.assets.ImportAsset(ctx, scope, bytes.NewReader(raw.Bytes()), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	closeRuntime(runtime)
	closed = true
	reopened, err := openImageRuntime(ctx, root, settings, false)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRuntime(reopened)
	if reopened.module.AllowLocalImageScope {
		t.Fatal("remote listener acquired local scope")
	}
	if got, err := reopened.store.ReadProject(scope, project.ID); err != nil || got.Title != project.Title {
		t.Fatalf("project was not restored: %v", err)
	}
	reader, _, err := reopened.assets.OpenAsset(ctx, scope, asset.ID, "original")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || !bytes.Equal(got, raw.Bytes()) {
		t.Fatal("original image bytes changed after restart")
	}
	if _, _, err := reopened.assets.OpenAsset(ctx, imaging.Scope{OwnerID: "other", BackendID: "local"}, asset.ID, "original"); err == nil {
		t.Fatal("runtime exposed another owner's asset")
	}
}

func TestImageRuntimeLocalListener(t *testing.T) {
	for address, want := range map[string]bool{
		"127.0.0.1:47321": true, "[::1]:0": true,
		"0.0.0.0:47321": false, "[::]:47321": false,
		":47321": false, "192.0.2.1:47321": false,
		"localhost:47321": false, "invalid": false,
	} {
		if got := localImageListener(address); got != want {
			t.Errorf("%s: got %v, want %v", address, got, want)
		}
	}
}
