package imaging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var storeScope = Scope{OwnerID: "owner", BackendID: "backend"}

func storeContext() context.Context { return WithScope(context.Background(), storeScope) }
func mustStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, e := OpenStore(dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func seedStore(t *testing.T, s *Store) {
	t.Helper()
	e := s.Update(storeContext(), func(tx *Transaction) error {
		if e := tx.PutProject(ImageProject{ID: "ip_one", Title: "one", Revision: 1, CreatedAt: time.Unix(1, 0)}); e != nil {
			return e
		}
		if e := tx.PutJob(ImageJob{ID: "ij_one", ProjectID: "ip_one", Revision: 1, Status: JobQueued, Request: CreateJobRequest{Parameters: map[string]any{"nested": map[string]any{"v": "original"}}}, CreatedAt: time.Unix(2, 0)}); e != nil {
			return e
		}
		if e := tx.AppendEvent(ImageEvent{JobID: "ij_one", Type: EventJobAccepted}); e != nil {
			return e
		}
		return tx.PutIdempotency(storeScope, "key", "hash", "ij_one")
	})
	if e != nil {
		t.Fatal(e)
	}
}
func TestStoreAtomicReopenAndIsolation(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	seedStore(t, s)
	if e := s.Update(context.Background(), func(*Transaction) error { return nil }); e == nil {
		t.Fatal("missing scope accepted")
	}
	if e := s.Update(storeContext(), func(tx *Transaction) error {
		j, e := tx.ReadJob(storeScope, "ij_one")
		if e != nil {
			return e
		}
		j.Request.Parameters["nested"].(map[string]any)["v"] = "changed"
		if e = tx.PutJob(j); e != nil {
			return e
		}
		return errors.New("rollback")
	}); e == nil {
		t.Fatal("rollback succeeded")
	}
	j, e := s.ReadJob(storeScope, "ij_one")
	if e != nil || j.Request.Parameters["nested"].(map[string]any)["v"] != "original" {
		t.Fatalf("rollback polluted state: %+v %v", j, e)
	}
	j.Request.Parameters["nested"].(map[string]any)["v"] = "outside"
	s.Close()
	s = mustStore(t, dir)
	j, e = s.ReadJob(storeScope, "ij_one")
	if e != nil || j.Request.Parameters["nested"].(map[string]any)["v"] != "original" {
		t.Fatal("read escaped", e)
	}
	ev, e := s.ReadEvents(context.Background(), storeScope, "ij_one", 0, 100)
	if e != nil || len(ev) != 1 || ev[0].Seq != 1 || ev[0].Revision != j.Revision {
		t.Fatalf("events %+v %v", ev, e)
	}
	if r, ok := s.GetIdempotency(storeScope, "key"); !ok || r.JobID != "ij_one" {
		t.Fatal("lost idempotency")
	}
	for _, scope := range []Scope{{OwnerID: "other", BackendID: "backend"}, {OwnerID: "owner", BackendID: "other"}} {
		if _, e = s.ReadJob(scope, "ij_one"); e == nil {
			t.Fatal("scope leak")
		}
		if e = s.Update(WithScope(context.Background(), scope), func(tx *Transaction) error { return tx.PutProject(ImageProject{ID: "ip_one", Revision: 2}) }); e == nil {
			t.Fatal("scope overwrite")
		}
	}
}
func TestStoreRejectsSecondWriter(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	other, e := OpenStore(dir)
	if e == nil {
		other.Close()
		t.Fatal("second writer accepted")
	}
	if !strings.Contains(e.Error(), "IMAGE_STORE_LOCKED") {
		t.Fatal(e)
	}
	s.Close()
	mustStore(t, dir)
}
func TestStoreSyncFailure(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	s.fault = func(p string) error {
		if p == "wal.before_sync" {
			return errors.New("sync failed")
		}
		return nil
	}
	if e := s.Update(storeContext(), func(tx *Transaction) error { return tx.PutProject(ImageProject{ID: "ip_failed", Revision: 1}) }); e == nil {
		t.Fatal("sync failure hidden")
	}
	if _, e := s.ReadProject(storeScope, "ip_failed"); e == nil {
		t.Fatal("failed tx published")
	}
	s.Close()
	s = mustStore(t, dir)
	if _, e := s.ReadProject(storeScope, "ip_failed"); e == nil {
		t.Fatal("failed tx replayed")
	}
}
func TestRecoveryTailAndChecksum(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	seedStore(t, s)
	s.Close()
	path := filepath.Join(dir, "transactions.wal")
	f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	f.WriteString(`{"txSeq":2`)
	f.Close()
	s = mustStore(t, dir)
	if _, e = s.ReadJob(storeScope, "ij_one"); e != nil {
		t.Fatal(e)
	}
	s.Close()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	b = []byte(strings.Replace(string(b), "one", "two", 1))
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	bad, e := OpenStore(dir)
	if e == nil {
		bad.Close()
		t.Fatal("corruption accepted")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(b) {
		t.Fatal("corruption silently altered")
	}
}
func TestSnapshotRecovery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "图片 存储")
	s := mustStore(t, dir)
	seedStore(t, s)
	for _, point := range []string{"snapshot.before_rename", "snapshot.after_rename", "wal.before_rotate", "wal.after_rotate"} {
		s.fault = func(p string) error {
			if p == point {
				return errors.New("interrupted")
			}
			return nil
		}
		if e := s.Checkpoint(); e == nil {
			t.Fatalf("fault not reached %s", point)
		}
		s.Close()
		s = mustStore(t, dir)
		if _, e := s.ReadJob(storeScope, "ij_one"); e != nil {
			t.Fatalf("%s: %v", point, e)
		}
		ev, e := s.ReadEvents(context.Background(), storeScope, "ij_one", 0, 100)
		if e != nil || len(ev) != 1 {
			t.Fatalf("duplicate/lost event %s: %v", point, e)
		}
	}
}
func TestIdempotencyConcurrent(t *testing.T) {
	s := mustStore(t, t.TempDir())
	seedStore(t, s)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := s.Update(storeContext(), func(tx *Transaction) error {
				r, ok := tx.GetIdempotency(storeScope, "key")
				if !ok || r.JobID != "ij_one" {
					return errors.New("lost key")
				}
				return tx.PutIdempotency(storeScope, "key", "hash", "ij_one")
			}); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	if e := s.Update(storeContext(), func(tx *Transaction) error { return tx.PutIdempotency(storeScope, "key", "different", "ij_one") }); e == nil {
		t.Fatal("conflict accepted")
	}
	page, e := s.ListJobs(context.Background(), storeScope, "ip_one", PageRequest{Limit: 100})
	if e != nil || len(page.Items) != 1 {
		t.Fatal("job count", e)
	}
	ev, e := s.ReadEvents(context.Background(), storeScope, "ij_one", 0, 100)
	if e != nil || len(ev) != 1 {
		t.Fatal("accepted count", e)
	}
}
