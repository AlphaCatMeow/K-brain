package cua

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func testBundle(t *testing.T, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	body := []byte("test-runtime")
	sum := sha256.Sum256(body)
	manifest := bundleManifest{Version: "test", Target: "test-target", Files: map[string]string{name: hex.EncodeToString(sum[:])}}
	m, _ := json.Marshal(manifest)
	f, _ := z.Create("bundle.json")
	f.Write(m)
	h := &zip.FileHeader{Name: name, Method: zip.Deflate}
	h.SetMode(0o755)
	f, _ = z.CreateHeader(h)
	f.Write(body)
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestBundleExtractionIntegrityAndConcurrency(t *testing.T) {
	data := testBundle(t, "bin/cua-driver")
	cache := t.TempDir()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := unpackBundle(context.Background(), data, cache, "test-target"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	root, err := unpackBundle(context.Background(), data, cache, "test-target")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unpackBundle(context.Background(), data, cache, "wrong-target"); err == nil {
		t.Fatal("accepted wrong platform")
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "cua-driver"), []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := unpackBundle(context.Background(), data, cache, "test-target"); err == nil {
		t.Fatal("accepted corrupt cache")
	}
}

func TestBundleRejectsTraversalAndCancellation(t *testing.T) {
	for _, name := range []string{"../outside", "/absolute", "C:/absolute", `bad\path`} {
		if _, err := unpackBundle(context.Background(), testBundle(t, name), t.TempDir(), "test-target"); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := unpackBundle(ctx, testBundle(t, "driver"), t.TempDir(), "test-target"); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestBundledRuntimeReadOnly(t *testing.T) {
	if os.Getenv("KB_TEST_CUA_BUNDLE") != "1" {
		t.Skip("opt-in bundled runtime extraction")
	}
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	argv, err := bundledCommand()
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) == 0 {
		t.Fatal("release runtime missing")
	}
	t.Logf("extracted bundled executable: %s", argv[0])
	out, err := exec.Command(argv[0], "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("bundled executable: %v %s", err, out)
	}
	t.Logf("bundled runtime version: %s", out)
	client := New(argv, t.TempDir())
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := client.Close(cleanup); err != nil {
			t.Error(err)
		}
	})
	if _, err := client.Discover(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Describe(ctx, "check_permissions"); err != nil {
		t.Fatal(err)
	}
	result, err := client.Call(ctx, "check_permissions", json.RawMessage("{}"))
	if err != nil || result.IsError {
		t.Fatalf("permission probe: %v %+v", err, result)
	}
	t.Logf("bundled MCP discovered %d tools; read-only permission probe passed", len(client.ordered))
}
