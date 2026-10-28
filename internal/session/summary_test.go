package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func summarySession(t *testing.T, store *Store, cwd string) string {
	t.Helper()
	id, err := store.Create(cwd, "model", "provider")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.update(id, func(data *sessionData) error {
		data.Messages[0] = ai.Message{Role: "user", Content: id}
		data.Meta.Title = id
		data.Meta.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestSummaryCacheAvoidsTranscriptReadsWhenFilesAreUnchanged(t *testing.T) {
	store := openPageStore(t)
	for range 8 {
		summarySession(t, store, t.TempDir())
	}
	reads := 0
	store.readObserver = func(string) { reads++ }
	page, err := store.ListPage(context.Background(), PageOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if reads != 8 {
		t.Fatalf("cold reads = %d, want 8", reads)
	}
	reads = 0
	if _, err := store.ListPage(context.Background(), PageOptions{Limit: 100}); err != nil {
		t.Fatal(err)
	}
	if reads != 0 {
		t.Fatalf("warm reads = %d, want 0", reads)
	}
	if store.summaries.scannerStop != nil {
		t.Fatal("ordinary session listing started the HTTP change scanner")
	}
	if err := store.SetTitle(page.Sessions[0].ID, "TUI-only write"); err != nil {
		t.Fatal(err)
	}
	if store.summaries.publishTimer != nil || store.summaries.scannerStop != nil {
		t.Fatal("TUI-only write started HTTP notification resources")
	}
}

func TestTUIStoreDoesNotStartSummaryBackgroundTasks(t *testing.T) {
	store, err := OpenProjectHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := summarySession(t, store, t.TempDir())
	if err := store.Save(id, 1, []ai.Message{{Role: "user", Content: "TUI save"}}, "model", "provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListPage(context.Background(), PageOptions{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTitle(id, "TUI rename"); err != nil {
		t.Fatal(err)
	}
	if store.summaries == nil {
		t.Fatal("list did not establish the lazy summary cache")
	}
	if store.summaries.scannerStop != nil || store.summaries.publishTimer != nil || store.summaries.waiters != 0 {
		t.Fatalf("TUI-only store started HTTP resources: %+v", store.summaries)
	}
	if len(store.summaries.changes) != 0 {
		t.Fatalf("TUI-only store retained HTTP changes: %+v", store.summaries.changes)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if store.summaries.scannerStop != nil || store.summaries.publishTimer != nil {
		t.Fatal("TUI-only store retained HTTP resources after close")
	}
}

func TestSummaryCacheReturnsClonesAndTracksWrites(t *testing.T) {
	store := openPageStore(t)
	id := summarySession(t, store, t.TempDir())
	if err := store.update(id, func(data *sessionData) error {
		data.Meta.SubUsage = map[string]ai.Usage{"child": {PromptTokens: 7}}
		data.Meta.ModelUsage = map[string]ai.Usage{"model": {CompletionTokens: 11}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListPage(context.Background(), PageOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	page.Sessions[0].Tags = []string{"mutated"}
	page.Sessions[0].SubUsage = map[string]ai.Usage{"mutated": {PromptTokens: 1}}
	before := page.Revision
	if err := store.SetTitle(id, "new title"); err != nil {
		t.Fatal(err)
	}
	page, err = store.ListPage(context.Background(), PageOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Revision <= before || page.Sessions[0].Title != "new title" {
		t.Fatalf("updated page = %+v", page)
	}
	if len(page.Sessions[0].Tags) != 0 || page.Sessions[0].SubUsage["child"].PromptTokens != 7 || page.Sessions[0].ModelUsage["model"].CompletionTokens != 11 {
		t.Fatalf("caller polluted cache: %+v", page.Sessions[0])
	}
}

func TestSummaryCacheCoversEveryWriteEntryPoint(t *testing.T) {
	store := openPageStore(t)
	id := summarySession(t, store, t.TempDir())
	baseline, err := store.SummarySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertWarm := func(label string) Page {
		t.Helper()
		reads := 0
		store.readObserver = func(string) { reads++ }
		page, err := store.ListPage(context.Background(), PageOptions{Limit: 100})
		store.readObserver = nil
		if err != nil {
			t.Fatalf("%s list: %v", label, err)
		}
		if reads != 0 {
			t.Fatalf("%s caused %d transcript rereads", label, reads)
		}
		return page
	}

	if err := store.SetSnapshot(id, 1, "checkpoint"); err != nil {
		t.Fatal(err)
	}
	if page := assertWarm("checkpoint"); page.Revision != baseline.Cursor.Revision {
		t.Fatal("non-projected checkpoint changed list revision")
	}
	if err := store.SaveTask(id, Task{ID: "task", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if page := assertWarm("task"); page.Revision != baseline.Cursor.Revision {
		t.Fatal("non-projected task changed list revision")
	}
	if err := store.SetTitle(id, "metadata title"); err != nil {
		t.Fatal(err)
	}
	metadataPage := assertWarm("metadata")
	if metadataPage.Revision <= baseline.Cursor.Revision || metadataPage.Sessions[0].Title != "metadata title" {
		t.Fatalf("metadata page=%+v", metadataPage)
	}
	newCWD := t.TempDir()
	if err := store.SetCWD(id, newCWD); err != nil {
		t.Fatal(err)
	}
	movePage := assertWarm("project-scoped move")
	if movePage.Sessions[0].CWD != newCWD {
		t.Fatalf("move page=%+v", movePage)
	}
	created, err := store.Create(t.TempDir(), "model", "provider")
	if err != nil {
		t.Fatal(err)
	}
	assertWarm("create")
	if err := store.Save(created, 0, []ai.Message{{Role: "user", Content: "created message"}}, "model", "provider"); err != nil {
		t.Fatal(err)
	}
	if page := assertWarm("save"); page.Total != 2 {
		t.Fatalf("save page=%+v", page)
	}
	imported, err := store.ImportHistory(
		"imported-session", "source", "fingerprint", "content", "{}", t.TempDir(),
		"model", "provider", "imported", time.Now().UTC(), time.Now().UTC(), false, false,
		false, "", []ai.Message{{Role: "user", Content: "imported message"}}, nil,
	)
	if err != nil || imported {
		t.Fatalf("import existing=%v err=%v", imported, err)
	}
	if page := assertWarm("import"); page.Total != 3 {
		t.Fatalf("import page=%+v", page)
	}
	if err := store.Delete("imported-session"); err != nil {
		t.Fatal(err)
	}
	if page := assertWarm("delete"); page.Total != 2 {
		t.Fatalf("delete page=%+v", page)
	}
}

func TestSummaryCacheDetectsExternalWriteAndDelete(t *testing.T) {
	home := t.TempDir()
	first, err := OpenProjectHome(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	id := summarySession(t, first, t.TempDir())
	page, err := first.ListPage(context.Background(), PageOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	second, err := OpenProjectHome(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if err := second.SetTitle(id, "external title with size change"); err != nil {
		t.Fatal(err)
	}
	updated, err := first.ListPage(context.Background(), PageOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision <= page.Revision || updated.Sessions[0].Title != "external title with size change" {
		t.Fatalf("external update missed: %+v", updated)
	}
	if err := second.Delete(id); err != nil {
		t.Fatal(err)
	}
	deleted, err := first.ListPage(context.Background(), PageOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Total != 0 || deleted.Revision <= updated.Revision {
		t.Fatalf("external delete missed: %+v", deleted)
	}
}

func TestSummaryScanFailureDoesNotPublishPartialState(t *testing.T) {
	store := openPageStore(t)
	id := summarySession(t, store, t.TempDir())
	page, err := store.ListPage(context.Background(), PageOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.TranscriptPath(id), []byte("broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListPage(context.Background(), PageOptions{Limit: 10}); err == nil {
		t.Fatal("corrupt transcript was hidden")
	}
	if store.summaries.revision != page.Revision || len(store.summaries.entries) != 1 {
		t.Fatalf("failed scan mutated cache: %+v", store.summaries)
	}
}

func TestSummaryCursorRepairsDirtyCacheOrReturnsError(t *testing.T) {
	store := openPageStore(t)
	id := summarySession(t, store, t.TempDir())
	snapshot, err := store.SummarySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	store.summaries.dirty = true
	if err := os.WriteFile(store.TranscriptPath(id), []byte("broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SummaryCursor(context.Background()); err == nil {
		t.Fatal("dirty cache returned a confirmation cursor after repair failed")
	}
	if store.summaries.revision != snapshot.Cursor.Revision || len(store.summaries.entries) != 1 {
		t.Fatal("failed dirty-cache repair published partial state")
	}
}

func TestSummaryColdReadRetriesAnUnstableExternalFile(t *testing.T) {
	store := openPageStore(t)
	id := summarySession(t, store, t.TempDir())
	changed := false
	reads := 0
	store.readObserver = func(observed string) {
		if observed != id {
			return
		}
		reads++
		if changed {
			return
		}
		changed = true
		path := store.TranscriptPath(id)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Errorf("read fixture: %v", err)
			return
		}
		raw = bytes.Replace(raw, []byte(`"title":"`+id+`"`), []byte(`"title":"externally changed while reading"`), 1)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Errorf("replace fixture: %v", err)
		}
	}
	page, err := store.ListPage(context.Background(), PageOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if reads != 2 || page.Sessions[0].Title != "externally changed while reading" {
		t.Fatalf("reads=%d page=%+v", reads, page)
	}
}

func TestSummaryChangesCoalesceCursorAndReset(t *testing.T) {
	store := openPageStore(t)
	store.summaryCoalesce = 20 * time.Millisecond
	id := summarySession(t, store, t.TempDir())
	baseline, err := store.SummarySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetTitle(id, "one"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetTitle(id, "two"); err != nil {
		t.Fatal(err)
	}
	result, err := store.SummaryChanges(context.Background(), SummaryChangeRequest{Epoch: baseline.Cursor.Epoch, Since: baseline.Cursor.Revision, Wait: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) != 1 || result.Changes[0].Meta.Title != "two" || result.NextSince != result.Changes[0].Revision {
		t.Fatalf("changes = %+v", result)
	}
	reset, err := store.SummaryChanges(context.Background(), SummaryChangeRequest{Epoch: "wrong", Since: 0})
	if err != nil || !reset.Reset || reset.NextSince != 0 {
		t.Fatalf("epoch reset = %+v, %v", reset, err)
	}
}

func TestSummaryChangesCoalescesWaiterWakeups(t *testing.T) {
	store := openPageStore(t)
	store.summaryCoalesce = 25 * time.Millisecond
	id := summarySession(t, store, t.TempDir())
	baseline, err := store.SummarySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	iterations := 0
	store.summaryWaitObserver = func() { iterations++ }
	resultCh := make(chan SummaryChangeResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := store.SummaryChanges(context.Background(), SummaryChangeRequest{Epoch: baseline.Cursor.Epoch, Since: baseline.Cursor.Revision, Wait: time.Second})
		resultCh <- result
		errCh <- err
	}()
	time.Sleep(5 * time.Millisecond)
	for index := range 50 {
		if err := store.SetTitle(id, fmt.Sprintf("coalesced-%d", index)); err != nil {
			t.Fatal(err)
		}
	}
	result := <-resultCh
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	if iterations > 3 {
		t.Fatalf("50 writes woke waiter %d times", iterations)
	}
	if len(result.Changes) != 1 || result.Changes[0].Meta.Title != "coalesced-49" {
		t.Fatalf("coalesced changes=%+v", result)
	}
}

func TestSummaryChangesCancellationReleasesWaiter(t *testing.T) {
	store := openPageStore(t)
	baseline, err := store.SummarySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = store.SummaryChanges(ctx, SummaryChangeRequest{Epoch: baseline.Cursor.Epoch, Since: baseline.Cursor.Revision, Wait: time.Second})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result = %v", err)
	}
	if store.summaries.waiters != 0 {
		t.Fatalf("waiters leaked: %d", store.summaries.waiters)
	}
}

func TestSummaryChangesCaughtUpPollDoesNotSpin(t *testing.T) {
	store := openPageStore(t)
	snapshot, err := store.SummarySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	store.summaries.nextPublish = time.Now().Add(-time.Second)
	iterations := 0
	store.summaryWaitObserver = func() { iterations++ }
	started := time.Now()
	result, err := store.SummaryChanges(context.Background(), SummaryChangeRequest{Epoch: snapshot.Cursor.Epoch, Since: snapshot.Cursor.Revision, Wait: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 20*time.Millisecond {
		t.Fatalf("caught-up long poll returned too early: %v", elapsed)
	}
	if iterations > 3 {
		t.Fatalf("caught-up long poll spun %d times", iterations)
	}
	if result.NextSince != snapshot.Cursor.Revision || len(result.Changes) != 0 {
		t.Fatalf("caught-up result = %+v", result)
	}
}

func TestSummaryTombstonePruningRaisesFloor(t *testing.T) {
	store := openPageStore(t)
	id := summarySession(t, store, t.TempDir())
	baseline, err := store.SummarySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(id); err != nil {
		t.Fatal(err)
	}
	change := store.summaries.changes[id]
	change.created = time.Now().Add(-summaryTombstoneTTL - time.Second)
	store.summaries.changes[id] = change
	store.pruneSummaryChangesLocked(time.Now())
	if store.summaries.minSince != change.revision {
		t.Fatalf("minSince = %d, want %d", store.summaries.minSince, change.revision)
	}
	result, err := store.SummaryChanges(context.Background(), SummaryChangeRequest{Epoch: baseline.Cursor.Epoch, Since: baseline.Cursor.Revision})
	if err != nil || !result.Reset || result.ResetReason != "expired" {
		t.Fatalf("expired cursor = %+v, %v", result, err)
	}
}

func TestSummaryChangesFirstInitializesWithoutSyntheticChanges(t *testing.T) {
	store := openPageStore(t)
	if store.summaries != nil {
		t.Fatal("opening a store initialized summaries")
	}
	summarySession(t, store, t.TempDir())
	if store.summaries != nil {
		t.Fatal("TUI-style writes initialized HTTP summaries")
	}
	result, err := store.SummaryChanges(context.Background(), SummaryChangeRequest{Epoch: "", Since: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reset || result.ResetReason != "epoch" || result.Cursor.Revision != 0 || len(result.Changes) != 0 {
		t.Fatalf("changes-first = %+v", result)
	}
	result, err = store.SummaryChanges(context.Background(), SummaryChangeRequest{Epoch: result.Cursor.Epoch, Since: 0})
	if err != nil || result.Reset || result.NextSince != 0 || len(result.Changes) != 0 {
		t.Fatalf("initialized changes = %+v, %v", result, err)
	}
}

func TestSummaryChangesBatchCursorNeverJumpsToHead(t *testing.T) {
	store := openPageStore(t)
	if _, err := store.SummarySnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.summaries.changes = make(map[string]summaryChangeEntry)
	for revision := uint64(1); revision <= maxSummaryChanges+101; revision++ {
		id := fmt.Sprintf("s-%04d", revision)
		entry := summaryEntry{meta: Meta{ID: id, UpdatedAt: time.Now().UTC()}, count: 1}
		store.summaries.changes[id] = summaryChangeEntry{revision: revision, kind: "upsert", listed: true, entry: entry, created: time.Now()}
		store.summaries.revision = revision
	}
	first, ready := store.summaryChangesLocked(SummaryChangeRequest{Epoch: store.summaries.epoch, Since: 0}, true)
	if !ready || !first.More || len(first.Changes) != maxSummaryChanges || first.NextSince != maxSummaryChanges || first.Cursor.Revision != maxSummaryChanges+101 {
		t.Fatalf("first batch = %+v", first)
	}
	second, ready := store.summaryChangesLocked(SummaryChangeRequest{Epoch: store.summaries.epoch, Since: first.NextSince}, true)
	if !ready || second.More || len(second.Changes) != 101 || second.NextSince != maxSummaryChanges+101 {
		t.Fatalf("second batch = %+v", second)
	}
}

func TestSummaryChangesFutureCursorResets(t *testing.T) {
	store := openPageStore(t)
	snapshot, err := store.SummarySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.SummaryChanges(context.Background(), SummaryChangeRequest{Epoch: snapshot.Cursor.Epoch, Since: snapshot.Cursor.Revision + 1})
	if err != nil || !result.Reset || result.ResetReason != "future" {
		t.Fatalf("future cursor = %+v, %v", result, err)
	}
}

func TestSummaryRevisionOverflowStartsNewEpochWithoutWrap(t *testing.T) {
	store := openPageStore(t)
	id := summarySession(t, store, t.TempDir())
	snapshot, err := store.SummarySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	store.summaries.revision = math.MaxUint64
	if err := store.SetTitle(id, "after overflow"); err != nil {
		t.Fatal(err)
	}
	if store.summaries.epoch == snapshot.Cursor.Epoch || store.summaries.revision != 1 || store.summaries.minSince != 1 || len(store.summaries.changes) != 0 {
		t.Fatalf("overflow state = %+v", store.summaries)
	}
	reset, err := store.SummaryChanges(context.Background(), SummaryChangeRequest{Epoch: snapshot.Cursor.Epoch, Since: snapshot.Cursor.Revision})
	if err != nil || !reset.Reset || reset.ResetReason != "epoch" {
		t.Fatalf("old epoch after overflow = %+v, %v", reset, err)
	}
}

func TestSummaryChangesLimitsWaitersAndRescans(t *testing.T) {
	store := openPageStore(t)
	snapshot, err := store.SummarySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, maxSummaryWaiters)
	for range maxSummaryWaiters {
		go func() {
			_, err := store.SummaryChanges(ctx, SummaryChangeRequest{Epoch: snapshot.Cursor.Epoch, Since: snapshot.Cursor.Revision, Wait: time.Second})
			results <- err
		}()
	}
	deadline := time.Now().Add(time.Second)
	for {
		store.mu.Lock()
		waiters := store.summaries.waiters
		store.mu.Unlock()
		if waiters == maxSummaryWaiters {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d waiters registered", waiters)
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := store.SummaryChanges(context.Background(), SummaryChangeRequest{Epoch: snapshot.Cursor.Epoch, Since: snapshot.Cursor.Revision, Wait: time.Second}); !errors.Is(err, ErrTooManySummaryWaiters) {
		t.Fatalf("extra waiter error=%v", err)
	}
	cancel()
	for range maxSummaryWaiters {
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter result=%v", err)
		}
	}
	if _, err := store.SummaryChanges(context.Background(), SummaryChangeRequest{Epoch: snapshot.Cursor.Epoch, Since: snapshot.Cursor.Revision, Rescan: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SummaryChanges(context.Background(), SummaryChangeRequest{Epoch: snapshot.Cursor.Epoch, Since: snapshot.Cursor.Revision, Rescan: true}); !errors.Is(err, ErrSummaryRescanLimited) {
		t.Fatalf("repeated rescan error=%v", err)
	}
}

func TestSummaryConcurrentListAndWrites(t *testing.T) {
	store := openPageStore(t)
	id := summarySession(t, store, t.TempDir())
	if _, err := store.SummarySnapshot(context.Background()); err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 2)
	go func() {
		for index := range 100 {
			if err := store.SetTitle(id, fmt.Sprintf("title-%d", index)); err != nil {
				errCh <- err
				return
			}
		}
		errCh <- nil
	}()
	go func() {
		for range 100 {
			if _, err := store.ListPage(context.Background(), PageOptions{Limit: 10}); err != nil {
				errCh <- err
				return
			}
		}
		errCh <- nil
	}()
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
}

func TestSummaryScannerStopsAfterLease(t *testing.T) {
	store := openPageStore(t)
	store.summaryScanInterval = 5 * time.Millisecond
	store.summaryScanLease = 15 * time.Millisecond
	snapshot, err := store.SummarySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SummaryChanges(context.Background(), SummaryChangeRequest{Epoch: snapshot.Cursor.Epoch, Since: snapshot.Cursor.Revision}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(40 * time.Millisecond)
	store.mu.Lock()
	running := store.summaries.scannerStop != nil
	store.mu.Unlock()
	if running {
		t.Fatal("summary scanner outlived its lease")
	}
}

func TestSummarySubscriptionResourcesAreReleased(t *testing.T) {
	store := openPageStore(t)
	store.summaryScanInterval = 5 * time.Millisecond
	store.summaryScanLease = 20 * time.Millisecond
	store.summarySnapshotLease = 20 * time.Millisecond
	snapshot, err := store.SummarySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := store.SummaryChanges(ctx, SummaryChangeRequest{Epoch: snapshot.Cursor.Epoch, Since: snapshot.Cursor.Revision, Wait: time.Second})
		result <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		store.mu.Lock()
		registered := store.summaries.waiters == 1
		done := store.summaries.scannerDone
		store.mu.Unlock()
		if registered && done != nil {
			cancel()
			if err := <-result; !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled waiter = %v", err)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("summary scanner did not exit after its lease")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("summary waiter did not register")
		}
		time.Sleep(time.Millisecond)
	}
	deadline = time.Now().Add(time.Second)
	var waiters, changes int
	var scanner chan struct{}
	var timer, journalTimer *time.Timer
	for {
		store.mu.Lock()
		waiters = store.summaries.waiters
		scanner = store.summaries.scannerStop
		timer = store.summaries.publishTimer
		journalTimer = store.summaries.journalTimer
		changes = len(store.summaries.changes)
		store.mu.Unlock()
		if journalTimer == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if waiters != 0 || scanner != nil || timer != nil || journalTimer != nil || changes != 0 {
		t.Fatalf("HTTP summary resources leaked: waiters=%d scanner=%v publishTimer=%v journalTimer=%v changes=%d", waiters, scanner != nil, timer != nil, journalTimer != nil, changes)
	}
}

func TestSummaryScannerFailureWakesLongPoll(t *testing.T) {
	store := openPageStore(t)
	store.summaryScanInterval = 5 * time.Millisecond
	store.summaryScanLease = time.Second
	id := summarySession(t, store, t.TempDir())
	snapshot, err := store.SummarySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := store.SummaryChanges(context.Background(), SummaryChangeRequest{Epoch: snapshot.Cursor.Epoch, Since: snapshot.Cursor.Revision, Wait: time.Second})
		result <- err
	}()
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(store.TranscriptPath(id), []byte("broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("scanner failure was returned as an empty change batch")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("scanner failure did not wake long poll")
	}
}

func benchmarkSummaryStore(b *testing.B, messageBytes int) (*Store, []string) {
	b.Helper()
	store, err := OpenProjectHome(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	ids := make([]string, 0, 248)
	content := strings.Repeat("x", messageBytes)
	for index := range 248 {
		id, err := store.Create(fmt.Sprintf("C:/fixture/project-%03d", index%8), "model", "provider")
		if err != nil {
			b.Fatal(err)
		}
		if err := store.update(id, func(data *sessionData) error {
			data.Messages[0] = ai.Message{Role: "user", Content: content}
			data.Meta.Title = fmt.Sprintf("session-%03d", index)
			data.Meta.UpdatedAt = time.Unix(int64(index+1), 0).UTC()
			return nil
		}); err != nil {
			b.Fatal(err)
		}
		ids = append(ids, id)
	}
	return store, ids
}

func BenchmarkSummaryListCold(b *testing.B) {
	store, _ := benchmarkSummaryStore(b, 32<<10)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		store.mu.Lock()
		store.summaries = nil
		store.mu.Unlock()
		if _, err := store.ListPage(context.Background(), PageOptions{Limit: 1000}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSummaryListWarm(b *testing.B) {
	store, _ := benchmarkSummaryStore(b, 32<<10)
	if _, err := store.ListPage(context.Background(), PageOptions{Limit: 1000}); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := store.ListPage(context.Background(), PageOptions{Limit: 1000}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSummaryMutation(b *testing.B) {
	store, ids := benchmarkSummaryStore(b, 32<<10)
	if _, err := store.SummarySnapshot(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := range b.N {
		if err := store.SetTitle(ids[index%len(ids)], fmt.Sprintf("mutation-%d", index)); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSummaryChanges(b *testing.B) {
	store, ids := benchmarkSummaryStore(b, 128)
	snapshot, err := store.SummarySnapshot(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	since := snapshot.Cursor.Revision
	b.ReportAllocs()
	b.ResetTimer()
	for index := range b.N {
		if err := store.SetTitle(ids[index%len(ids)], fmt.Sprintf("change-%d", index)); err != nil {
			b.Fatal(err)
		}
		result, err := store.SummaryChanges(context.Background(), SummaryChangeRequest{Epoch: snapshot.Cursor.Epoch, Since: since})
		if err != nil {
			b.Fatal(err)
		}
		since = result.NextSince
	}
}
