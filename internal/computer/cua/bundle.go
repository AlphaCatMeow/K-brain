package cua

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/datapath"
	"github.com/gofrs/flock"
)

//go:embed bundle/*
var bundledRuntime embed.FS

type bundleManifest struct {
	Version string            `json:"version"`
	Target  string            `json:"target"`
	Files   map[string]string `json:"files"`
}

func bundledCommand() ([]string, error) {
	data, err := bundledRuntime.ReadFile("bundle/runtime.zip")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	dir, err := datapath.UserDir()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root, err := unpackBundle(ctx, data, filepath.Join(dir, "runtimes", "cua"), runtime.GOOS+"-"+runtime.GOARCH)
	if err != nil {
		return nil, fmt.Errorf("prepare bundled Cua: %w", err)
	}
	if runtime.GOOS == "darwin" {
		// A KB-owned direct runtime avoids installing or launching a shared daemon.
		return []string{filepath.Join(root, "CuaDriver.app", "Contents", "MacOS", "cua-driver"), "mcp", "--direct"}, nil
	}
	name := "cua-driver"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return []string{filepath.Join(root, name), "mcp"}, nil
}

func bundlePath(name string) bool {
	return fs.ValidPath(name) && name != "." && !strings.ContainsAny(name, "\\:")
}

func unpackBundle(ctx context.Context, data []byte, cache, target string) (string, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	manifestFile, err := archive.Open("bundle.json")
	if err != nil {
		return "", err
	}
	var manifest bundleManifest
	err = json.NewDecoder(io.LimitReader(manifestFile, 1<<20)).Decode(&manifest)
	manifestFile.Close()
	if err != nil {
		return "", err
	}
	if manifest.Target != target || manifest.Version == "" || len(manifest.Files) == 0 {
		return "", fmt.Errorf("Cua bundle target or manifest invalid: %s", manifest.Target)
	}
	sum := sha256.Sum256(data)
	root := filepath.Join(cache, hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(cache, 0o700); err != nil {
		return "", err
	}
	lock := flock.New(root+".lock", flock.SetPermissions(0o600))
	ok, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("Cua bundle lock unavailable")
	}
	defer lock.Unlock()
	if err := verifyBundle(ctx, root, manifest); err == nil {
		return root, nil
	}
	if _, err := os.Lstat(root); err == nil {
		return "", errors.New("bundled Cua cache integrity check failed; remove this runtime cache before retrying")
	}
	stage, err := os.MkdirTemp(cache, ".extract-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	seen := map[string]bool{}
	var total uint64
	for _, entry := range archive.File {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		name := entry.Name
		if name == "bundle.json" {
			continue
		}
		expected, ok := manifest.Files[name]
		if !ok || !bundlePath(name) || seen[name] || !entry.Mode().IsRegular() {
			return "", errors.New("unsafe or unexpected Cua bundle entry")
		}
		seen[name] = true
		total += entry.UncompressedSize64
		if total > 2<<30 || entry.UncompressedSize64 > 1<<30 {
			return "", errors.New("Cua bundle too large")
		}
		dest := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
			return "", err
		}
		in, err := entry.Open()
		if err != nil {
			return "", err
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, entry.Mode().Perm()&0o755)
		if err != nil {
			in.Close()
			return "", err
		}
		hash := sha256.New()
		_, copyErr := io.Copy(io.MultiWriter(out, hash), io.LimitReader(in, int64(entry.UncompressedSize64)+1))
		closeErr := out.Close()
		in.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		if hex.EncodeToString(hash.Sum(nil)) != expected {
			return "", errors.New("Cua bundle file hash mismatch")
		}
	}
	if len(seen) != len(manifest.Files) {
		return "", errors.New("Cua bundle file missing")
	}
	if err := verifyBundle(ctx, stage, manifest); err != nil {
		return "", err
	}
	if err := os.Rename(stage, root); err != nil {
		return "", err
	}
	return root, nil
}

func verifyBundle(ctx context.Context, root string, manifest bundleManifest) error {
	for name, expected := range manifest.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !bundlePath(name) {
			return errors.New("invalid Cua manifest path")
		}
		path := filepath.Join(root, filepath.FromSlash(name))
		// Reject links in every component, including the cache root.
		for p := path; ; p = filepath.Dir(p) {
			info, err := os.Lstat(p)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("Cua cache contains a symlink")
			}
			if p == root {
				break
			}
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, err = io.Copy(hash, file)
		file.Close()
		if err != nil {
			return err
		}
		if hex.EncodeToString(hash.Sum(nil)) != expected {
			return errors.New("Cua cache hash mismatch")
		}
	}
	return nil
}
