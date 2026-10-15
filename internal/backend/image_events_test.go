package backend

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func imageHTTPEventsRequest(s *Server, id, last string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/v1/image/jobs/"+id+"/events", nil)
	r.RemoteAddr = "127.0.0.1:1"
	r.Header.Set("Authorization", "Bearer test-image-token")
	if last != "" {
		r.Header.Set("Last-Event-ID", last)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestImageEventHTTPReplayTerminalGap(t *testing.T) {
	s := imageHTTPFixture(t)
	j, _ := imageHTTPCreateJob(t, s, "events-key")
	if _, err := s.image.Jobs.CancelJob(context.Background(), imageTestScope(), j.ID); err != nil {
		t.Fatal(err)
	}
	w := imageHTTPEventsRequest(s, j.ID, "1")
	if w.Code != 200 || strings.Contains(w.Body.String(), "id: 1\n") || !strings.Contains(w.Body.String(), "id: 2\n") || !strings.Contains(w.Body.String(), `"version":"kbrain.image.v1"`) || !strings.Contains(w.Body.String(), `"type":"job.finished"`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	w = imageHTTPEventsRequest(s, j.ID, "2")
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal(w.Body.String())
	}
	w = imageHTTPEventsRequest(s, j.ID, "3")
	imageAssertError(t, w, 409, imaging.CodeEventGap)
	if !strings.Contains(w.Body.String(), `"lastEventSeq":2`) || !strings.Contains(w.Body.String(), `"firstAvailableSeq":1`) {
		t.Fatal("missing recovery position")
	}
	for _, last := range []string{"-1", "+1", "1.0", "9007199254740992"} {
		w = imageHTTPEventsRequest(s, j.ID, last)
		imageAssertError(t, w, 422, imaging.CodeInvalidInput)
	}
	w = imageHTTPEventsRequest(s, "ij_other", "0")
	imageAssertError(t, w, 404, imaging.CodeNotFound)
}
func TestImageEventHTTPRetainedGapAndPagedReplay(t *testing.T) {
	s := imageHTTPFixture(t)
	scope := imageTestScope()
	ctx := imaging.WithScope(context.Background(), scope)
	p, err := s.image.Store.CreateProject(ctx, scope, "history", "")
	if err != nil {
		t.Fatal(err)
	}
	j := imaging.ImageJob{ID: "ij_retained", ProjectID: p.ID, Status: imaging.JobFailed, Revision: 1}
	err = s.image.Store.Update(ctx, func(tx *imaging.Transaction) error {
		if e := tx.PutJob(j); e != nil {
			return e
		}
		if e := tx.AppendEvent(imaging.ImageEvent{JobID: j.ID, Type: imaging.EventJobAccepted, Timestamp: time.Now().Add(-31 * 24 * time.Hour)}); e != nil {
			return e
		}
		for range 104 {
			if e := tx.AppendEvent(imaging.ImageEvent{JobID: j.ID, Type: imaging.EventJobStateChanged, Data: imaging.ImageEventData{Status: j.Status}}); e != nil {
				return e
			}
		}
		return tx.AppendEvent(imaging.ImageEvent{JobID: j.ID, Type: imaging.EventJobFinished, Data: imaging.ImageEventData{Status: j.Status}})
	})
	if err != nil {
		t.Fatal(err)
	}
	w := imageHTTPEventsRequest(s, j.ID, "0")
	if w.Code != 200 || strings.Count(w.Body.String(), "event: image.event\n") != 106 {
		t.Fatalf("paged replay %d %s", w.Code, w.Body.String())
	}
	err = s.image.Store.Update(ctx, func(tx *imaging.Transaction) error { return tx.PruneEvents(j.ID) })
	if err != nil {
		t.Fatal(err)
	}
	w = imageHTTPEventsRequest(s, j.ID, "0")
	imageAssertError(t, w, 409, imaging.CodeEventGap)
	if !strings.Contains(w.Body.String(), `"firstAvailableSeq":2`) {
		t.Fatal(w.Body.String())
	}
	w = imageHTTPEventsRequest(s, j.ID, "1")
	if w.Code != 200 || strings.Count(w.Body.String(), "event: image.event\n") != 105 {
		t.Fatal("retained replay incomplete")
	}
}

func TestImageEventHTTPHeartbeatAndRequestCancellation(t *testing.T) {
	s := imageHTTPFixture(t)
	j, _ := imageHTTPCreateJob(t, s, "heartbeat-key")
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(done); s.ServeHTTP(w, r) }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/image/jobs/"+j.ID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer test-image-token")
	r.Header.Set("Last-Event-ID", "1")
	start := time.Now()
	resp, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil || line != ": heartbeat\n" {
		t.Fatalf("heartbeat %q %v", line, err)
	}
	if time.Since(start) < 14*time.Second {
		t.Fatal("heartbeat was not 15 seconds")
	}
	cancel()
	_ = resp.Body.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("request cancellation did not close subscription")
	}
	current, err := s.image.Jobs.GetJob(context.Background(), imageTestScope(), j.ID)
	if err != nil || current.Status != imaging.JobQueued {
		t.Fatal("subscription cancellation changed job")
	}
}
