package imaging

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
)

func TestImageAssetImportOwnershipAndReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	assets, err := NewAssetStore(store, filepath.Join(dir, "images"))
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{OwnerID: "alice", BackendID: "backend"}
	raw := imageTestBytes(t, "png", 20, 10)
	asset, err := assets.ImportAsset(context.Background(), scope, bytes.NewReader(raw), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := assets.OpenAsset(context.Background(), Scope{OwnerID: "bob", BackendID: "backend"}, asset.ID, "original"); err == nil {
		t.Fatal("other owner read asset")
	}
	assets.Close()
	store.Close()
	store, err = OpenStore(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assets, err = NewAssetStore(store, filepath.Join(dir, "images"))
	if err != nil {
		t.Fatal(err)
	}
	defer assets.Close()
	reader, metadata, err := assets.OpenAsset(context.Background(), scope, asset.ID, "original")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) || metadata.ID != asset.ID || !metadata.Available {
		t.Fatal("original not restored")
	}
	thumb, meta, err := assets.OpenAsset(context.Background(), scope, asset.ID, "thumbnail")
	if err != nil {
		t.Fatal(err)
	}
	defer thumb.Close()
	if meta.MIME != "image/png" {
		t.Fatal("thumbnail media type")
	}
	if _, _, err := assets.OpenAsset(context.Background(), scope, asset.ID, "../original"); err == nil {
		t.Fatal("invalid variant accepted")
	}
}

func TestImageAssetOutputNeedsTransactionBeforeRead(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assets, err := NewAssetStore(store, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer assets.Close()
	scope := Scope{OwnerID: "alice", BackendID: "backend"}
	raw := imageTestBytes(t, "jpeg", 2, 2)
	asset, err := assets.SaveOutput(context.Background(), scope, OutputSource{Kind: "bytes", MIME: "image/jpeg", Data: raw})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := assets.OpenAsset(context.Background(), scope, asset.ID, "original"); err == nil {
		t.Fatal("uncommitted asset is public")
	}
	if err := store.Update(WithScope(context.Background(), scope), func(tx *Transaction) error { return tx.PutAsset(asset) }); err != nil {
		t.Fatal(err)
	}
	r, _, err := assets.OpenAsset(context.Background(), scope, asset.ID, "original")
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if _, err := assets.SaveOutput(context.Background(), scope, OutputSource{Kind: "url", URL: "https://example.test/result.png"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unimplemented download must be explicit: %v", err)
	}
}

func TestImageAssetResolveChecksScopeAndMissingFiles(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assets, err := NewAssetStore(store, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer assets.Close()
	scope := Scope{OwnerID: "alice", BackendID: "backend"}
	asset, err := assets.ImportAsset(context.Background(), scope, bytes.NewReader(imageTestBytes(t, "png", 2, 2)), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := assets.ResolveAsset(scope, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	r, err := resolved.Open()
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	if _, err := assets.ResolveAsset(Scope{OwnerID: "bob", BackendID: "backend"}, asset.ID); err == nil {
		t.Fatal("cross-owner input resolved")
	}
	if err := assets.root.Remove(asset.Hash + ".original"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := assets.OpenAsset(context.Background(), scope, asset.ID, "original"); err == nil {
		t.Fatal("missing file advertised as available")
	}
	stored, err := store.ReadAsset(scope, asset.ID)
	if err != nil || stored.Available {
		t.Fatal("missing asset state was not persisted")
	}
}
