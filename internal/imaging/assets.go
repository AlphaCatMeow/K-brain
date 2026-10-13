package imaging

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type AssetStore struct {
	store *Store
	root  *os.Root
	mu    sync.Mutex
	fault func(string) error // Test-only asset persistence boundaries.
}

func NewAssetStore(store *Store, dir string) (*AssetStore, error) {
	if store == nil || dir == "" {
		return nil, invalid("asset store requires storage directory")
	}
	// Keep the shared platform replacement helper independent of later cwd changes.
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &AssetStore{store: store, root: root}, nil
}
func (s *AssetStore) Close() error { return s.root.Close() }

func (s *AssetStore) ImportAsset(ctx context.Context, scope Scope, input io.Reader, mime string) (ImageAsset, error) {
	if scope.OwnerID == "" || scope.BackendID == "" {
		return ImageAsset{}, &ImageError{Code: CodeForbidden}
	}
	content, err := DecodeImageContent(ctx, input, mime, UploadImageLimits)
	if err != nil {
		return ImageAsset{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	asset, err := s.saveContentLocked(ctx, content, "upload")
	if err != nil {
		return ImageAsset{}, err
	}
	if err := s.store.Update(WithScope(ctx, scope), func(tx *Transaction) error { return tx.PutAsset(asset) }); err != nil {
		return ImageAsset{}, err
	}
	return asset, nil
}

// SaveOutput stages files; the caller commits the asset and job result together.
// A durable busy job or nonempty execution lease must protect the entire interval
// through that commit, including cancellation after this method returns.
func (s *AssetStore) SaveOutput(ctx context.Context, scope Scope, source OutputSource) (ImageAsset, error) {
	if scope.OwnerID == "" || scope.BackendID == "" {
		return ImageAsset{}, &ImageError{Code: CodeForbidden}
	}
	if source.Kind != "bytes" {
		return ImageAsset{}, ErrUnsupported
	}
	content, err := DecodeImageContent(ctx, bytes.NewReader(source.Data), source.MIME, OutputImageLimits)
	if err != nil {
		return ImageAsset{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveContentLocked(ctx, content, "generated")
}

// saveContentLocked requires mu through any immediately following asset commit.
func (s *AssetStore) saveContentLocked(ctx context.Context, content ImageContent, source string) (ImageAsset, error) {
	if err := ctx.Err(); err != nil {
		return ImageAsset{}, err
	}
	id, err := assetRandomID()
	if err != nil {
		return ImageAsset{}, err
	}
	if err := s.writeContent(content.Hash+".original", content.Data); err != nil {
		return ImageAsset{}, err
	}
	if err := s.writeContent(content.Hash+".thumbnail", content.Thumbnail); err != nil {
		return ImageAsset{}, err
	}
	return ImageAsset{ID: "ia_" + id, Hash: content.Hash, MIME: content.MIME, Bytes: int64(len(content.Data)), Width: content.Width, Height: content.Height, Source: source, ParentAssetIDs: []string{}, CreatedAt: time.Now().UTC(), Available: true}, nil
}
func assetRandomID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}
func (s *AssetStore) writeContent(name string, data []byte) error {
	existing, err := s.root.ReadFile(name)
	if err == nil && bytes.Equal(existing, data) {
		// Retry a directory barrier that may have failed after an earlier rename.
		return s.syncContentDirectory()
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	id, err := assetRandomID()
	if err != nil {
		return err
	}
	temp := "tmp_" + id
	f, err := s.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer s.root.Remove(temp)
	_, writeErr := f.Write(data)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	// Asset directories are backend-owned, as are Store checkpoint directories.
	// On Windows the shared replacement helper uses MOVEFILE_WRITE_THROUGH;
	// syncStoreDirectory is a no-op there (directory fsync is unsupported).
	if err := replaceStoreFile(filepath.Join(s.root.Name(), temp), filepath.Join(s.root.Name(), name)); err != nil {
		return err
	}
	return s.syncContentDirectory()
}

func (s *AssetStore) syncContentDirectory() error {
	if s.fault != nil {
		if err := s.fault("asset.before_directory_sync"); err != nil {
			return err
		}
	}
	return syncStoreDirectory(s.root.Name())
}

func (s *AssetStore) OpenAsset(ctx context.Context, scope Scope, id, variant string) (io.ReadCloser, ImageAsset, error) {
	if err := ctx.Err(); err != nil {
		return nil, ImageAsset{}, err
	}
	if variant != "original" && variant != "thumbnail" {
		return nil, ImageAsset{}, invalid("invalid asset variant")
	}
	asset, err := s.store.ReadAsset(scope, id)
	if err != nil {
		return nil, ImageAsset{}, err
	}
	if !asset.Available {
		return nil, ImageAsset{}, notFound()
	}
	hash, err := hex.DecodeString(asset.Hash)
	if err != nil || len(hash) != 32 {
		return nil, ImageAsset{}, invalid("invalid stored image hash")
	}
	f, err := s.root.Open(asset.Hash + "." + variant)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			asset.Available = false
			if saveErr := s.store.Update(WithScope(ctx, scope), func(tx *Transaction) error { return tx.PutAsset(asset) }); saveErr != nil {
				return nil, ImageAsset{}, saveErr
			}
			return nil, ImageAsset{}, notFound()
		}
		return nil, ImageAsset{}, &ImageError{Code: CodeUnavailable}
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, ImageAsset{}, &ImageError{Code: CodeUnavailable}
	}
	asset.Bytes = info.Size()
	if variant == "thumbnail" {
		asset.MIME = "image/png"
	}
	return f, asset, nil
}

func (s *AssetStore) ResolveAsset(scope Scope, id string) (ResolvedAsset, error) {
	asset, err := s.store.ReadAsset(scope, id)
	if err != nil {
		return ResolvedAsset{}, err
	}
	if !asset.Available {
		return ResolvedAsset{}, notFound()
	}
	return ResolvedAsset{AssetID: id, MIME: asset.MIME, Bytes: asset.Bytes, Open: func() (io.ReadCloser, error) {
		reader, _, err := s.OpenAsset(context.Background(), scope, id, "original")
		return reader, err
	}}, nil
}
