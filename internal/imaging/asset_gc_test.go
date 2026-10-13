package imaging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type assetGCTest struct {
	t      *testing.T
	store  *Store
	assets *AssetStore
	dir    string
	now    time.Time
}

func newAssetGCTest(t *testing.T) *assetGCTest {
	t.Helper()
	g := &assetGCTest{t: t, dir: t.TempDir(), now: time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC)}
	g.open()
	t.Cleanup(func() { g.assets.Close(); g.store.Close() })
	return g
}
func (g *assetGCTest) open() {
	g.t.Helper()
	var err error
	g.store, err = OpenStore(filepath.Join(g.dir, "store"))
	if err != nil {
		g.t.Fatal(err)
	}
	g.store.now = func() time.Time { return g.now }
	g.assets, err = NewAssetStore(g.store, filepath.Join(g.dir, "assets"))
	if err != nil {
		g.t.Fatal(err)
	}
}
func (g *assetGCTest) reopen() {
	g.t.Helper()
	if err := g.assets.Close(); err != nil {
		g.t.Fatal(err)
	}
	if err := g.store.Close(); err != nil {
		g.t.Fatal(err)
	}
	g.open()
}
func (g *assetGCTest) run() AssetGCResult {
	g.t.Helper()
	result, err := g.assets.CollectGarbage(context.Background(), AssetGCOptions{Now: func() time.Time { return g.now }})
	if err != nil {
		g.t.Fatal(err)
	}
	return result
}
func (g *assetGCTest) asset(scope Scope, source string, width int) ImageAsset {
	g.t.Helper()
	a, err := g.assets.SaveOutput(context.Background(), scope, OutputSource{Kind: "bytes", MIME: "image/png", Data: imageTestBytes(g.t, "png", width, 2)})
	if err != nil {
		g.t.Fatal(err)
	}
	a.Source = source
	a.CreatedAt = g.now.Add(-8 * 24 * time.Hour)
	if err := g.store.Update(WithScope(context.Background(), scope), func(tx *Transaction) error { return tx.PutAsset(a) }); err != nil {
		g.t.Fatal(err)
	}
	return a
}
func (g *assetGCTest) job(scope Scope, id string, status JobStatus, inputs, outputs []string) ImageProject {
	g.t.Helper()
	p, err := g.store.CreateProject(context.Background(), scope, id, filepath.Join(g.dir, "personal-workspace"))
	if err != nil {
		g.t.Fatal(err)
	}
	j := ImageJob{ID: id, ProjectID: p.ID, Revision: 1, Status: status, CreatedAt: g.now,
		Items: []ImageItem{{ID: id + "_item", Status: status, OutputAssetIDs: outputs}}}
	for _, id := range inputs {
		j.Request.Inputs = append(j.Request.Inputs, InputAsset{AssetID: id, Role: "reference"})
	}
	if err := g.store.Update(WithScope(context.Background(), scope), func(tx *Transaction) error {
		if err := tx.PutJob(j); err != nil {
			return err
		}
		if err := tx.AppendEvent(ImageEvent{JobID: id, Type: EventJobAccepted}); err != nil {
			return err
		}
		return tx.PutIdempotency(scope, id+"_key", "request-hash", id)
	}); err != nil {
		g.t.Fatal(err)
	}
	return p
}
func (g *assetGCTest) files(a ImageAsset, present bool) {
	g.t.Helper()
	for _, variant := range []string{"original", "thumbnail"} {
		_, err := g.assets.root.Lstat(a.Hash + "." + variant)
		if present && err != nil {
			g.t.Fatalf("expected %s: %v", variant, err)
		}
		if !present && !errors.Is(err, os.ErrNotExist) {
			g.t.Fatalf("expected removed %s, got %v", variant, err)
		}
	}
}
func (g *assetGCTest) available(scope Scope, a ImageAsset, want bool) {
	g.t.Helper()
	got, err := g.store.ReadAsset(scope, a.ID)
	if err != nil || got.Available != want {
		g.t.Fatalf("availability = %v, %v; want %v", got.Available, err, want)
	}
}

func TestAssetGCSharedHashAcrossOwnersAndBackends(t *testing.T) {
	g := newAssetGCTest(t)
	alice := Scope{OwnerID: "alice", BackendID: "one"}
	bob := Scope{OwnerID: "bob", BackendID: "two"}
	a := g.asset(alice, "generated", 2)
	b := g.asset(bob, "generated", 2)
	if a.Hash != b.Hash || a.ID == b.ID {
		t.Fatal("test did not create independently owned shared content")
	}
	pa := g.job(alice, "ij_alice", JobSucceeded, nil, []string{a.ID})
	pb := g.job(bob, "ij_bob", JobSucceeded, nil, []string{b.ID})
	if err := g.store.DeleteProject(context.Background(), alice, pa.ID, pa.Revision); err != nil {
		t.Fatal(err)
	}
	g.now = g.now.Add(30 * 24 * time.Hour)
	g.run()
	g.reopen()
	g.now = g.now.Add(24 * time.Hour)
	result := g.run()
	if result.UnavailableAssets != 1 || result.RemovedFiles != 0 {
		t.Fatal(result)
	}
	g.available(alice, a, false)
	g.available(bob, b, true)
	g.files(b, true)
	for _, variant := range []string{"original", "thumbnail"} {
		r, _, err := g.assets.OpenAsset(context.Background(), bob, b.ID, variant)
		if err != nil {
			t.Fatal(err)
		}
		r.Close()
	}
	if err := g.store.DeleteProject(context.Background(), bob, pb.ID, pb.Revision); err != nil {
		t.Fatal(err)
	}
	g.now = g.now.Add(30 * 24 * time.Hour)
	g.run()
	g.now = g.now.Add(24 * time.Hour)
	if result := g.run(); result.RemovedFiles != 2 {
		t.Fatal(result)
	}
	g.files(a, false)
	g.reopen()
	g.available(bob, b, false)
}

func TestAssetGCTombstoneRetentionAndIdempotency(t *testing.T) {
	g := newAssetGCTest(t)
	a := g.asset(storeScope, "generated", 2)
	p := g.job(storeScope, "ij_tombstone", JobPartialSucceeded, nil, []string{a.ID})
	if err := g.store.DeleteProject(context.Background(), storeScope, p.ID, p.Revision); err != nil {
		t.Fatal(err)
	}
	g.now = g.now.Add(29 * 24 * time.Hour)
	if result := g.run(); result.MarkedAssets != 0 || result.RemovedFiles != 0 {
		t.Fatal(result)
	}
	g.reopen()
	if _, ok := g.store.GetIdempotency(storeScope, "ij_tombstone_key"); !ok {
		t.Fatal("30-day idempotency tombstone missing")
	}
	g.files(a, true)
	g.now = g.now.Add(24 * time.Hour)
	if _, ok := g.store.GetIdempotency(storeScope, "ij_tombstone_key"); ok {
		t.Fatal("expired idempotency tombstone still replayable")
	}
	if result := g.run(); result.MarkedAssets != 1 || result.MarkedFiles != 2 || result.RemovedFiles != 0 {
		t.Fatal(result)
	}
	g.now = g.now.Add(24*time.Hour - time.Nanosecond)
	g.run()
	g.files(a, true)
	g.now = g.now.Add(time.Nanosecond)
	g.run()
	g.files(a, false)
}

func TestAssetGCRetainsPartialUnknownAndBusyTaskInputsOutputs(t *testing.T) {
	for _, status := range []JobStatus{JobPartialSucceeded, JobResultUnknown, JobRunning, JobSaving, JobQueued, JobCancelRequested} {
		t.Run(string(status), func(t *testing.T) {
			g := newAssetGCTest(t)
			input := g.asset(storeScope, "upload", 2)
			output := g.asset(storeScope, "generated", 3)
			g.job(storeScope, "ij_kept", status, []string{input.ID}, []string{output.ID})
			// A staged output has no record yet, so only a busy task protects it.
			staged, err := g.assets.SaveOutput(context.Background(), storeScope, OutputSource{Kind: "bytes", MIME: "image/png", Data: imageTestBytes(t, "png", 4, 2)})
			if err != nil {
				t.Fatal(err)
			}
			g.now = g.now.Add(100 * 24 * time.Hour)
			first := g.run()
			g.now = g.now.Add(48 * time.Hour)
			second := g.run()
			wantBusy := status != JobPartialSucceeded && status != JobResultUnknown
			if first.DeferredBusy != wantBusy || second.DeferredBusy != wantBusy {
				t.Fatal(first, second)
			}
			g.files(input, true)
			g.files(output, true)
			g.available(storeScope, input, true)
			g.files(staged, wantBusy)
		})
	}
}

func TestAssetGCUnknownItemRetainsReferencesWithoutDeferring(t *testing.T) {
	g := newAssetGCTest(t)
	input := g.asset(storeScope, "upload", 2)
	unused := g.asset(storeScope, "upload", 3)
	g.job(storeScope, "ij_partial", JobPartialSucceeded, []string{input.ID}, nil)
	if err := g.store.Update(storeContext(), func(tx *Transaction) error {
		j, err := tx.ReadJob(storeScope, "ij_partial")
		if err != nil {
			return err
		}
		j.Items[0].Status = JobResultUnknown
		j.Revision++
		if err := tx.PutJob(j); err != nil {
			return err
		}
		return tx.AppendEvent(ImageEvent{JobID: j.ID, Type: EventJobStateChanged})
	}); err != nil {
		t.Fatal(err)
	}
	if result := g.run(); result.DeferredBusy {
		t.Fatal(result)
	}
	g.now = g.now.Add(48 * time.Hour)
	if result := g.run(); result.DeferredBusy || result.RemovedFiles != 2 {
		t.Fatal(result)
	}
	g.files(input, true)
	g.files(unused, false)
}

func TestAssetGCExpiredUploadAndGeneratedParentRetention(t *testing.T) {
	g := newAssetGCTest(t)
	unused := g.asset(storeScope, "upload", 2)
	parent := g.asset(storeScope, "upload", 3)
	work := g.asset(storeScope, "generated", 4)
	work.ParentAssetIDs = []string{parent.ID}
	if err := g.store.Update(storeContext(), func(tx *Transaction) error { return tx.PutAsset(work) }); err != nil {
		t.Fatal(err)
	}
	if result := g.run(); result.MarkedAssets != 1 || result.RemovedFiles != 0 {
		t.Fatal(result)
	}
	g.reopen()
	g.now = g.now.Add(24 * time.Hour)
	if result := g.run(); result.RemovedFiles != 2 || result.UnavailableAssets != 1 {
		t.Fatal(result)
	}
	g.files(unused, false)
	g.files(parent, true)
	g.files(work, true)
	g.available(storeScope, parent, true)
}

func TestAssetGCCrashOrphansAndTemporaryFiles(t *testing.T) {
	g := newAssetGCTest(t)
	staged, err := g.assets.SaveOutput(context.Background(), storeScope, OutputSource{Kind: "bytes", MIME: "image/png", Data: imageTestBytes(t, "png", 2, 2)})
	if err != nil {
		t.Fatal(err)
	}
	temp := "tmp_" + strings.Repeat("a", 32)
	if err := g.assets.root.WriteFile(temp, []byte("interrupted upload"), 0600); err != nil {
		t.Fatal(err)
	}
	// These names and nested directories are never considered owned content.
	for _, name := range []string{"personal.txt", "tmp_notes", strings.Repeat("a", 64) + ".png"} {
		if err := g.assets.root.WriteFile(name, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.assets.root.Mkdir("workspace", 0700); err != nil {
		t.Fatal(err)
	}
	if err := g.assets.root.WriteFile("workspace/"+temp, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := g.assets.root.WriteFile(assetGCManifestTemp, []byte("interrupted GC write"), 0600); err != nil {
		t.Fatal(err)
	}
	if result := g.run(); result.MarkedFiles != 3 || result.RemovedFiles != 0 {
		t.Fatal(result)
	}
	g.reopen()
	g.now = g.now.Add(24 * time.Hour)
	if result := g.run(); result.RemovedFiles != 3 {
		t.Fatal(result)
	}
	g.files(staged, false)
	if _, err := g.assets.root.Lstat(temp); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	for _, name := range []string{"personal.txt", "tmp_notes", strings.Repeat("a", 64) + ".png", "workspace/" + temp} {
		if b, err := g.assets.root.ReadFile(name); err != nil || string(b) != "keep" {
			t.Fatalf("unowned file changed: %v", err)
		}
	}
}

func TestAssetGCWALFaultNeverDeletesContent(t *testing.T) {
	for _, boundary := range []string{"wal.before_append", "wal.after_append", "wal.before_sync", "wal.after_sync"} {
		t.Run(boundary, func(t *testing.T) {
			g := newAssetGCTest(t)
			a := g.asset(storeScope, "upload", 2)
			g.run()
			g.now = g.now.Add(24 * time.Hour)
			g.store.fault = func(point string) error {
				if point == boundary {
					return errors.New("sensitive/local/path")
				}
				return nil
			}
			_, err := g.assets.CollectGarbage(context.Background(), AssetGCOptions{Now: func() time.Time { return g.now }})
			if err == nil || strings.Contains(err.Error(), "sensitive") {
				t.Fatal("fault must fail without leaking path", err)
			}
			g.files(a, true)
			g.available(storeScope, a, true)
			g.reopen()
			g.available(storeScope, a, true)
			g.files(a, true)
			if result := g.run(); result.RemovedFiles != 2 || result.UnavailableAssets != 1 {
				t.Fatal(result)
			}
		})
	}
}

func TestAssetGCReferenceAddedAfterMarkCancelsSweep(t *testing.T) {
	g := newAssetGCTest(t)
	a := g.asset(storeScope, "upload", 2)
	g.run()
	g.job(storeScope, "ij_new_reference", JobSucceeded, []string{a.ID}, nil)
	g.now = g.now.Add(48 * time.Hour)
	if result := g.run(); result.RemovedFiles != 0 || result.UnavailableAssets != 0 {
		t.Fatal(result)
	}
	g.files(a, true)
	g.available(storeScope, a, true)
	g.reopen()
	g.run()
	g.files(a, true)
}

func TestAssetGCManifestCorruptionAndPathBoundary(t *testing.T) {
	g := newAssetGCTest(t)
	a := g.asset(storeScope, "upload", 2)
	g.run()
	g.now = g.now.Add(48 * time.Hour)
	manifest := newAssetGCManifest()
	manifest.Files["../personal-workspace/secret"] = assetGCFileMark{MarkedAt: g.now.Add(-48 * time.Hour)}
	b, err := encodeRecord(1, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.assets.root.WriteFile(assetGCManifestName, b, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = g.assets.CollectGarbage(context.Background(), AssetGCOptions{Now: func() time.Time { return g.now }})
	var imageErr *ImageError
	if !errors.As(err, &imageErr) || imageErr.Code != CodeStoreCorrupt || strings.Contains(err.Error(), "personal") {
		t.Fatal(err)
	}
	g.files(a, true)
	g.available(storeScope, a, true)
}

func TestAssetGCFileRefreshRestartsGraceAndClockRollback(t *testing.T) {
	g := newAssetGCTest(t)
	a, err := g.assets.SaveOutput(context.Background(), storeScope, OutputSource{Kind: "bytes", MIME: "image/png", Data: imageTestBytes(t, "png", 2, 2)})
	if err != nil {
		t.Fatal(err)
	}
	g.run()
	g.now = g.now.Add(-24 * time.Hour)
	if result := g.run(); result.RemovedFiles != 0 {
		t.Fatal(result)
	}
	g.now = g.now.Add(48 * time.Hour)
	for _, variant := range []string{"original", "thumbnail"} {
		path := filepath.Join(g.dir, "assets", a.Hash+"."+variant)
		if err := os.Chtimes(path, g.now.Add(-25*time.Hour), g.now.Add(-25*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if result := g.run(); result.RemovedFiles != 0 || result.MarkedFiles != 2 {
		t.Fatal(result)
	}
	g.files(a, true)
	g.now = g.now.Add(24 * time.Hour)
	g.run()
	g.files(a, false)
}

func TestAssetGCSymlinksCannotEscapeRoot(t *testing.T) {
	g := newAssetGCTest(t)
	outside := filepath.Join(g.dir, "personal-workspace")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(outside, "private-image")
	if err := os.WriteFile(sentinel, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	name := strings.Repeat("a", 64) + ".original"
	if err := os.Symlink(sentinel, filepath.Join(g.dir, "assets", name)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	g.run()
	g.now = g.now.Add(48 * time.Hour)
	g.run()
	if b, err := os.ReadFile(sentinel); err != nil || string(b) != "private" {
		t.Fatal("outside content changed", err)
	}
	if info, err := g.assets.root.Lstat(name); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("symlink should not be a GC candidate", err)
	}
}

func TestAssetGCInjectedRetentionAndCancellation(t *testing.T) {
	g := newAssetGCTest(t)
	a := g.asset(storeScope, "upload", 2)
	o := AssetGCOptions{Now: func() time.Time { return g.now }, UploadRetention: 9 * 24 * time.Hour, GracePeriod: time.Hour}
	if result, err := g.assets.CollectGarbage(context.Background(), o); err != nil || result.MarkedAssets != 0 {
		t.Fatal(result, err)
	}
	g.now = g.now.Add(24 * time.Hour)
	if result, err := g.assets.CollectGarbage(context.Background(), o); err != nil || result.MarkedAssets != 1 {
		t.Fatal(result, err)
	}
	g.now = g.now.Add(time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.assets.CollectGarbage(ctx, o); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	g.files(a, true)
	if _, err := g.assets.CollectGarbage(context.Background(), AssetGCOptions{GracePeriod: -time.Hour}); err == nil {
		t.Fatal("negative retention accepted")
	}
	if result, err := g.assets.CollectGarbage(context.Background(), o); err != nil || result.RemovedFiles != 2 {
		t.Fatal(result, err)
	}
}

func TestAssetGCManifestWriteFailureNeverDeletesContent(t *testing.T) {
	g := newAssetGCTest(t)
	a := g.asset(storeScope, "upload", 2)
	g.run()
	g.now = g.now.Add(48 * time.Hour)
	if err := g.assets.root.Mkdir(assetGCManifestTemp, 0700); err != nil {
		t.Fatal(err)
	}
	if err := g.assets.root.WriteFile(assetGCManifestTemp+"/sentinel", []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := g.assets.CollectGarbage(context.Background(), AssetGCOptions{Now: func() time.Time { return g.now }})
	if err == nil || strings.Contains(err.Error(), g.dir) || strings.Contains(err.Error(), assetGCManifestTemp) {
		t.Fatal("manifest failure must be sanitized", err)
	}
	g.files(a, true)
	g.available(storeScope, a, true)
	g.reopen()
	g.files(a, true)
	g.available(storeScope, a, true)
}

func TestAssetGCInvalidStoredHashFailsClosed(t *testing.T) {
	g := newAssetGCTest(t)
	a := g.asset(storeScope, "upload", 2)
	if err := g.store.Update(storeContext(), func(tx *Transaction) error {
		return tx.PutAsset(ImageAsset{ID: "ia_corrupt", Hash: "../personal/secret", Source: "upload", CreatedAt: g.now.Add(-100 * 24 * time.Hour), Available: true})
	}); err != nil {
		t.Fatal(err)
	}
	_, err := g.assets.CollectGarbage(context.Background(), AssetGCOptions{Now: func() time.Time { return g.now }})
	var imageErr *ImageError
	if !errors.As(err, &imageErr) || imageErr.Code != CodeStoreCorrupt || strings.Contains(err.Error(), "personal") {
		t.Fatal(err)
	}
	g.files(a, true)
}
