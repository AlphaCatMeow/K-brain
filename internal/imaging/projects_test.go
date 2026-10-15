package imaging

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProjectCRUDDeleteTombstoneAndReferences(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := mustStore(t, dir)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return base }
	p, e := s.CreateProject(ctx, storeScope, "draft", "arbitrary/workspace/string")
	if e != nil {
		t.Fatal(e)
	}
	if p.Revision != 1 || p.WorkspaceRef != "arbitrary/workspace/string" {
		t.Fatal(p)
	}
	p, e = s.UpdateProject(ctx, storeScope, p.ID, "renamed", 1)
	if e != nil || p.Revision != 2 {
		t.Fatal(p, e)
	}
	if _, e = s.UpdateProject(ctx, storeScope, p.ID, "stale", 1); e == nil {
		t.Fatal("stale patch accepted")
	}
	p2, e := s.CreateProject(ctx, storeScope, "keep", "")
	if e != nil {
		t.Fatal(e)
	}
	e = s.Update(storeContext(), func(tx *Transaction) error {
		if e := tx.PutAsset(ImageAsset{ID: "ia_parent", Hash: "content", Source: "upload", Available: true}); e != nil {
			return e
		}
		for _, pair := range [][2]string{{"ij_delete", p.ID}, {"ij_keep", p2.ID}} {
			if e := tx.PutJob(ImageJob{ID: pair[0], ProjectID: pair[1], Revision: 1, Status: JobQueued, Request: CreateJobRequest{Inputs: []InputAsset{{AssetID: "ia_parent", Role: "source"}}, ParentAssetID: "ia_parent"}}); e != nil {
				return e
			}
			if e := tx.AppendEvent(ImageEvent{JobID: pair[0], Type: EventJobAccepted}); e != nil {
				return e
			}
		}
		return tx.PutIdempotency(storeScope, "deleted-key", "hash", "ij_delete")
	})
	if e != nil {
		t.Fatal(e)
	}
	e = s.DeleteProject(ctx, storeScope, p.ID, 2)
	var ie *ImageError
	if !errors.As(e, &ie) || ie.Code != CodeProjectBusy {
		t.Fatal("busy deletion allowed", e)
	}
	e = s.Update(storeContext(), func(tx *Transaction) error {
		j, e := tx.ReadJob(storeScope, "ij_delete")
		if e != nil {
			return e
		}
		j.Revision++
		j.Status = JobSucceeded
		if e = tx.PutJob(j); e != nil {
			return e
		}
		return tx.AppendEvent(ImageEvent{JobID: j.ID, Type: EventJobFinished, Data: ImageEventData{Status: JobSucceeded}})
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.DeleteProject(ctx, storeScope, p.ID, 2); e != nil {
		t.Fatal(e)
	}
	if e = s.DeleteProject(ctx, storeScope, p.ID, 2); e != nil {
		t.Fatal("delete not idempotent", e)
	}
	if _, e = s.ReadProject(storeScope, p.ID); e == nil {
		t.Fatal("deleted project visible")
	}
	if _, e = s.ReadJob(storeScope, "ij_delete"); e == nil {
		t.Fatal("deleted history visible")
	}
	if _, e = s.ReadAsset(storeScope, "ia_parent"); e != nil {
		t.Fatal("shared asset removed", e)
	}
	if s.AssetReferenceCounts(storeScope)["ia_parent"] == 0 {
		t.Fatal("shared reference lost")
	}
	r, ok := s.GetIdempotency(storeScope, "deleted-key")
	if !ok || !r.ExpiresAt.Equal(base.Add(30*24*time.Hour)) {
		t.Fatal("missing tombstone", r)
	}
	s.Close()
	s = mustStore(t, dir)
	s.now = func() time.Time { return base.Add(29 * 24 * time.Hour) }
	if _, ok = s.GetIdempotency(storeScope, "deleted-key"); !ok {
		t.Fatal("tombstone lost on reopen")
	}
	s.now = func() time.Time { return base.Add(30 * 24 * time.Hour) }
	if _, ok = s.GetIdempotency(storeScope, "deleted-key"); ok {
		t.Fatal("expired tombstone still live")
	}
	page, e := s.ListProjects(ctx, storeScope, PageRequest{Limit: 100})
	if e != nil || len(page.Items) != 1 || page.Items[0].ID != p2.ID {
		t.Fatal(page, e)
	}
}
func TestProjectParentAssetScopeAndCycles(t *testing.T) {
	s := mustStore(t, t.TempDir())
	if e := s.Update(storeContext(), func(tx *Transaction) error {
		if e := tx.PutAsset(ImageAsset{ID: "ia_a", Hash: "a", Source: "upload", Available: true}); e != nil {
			return e
		}
		return tx.PutAsset(ImageAsset{ID: "ia_b", Hash: "b", Source: "generated", Available: true, ParentAssetIDs: []string{"ia_a"}})
	}); e != nil {
		t.Fatal(e)
	}
	if e := s.Update(storeContext(), func(tx *Transaction) error {
		a, e := tx.ReadAsset(storeScope, "ia_a")
		if e != nil {
			return e
		}
		a.ParentAssetIDs = []string{"ia_b"}
		return tx.PutAsset(a)
	}); e == nil {
		t.Fatal("cycle accepted")
	}
	other := Scope{OwnerID: "other", BackendID: storeScope.BackendID}
	if e := s.Update(WithScope(context.Background(), other), func(tx *Transaction) error {
		return tx.PutAsset(ImageAsset{ID: "ia_c", ParentAssetIDs: []string{"ia_a"}})
	}); e == nil {
		t.Fatal("cross owner parent accepted")
	}
	if e := s.Update(WithScope(context.Background(), other), func(tx *Transaction) error { return tx.PutAsset(ImageAsset{ID: "ia_a", Hash: "a"}) }); e == nil {
		t.Fatal("cross owner overwrite accepted")
	}
}
func TestProjectFailedDeleteLeavesAllRecords(t *testing.T) {
	s := mustStore(t, t.TempDir())
	seedStore(t, s)
	if e := s.Update(storeContext(), func(tx *Transaction) error {
		j, e := tx.ReadJob(storeScope, "ij_one")
		if e != nil {
			return e
		}
		j.Revision++
		j.Status = JobFailed
		if e = tx.PutJob(j); e != nil {
			return e
		}
		return tx.AppendEvent(ImageEvent{JobID: j.ID, Type: EventJobFinished, Data: ImageEventData{Status: JobFailed}})
	}); e != nil {
		t.Fatal(e)
	}
	if e := s.Update(storeContext(), func(tx *Transaction) error {
		if e := tx.DeleteProject("ip_one", 1); e != nil {
			return e
		}
		return errors.New("abort delete")
	}); e == nil {
		t.Fatal("abort ignored")
	}
	if _, e := s.ReadJob(storeScope, "ij_one"); e != nil {
		t.Fatal("delete rollback lost job", e)
	}
	r, ok := s.GetIdempotency(storeScope, "key")
	if !ok || !r.ExpiresAt.IsZero() {
		t.Fatal("delete rollback polluted key")
	}
}
