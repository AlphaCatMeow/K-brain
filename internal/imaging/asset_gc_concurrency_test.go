package imaging

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func requireGCNotFound(t *testing.T, err error) {
	t.Helper()
	var imageErr *ImageError
	if !errors.As(err, &imageErr) || imageErr.Code != CodeNotFound {
		t.Fatalf("want unavailable reference rejected as not found, got %v", err)
	}
}

// Each mutation is prepared from an available asset, before GC gets to sweep it.
func gcReferenceMutation(t *testing.T, g *assetGCTest, kind string, parent ImageAsset) func(*Transaction) error {
	t.Helper()
	p := g.job(storeScope, "ij_existing", JobSucceeded, nil, nil)
	child := g.asset(storeScope, "generated", 3)
	return func(tx *Transaction) error {
		switch kind {
		case "new job", "new input":
			j := ImageJob{ID: "ij_new", ProjectID: p.ID, Revision: 1, Status: JobSucceeded, CreatedAt: g.now}
			event := EventJobAccepted
			if kind == "new input" {
				var err error
				j, err = tx.ReadJob(storeScope, "ij_existing")
				if err != nil {
					return err
				}
				j.Revision++
				event = EventJobStateChanged
			}
			j.Request.Inputs = []InputAsset{{AssetID: parent.ID, Role: "reference"}}
			j.Request.ParentAssetID = parent.ID
			if err := tx.PutJob(j); err != nil {
				return err
			}
			return tx.AppendEvent(ImageEvent{JobID: j.ID, Type: event})
		case "new child":
			child.ID = "ia_new_child"
		}
		child.ParentAssetIDs = []string{parent.ID}
		return tx.PutAsset(child)
	}
}

func TestAssetGCRejectsReferencesPreparedBeforeSweep(t *testing.T) {
	for _, kind := range []string{"new job", "new input", "new child", "new parent"} {
		t.Run(kind, func(t *testing.T) {
			g := newAssetGCTest(t)
			parent := g.asset(storeScope, "upload", 2)
			mutate := gcReferenceMutation(t, g, kind, parent)
			g.run()
			g.now = g.now.Add(24 * time.Hour)
			if result := g.run(); result.UnavailableAssets != 1 || result.RemovedFiles != 2 {
				t.Fatal(result)
			}
			requireGCNotFound(t, g.store.Update(storeContext(), mutate))
			g.reopen()
			requireGCNotFound(t, g.store.Update(storeContext(), mutate))
			g.files(parent, false)
		})
	}
}

func TestAssetGCConcurrentReferenceAndSweep(t *testing.T) {
	for _, kind := range []string{"new job", "new parent"} {
		t.Run(kind, func(t *testing.T) {
			g := newAssetGCTest(t)
			parent := g.asset(storeScope, "upload", 2)
			mutate := gcReferenceMutation(t, g, kind, parent)
			g.run()
			g.now = g.now.Add(24 * time.Hour)
			start := make(chan struct{})
			committed := make(chan error, 1)
			type collection struct {
				result AssetGCResult
				err    error
			}
			collected := make(chan collection, 1)
			go func() {
				<-start
				committed <- g.store.Update(storeContext(), mutate)
			}()
			go func() {
				<-start
				r, err := g.assets.CollectGarbage(context.Background(), AssetGCOptions{Now: func() time.Time { return g.now }})
				collected <- collection{r, err}
			}()
			close(start)
			err, sweep := <-committed, <-collected
			if sweep.err != nil {
				t.Fatal(sweep.err)
			}
			if err == nil {
				if sweep.result.UnavailableAssets != 0 || sweep.result.RemovedFiles != 0 {
					t.Fatalf("committed reference lost its content: %+v", sweep.result)
				}
			} else {
				requireGCNotFound(t, err)
				if sweep.result.UnavailableAssets != 1 || sweep.result.RemovedFiles != 2 {
					t.Fatal(sweep.result)
				}
			}
			g.reopen()
			g.available(storeScope, parent, err == nil)
			g.files(parent, err == nil)
		})
	}
}

func TestAssetGCHistoricalUnavailableReferencesStillUpdateAndRecover(t *testing.T) {
	g := newAssetGCTest(t)
	parent := g.asset(storeScope, "upload", 2)
	child := g.asset(storeScope, "generated", 3)
	child.ParentAssetIDs = []string{parent.ID}
	if err := g.store.Update(storeContext(), func(tx *Transaction) error { return tx.PutAsset(child) }); err != nil {
		t.Fatal(err)
	}
	g.job(storeScope, "ij_history", JobRunning, []string{parent.ID}, []string{child.ID})
	// Missing content can be recorded by OpenAsset even for an existing live job.
	parent.Available = false
	child.Available = false
	if err := g.store.Update(storeContext(), func(tx *Transaction) error {
		if err := tx.PutAsset(parent); err != nil {
			return err
		}
		return tx.PutAsset(child)
	}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []JobStatus{JobFailed, JobCancelled} {
		g.reopen()
		if err := g.store.Update(storeContext(), func(tx *Transaction) error {
			j, err := tx.ReadJob(storeScope, "ij_history")
			if err != nil {
				return err
			}
			j.Revision++
			j.Status, j.Items[0].Status = status, status
			if err := tx.PutJob(j); err != nil {
				return err
			}
			if err := tx.PutAsset(child); err != nil {
				return err
			}
			return tx.AppendEvent(ImageEvent{JobID: j.ID, Type: EventJobFinished, Data: ImageEventData{Status: status}})
		}); err != nil {
			t.Fatalf("historical update after recovery: %v", err)
		}
		g.available(storeScope, parent, false)
		g.available(storeScope, child, false)
	}
}

func TestAssetGCImportHoldsLockThroughDurableCommit(t *testing.T) {
	g := newAssetGCTest(t)
	g.now = time.Now().UTC()
	old := g.asset(storeScope, "upload", 2)
	g.run()
	g.now = g.now.Add(24 * time.Hour)
	atSync, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	g.store.fault = func(point string) error {
		if point == "wal.before_sync" {
			once.Do(func() {
				close(atSync)
				<-release
			})
		}
		return nil
	}
	type imported struct {
		asset ImageAsset
		err   error
	}
	done := make(chan imported, 1)
	data := imageTestBytes(t, "png", 2, 2)
	go func() {
		a, err := g.assets.ImportAsset(context.Background(), storeScope, bytes.NewReader(data), "image/png")
		done <- imported{a, err}
	}()
	<-atSync
	locked := !g.assets.mu.TryLock()
	if !locked {
		g.assets.mu.Unlock()
	}
	gcDone := make(chan error, 1)
	go func() {
		_, err := g.assets.CollectGarbage(context.Background(), AssetGCOptions{Now: func() time.Time { return g.now }})
		gcDone <- err
	}()
	close(release)
	result := <-done
	if err := <-gcDone; err != nil {
		t.Fatal(err)
	}
	g.store.fault = nil
	if !locked {
		t.Fatal("import released the asset lock before its WAL commit")
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.asset.Hash != old.Hash {
		t.Fatal("expected same-hash content reuse")
	}
	g.reopen()
	g.available(storeScope, result.asset, true)
	g.files(result.asset, true)
}

func TestAssetGCSaveOutputBusyUntilAtomicReferenceCommit(t *testing.T) {
	g := newAssetGCTest(t)
	data := imageTestBytes(t, "png", 2, 2)
	source := OutputSource{Kind: "bytes", MIME: "image/png", Data: data}
	staged, err := g.assets.SaveOutput(context.Background(), storeScope, source)
	if err != nil {
		t.Fatal(err)
	}
	g.run() // Old orphan marks must not survive a durable busy job.
	g.now = g.now.Add(48 * time.Hour)
	g.job(storeScope, "ij_saving", JobSaving, nil, nil)
	staged, err = g.assets.SaveOutput(context.Background(), storeScope, source)
	if err != nil {
		t.Fatal(err)
	}
	if result := g.run(); !result.DeferredBusy || result.RemovedFiles != 0 {
		t.Fatal(result)
	}
	g.files(staged, true)
	staged.JobID = "ij_saving"
	if err := g.store.Update(storeContext(), func(tx *Transaction) error {
		if err := tx.PutAsset(staged); err != nil {
			return err
		}
		j, err := tx.ReadJob(storeScope, staged.JobID)
		if err != nil {
			return err
		}
		j.Revision++
		j.Status, j.Items[0].Status = JobSucceeded, JobSucceeded
		j.AssetIDs, j.Items[0].OutputAssetIDs = []string{staged.ID}, []string{staged.ID}
		if err := tx.PutJob(j); err != nil {
			return err
		}
		return tx.AppendEvent(ImageEvent{JobID: j.ID, Type: EventJobFinished, Data: ImageEventData{Status: JobSucceeded}})
	}); err != nil {
		t.Fatal(err)
	}
	g.reopen()
	g.now = g.now.Add(48 * time.Hour)
	if result := g.run(); result.DeferredBusy || result.RemovedFiles != 0 {
		t.Fatal(result)
	}
	g.files(staged, true)
	g.available(storeScope, staged, true)
}

func TestAssetGCCancelledWorkerLeasePinsLateOutputUntilCommit(t *testing.T) {
	for _, deadline := range []string{"expired", "zero", "future"} {
		t.Run(deadline, func(t *testing.T) {
			g := newAssetGCTest(t)
			source := OutputSource{Kind: "bytes", MIME: "image/png", Data: imageTestBytes(t, "png", 2, 2)}
			// Establish old orphan marks before the worker reuses this hash.
			if _, err := g.assets.SaveOutput(context.Background(), storeScope, source); err != nil {
				t.Fatal(err)
			}
			g.run()
			g.now = g.now.Add(48 * time.Hour)
			g.job(storeScope, "ij_cancelled", JobCancelled, nil, nil)
			expires := time.Time{}
			switch deadline {
			case "expired":
				expires = g.now.Add(-48 * time.Hour)
			case "future":
				expires = g.now.Add(48 * time.Hour)
			}
			if err := g.store.Update(storeContext(), func(tx *Transaction) error {
				return tx.PutExecution(ExecutionRecord{JobID: "ij_cancelled", Items: map[string]ItemExecution{
					"ij_cancelled_item": {LeaseOwner: "slow-worker", LeaseExpiresAt: expires},
				}})
			}); err != nil {
				t.Fatal(err)
			}
			// Durable lease survives reopening, even though public status is terminal.
			g.reopen()
			staged, err := g.assets.SaveOutput(context.Background(), storeScope, source)
			if err != nil {
				t.Fatal(err)
			}
			if result := g.run(); !result.DeferredBusy || result.RemovedFiles != 0 {
				t.Fatal(result)
			}
			g.files(staged, true)
			staged.JobID = "ij_cancelled"
			if err := g.store.Update(storeContext(), func(tx *Transaction) error {
				if err := tx.PutAsset(staged); err != nil {
					return err
				}
				j, err := tx.ReadJob(storeScope, staged.JobID)
				if err != nil {
					return err
				}
				j.Revision++
				j.AssetIDs = []string{staged.ID}
				j.Items[0].OutputAssetIDs = []string{staged.ID}
				if err := tx.PutJob(j); err != nil {
					return err
				}
				if err := tx.PutExecution(ExecutionRecord{JobID: j.ID, Items: map[string]ItemExecution{
					j.Items[0].ID: {LeaseExpiresAt: expires}, // Only ownership is cleared.
				}}); err != nil {
					return err
				}
				return tx.AppendEvent(ImageEvent{JobID: j.ID, Type: EventAssetReady, Data: ImageEventData{Asset: &staged}})
			}); err != nil {
				t.Fatal(err)
			}
			g.reopen()
			g.now = g.now.Add(48 * time.Hour)
			if result := g.run(); result.DeferredBusy || result.RemovedFiles != 0 {
				t.Fatal(result)
			}
			g.available(storeScope, staged, true)
			g.files(staged, true)
		})
	}
}

func TestAssetImportDirectoryBarrierPrecedesReferenceCommit(t *testing.T) {
	for _, failAt := range []int{1, 2} {
		t.Run(string(rune('0'+failAt)), func(t *testing.T) {
			g := newAssetGCTest(t)
			data := imageTestBytes(t, "png", 2, 2)
			failure := errors.New("directory persistence fault")
			barriers := 0
			g.assets.fault = func(point string) error {
				if point == "asset.before_directory_sync" {
					barriers++
					if barriers == failAt {
						return failure
					}
				}
				return nil
			}
			if _, err := g.assets.ImportAsset(context.Background(), storeScope, bytes.NewReader(data), "image/png"); !errors.Is(err, failure) {
				t.Fatal(err)
			}
			if len(g.store.data.Assets) != 0 {
				t.Fatal("reference committed before file directory barrier")
			}
			g.reopen()
			if len(g.store.data.Assets) != 0 {
				t.Fatal("failed import recovered as a committed asset")
			}
			barriers = 0
			g.assets.fault = func(point string) error {
				if point == "asset.before_directory_sync" {
					barriers++
				}
				return nil
			}
			a, err := g.assets.ImportAsset(context.Background(), storeScope, bytes.NewReader(data), "image/png")
			if err != nil {
				t.Fatal(err)
			}
			if barriers != 2 {
				t.Fatalf("same-content retry skipped directory barriers: %d", barriers)
			}
			g.reopen()
			g.available(storeScope, a, true)
			g.files(a, true)
		})
	}
}
