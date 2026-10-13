package imaging

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestPaginationScopeChecksumAndReopen(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	ctx := context.Background()
	scopes := []Scope{storeScope, {OwnerID: "other", BackendID: storeScope.BackendID}, {OwnerID: storeScope.OwnerID, BackendID: "other"}}
	for n, scope := range scopes {
		e := s.Update(WithScope(ctx, scope), func(tx *Transaction) error {
			for _, suffix := range []string{"a", "b", "c"} {
				id := string(rune('0'+n)) + suffix
				if e := tx.PutProject(ImageProject{ID: "ip_" + id, Revision: 1, CreatedAt: time.Unix(1, 0)}); e != nil {
					return e
				}
				if e := tx.PutJob(ImageJob{ID: "ij_" + id, ProjectID: "ip_" + id, Revision: 1, Status: JobQueued, CreatedAt: time.Unix(1, 0)}); e != nil {
					return e
				}
				if e := tx.AppendEvent(ImageEvent{JobID: "ij_" + id, Type: EventJobAccepted}); e != nil {
					return e
				}
				if e := tx.PutIdempotency(scope, "same-"+suffix, "hash", "ij_"+id); e != nil {
					return e
				}
			}
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	first, e := s.ListProjects(ctx, storeScope, PageRequest{Limit: 1})
	if e != nil || len(first.Items) != 1 || first.Items[0].ID != "ip_0a" || first.NextCursor == "" {
		t.Fatal(first, e)
	}
	s.Close()
	s = mustStore(t, dir)
	second, e := s.ListProjects(ctx, storeScope, PageRequest{Limit: 1, Cursor: first.NextCursor})
	if e != nil || len(second.Items) != 1 || second.Items[0].ID != "ip_0b" {
		t.Fatal(second, e)
	}
	third, e := s.ListProjects(ctx, storeScope, PageRequest{Limit: 1, Cursor: second.NextCursor})
	if e != nil || len(third.Items) != 1 || third.Items[0].ID != "ip_0c" || third.NextCursor != "" {
		t.Fatal(third, e)
	}
	for _, scope := range scopes[1:] {
		if _, e = s.ListProjects(ctx, scope, PageRequest{Limit: 1, Cursor: first.NextCursor}); e == nil {
			t.Fatal("cursor replayed in another scope")
		}
		if r, ok := s.GetIdempotency(scope, "same-a"); !ok || r.JobID == "ij_0a" {
			t.Fatal("idempotency scope collision")
		}
	}
	parts := strings.Split(first.NextCursor, ".")
	payload, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil || strings.Contains(string(payload), `"`+storeScope.OwnerID+`"`) || strings.Contains(string(payload), `"`+storeScope.BackendID+`"`) {
		t.Fatal("cursor exposes internal identity", e)
	}
	for _, cursor := range []string{parts[0] + "x." + parts[1], parts[0] + "." + strings.Repeat("A", 43), "junk"} {
		if _, e = s.ListProjects(ctx, storeScope, PageRequest{Limit: 1, Cursor: cursor}); e == nil {
			t.Fatal("forged cursor accepted")
		}
	}
	if _, e = s.ListJobs(ctx, storeScope, "", PageRequest{Limit: 1, Cursor: first.NextCursor}); e == nil {
		t.Fatal("cursor type not bound")
	}
	jobs, e := s.ListJobs(ctx, storeScope, "", PageRequest{Limit: 1})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.ListJobs(ctx, storeScope, "ip_0a", PageRequest{Limit: 1, Cursor: jobs.NextCursor}); e == nil {
		t.Fatal("cursor filter not bound")
	}
	for _, limit := range []int{-1, 0, 101} {
		if _, e = s.ListProjects(ctx, storeScope, PageRequest{Limit: limit}); e == nil {
			t.Fatal("invalid limit accepted")
		}
	}
	// Deleting a cursor anchor does not invalidate its stable createdAt+id position.
	if e = s.Update(storeContext(), func(tx *Transaction) error {
		j, e := tx.ReadJob(storeScope, "ij_0a")
		if e != nil {
			return e
		}
		j.Revision++
		j.Status = JobFailed
		if e = tx.PutJob(j); e != nil {
			return e
		}
		if e = tx.AppendEvent(ImageEvent{JobID: j.ID, Type: EventJobFinished, Data: ImageEventData{Status: JobFailed}}); e != nil {
			return e
		}
		return tx.DeleteProject("ip_0a", 1)
	}); e != nil {
		t.Fatal(e)
	}
	page, e := s.ListProjects(ctx, storeScope, PageRequest{Limit: 100, Cursor: first.NextCursor})
	if e != nil || len(page.Items) != 2 {
		t.Fatal("deleted cursor anchor", page, e)
	}
}
