package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

// This fake is exclusively an HTTP contract fixture; it never contacts an adapter.
type imageHTTPJobsFake struct {
	store *imaging.Store
	mu    sync.Mutex
	next  int
}

func (f *imageHTTPJobsFake) CreateJob(ctx context.Context, scope imaging.Scope, key string, input imaging.CreateJobRequest) (imaging.ImageJob, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hash, err := imaging.HashRequest(input)
	if err != nil {
		return imaging.ImageJob{}, false, err
	}
	if prior, ok := f.store.GetIdempotency(scope, key); ok {
		if prior.Hash != hash {
			return imaging.ImageJob{}, false, imageError(imaging.CodeIdempotencyConflict)
		}
		j, e := f.store.ReadJob(scope, prior.JobID)
		return j, true, e
	}
	f.next++
	j := imaging.ImageJob{ID: fmt.Sprintf("ij_http_%d", f.next), ProjectID: input.ProjectID, Request: input, Status: imaging.JobQueued, Revision: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	err = f.store.Update(imaging.WithScope(ctx, scope), func(tx *imaging.Transaction) error {
		if e := tx.PutJob(j); e != nil {
			return e
		}
		if e := tx.AppendEvent(imaging.ImageEvent{JobID: j.ID, Type: imaging.EventJobAccepted, Data: imaging.ImageEventData{Status: j.Status}}); e != nil {
			return e
		}
		return tx.PutIdempotency(scope, key, hash, j.ID)
	})
	return j, false, err
}
func (f *imageHTTPJobsFake) GetJob(_ context.Context, scope imaging.Scope, id string) (imaging.ImageJob, error) {
	return f.store.ReadJob(scope, id)
}
func (f *imageHTTPJobsFake) ListJobs(ctx context.Context, scope imaging.Scope, id string, p imaging.PageRequest) (imaging.Page[imaging.ImageJob], error) {
	return f.store.ListJobs(ctx, scope, id, p)
}
func (f *imageHTTPJobsFake) ReadEvents(ctx context.Context, scope imaging.Scope, id string, after uint64, limit int) ([]imaging.ImageEvent, error) {
	return f.store.ReadEvents(ctx, scope, id, after, limit)
}
func (f *imageHTTPJobsFake) CancelJob(ctx context.Context, scope imaging.Scope, id string) (imaging.ImageJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, err := f.store.ReadJob(scope, id)
	if err != nil || imageTerminal(j.Status) {
		return j, err
	}
	j.Status = imaging.JobCancelled
	j.Revision++
	j.CancelEffect = "not_submitted"
	err = f.store.Update(imaging.WithScope(ctx, scope), func(tx *imaging.Transaction) error {
		if e := tx.PutJob(j); e != nil {
			return e
		}
		return tx.AppendEvent(imaging.ImageEvent{JobID: id, Type: imaging.EventJobFinished, Data: imaging.ImageEventData{Status: j.Status}})
	})
	return j, err
}
func imageHTTPCreateJob(t *testing.T, s *Server, key string) (imaging.ImageJob, string) {
	t.Helper()
	p, err := s.image.Store.CreateProject(context.Background(), imageTestScope(), "test", "")
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"projectId":%q,"targetId":"it_test","operation":"generate","prompt":"test","parameters":{},"inputs":[],"count":1}`, p.ID)
	w := imageHTTPPostJob(s, key, body)
	if w.Code != 202 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var v struct{ Job imaging.ImageJob }
	if err = json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v.Job, body
}
func imageHTTPPostJob(s *Server, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/v1/image/jobs", strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("Authorization", "Bearer test-image-token")
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestImageJobHTTPAcceptedReplayConflictSnapshotCancel(t *testing.T) {
	s := imageHTTPFixture(t)
	j, body := imageHTTPCreateJob(t, s, "http-key")
	w := imageHTTPPostJob(s, "http-key", body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"replayed":true`) {
		t.Fatal(w.Body.String())
	}
	w = imageHTTPPostJob(s, "http-key", strings.Replace(body, `"prompt":"test"`, `"prompt":"different"`, 1))
	imageAssertError(t, w, 409, imaging.CodeIdempotencyConflict)
	w = imageHTTPPostJob(s, "", body)
	imageAssertError(t, w, 422, imaging.CodeInvalidInput)
	w = imageHTTPRequest(s, "GET", "/v1/image/jobs/"+j.ID, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"lastEventSeq":1`) || !strings.Contains(w.Body.String(), `"firstAvailableSeq":1`) {
		t.Fatal(w.Body.String())
	}
	for range 2 {
		w = imageHTTPRequest(s, "POST", "/v1/image/jobs/"+j.ID+"/cancel", nil)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"cancelled"`) {
			t.Fatal(w.Body.String())
		}
	}
	events, err := s.image.Store.ReadEvents(context.Background(), imageTestScope(), j.ID, 0, 100)
	if err != nil || len(events) != 2 {
		t.Fatalf("events %v %v", events, err)
	}
	w = imageHTTPRequest(s, "GET", "/v1/image/jobs?projectId="+j.ProjectID, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), j.ID) {
		t.Fatal(w.Body.String())
	}
	other := imaging.Scope{OwnerID: "other", BackendID: "local"}
	if _, err = s.image.Store.ReadJob(other, j.ID); err == nil {
		t.Fatal("cross owner access")
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	w = imageHTTPPostJob(s, "after-close", body)
	imageAssertError(t, w, 503, imaging.CodeUnavailable)
}

func TestImageJobHTTPDeleteRefusesActiveThenTombstones(t *testing.T) {
	s := imageHTTPFixture(t)
	j, _ := imageHTTPCreateJob(t, s, "delete-key")
	w := imageHTTPRequest(s, "DELETE", "/v1/image/jobs/"+j.ID, nil)
	imageAssertError(t, w, 409, imaging.CodeProjectBusy)
	if !strings.Contains(w.Body.String(), "Image job is still active") {
		t.Fatal(w.Body.String())
	}
	if w = imageHTTPRequest(s, "POST", "/v1/image/jobs/"+j.ID+"/cancel", nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = imageHTTPRequest(s, "DELETE", "/v1/image/jobs/"+j.ID, nil); w.Code != 200 || !strings.Contains(w.Body.String(), `"deleted":true`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	w = imageHTTPRequest(s, "GET", "/v1/image/jobs/"+j.ID, nil)
	imageAssertError(t, w, 404, imaging.CodeNotFound)
	w = imageHTTPRequest(s, "GET", "/v1/image/jobs?projectId="+j.ProjectID, nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), j.ID) {
		t.Fatal(w.Body.String())
	}
	// Repeating the delete is idempotent so a retried or concurrent click never errors.
	if w = imageHTTPRequest(s, "DELETE", "/v1/image/jobs/"+j.ID, nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = imageHTTPRequest(s, "DELETE", "/v1/image/jobs/ij_missing", nil)
	imageAssertError(t, w, 404, imaging.CodeNotFound)
	w = imageHTTPRequest(s, "PATCH", "/v1/image/jobs/"+j.ID, nil)
	if w.Code != 405 || !strings.Contains(w.Header().Get("Allow"), "DELETE") {
		t.Fatalf("%d %s", w.Code, w.Header().Get("Allow"))
	}
}
