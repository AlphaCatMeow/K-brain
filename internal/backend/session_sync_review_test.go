package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/session"
)

type pausedSummaryReviewContext struct {
	context.Context
	once    sync.Once
	entered chan struct{}
	proceed <-chan struct{}
}

func (ctx *pausedSummaryReviewContext) Err() error {
	ctx.once.Do(func() {
		close(ctx.entered)
		<-ctx.proceed
	})
	return ctx.Context.Err()
}

func TestSessionSnapshotInFlightCannotSurviveServerClose(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	createListedSession(t, server.URL, t.TempDir(), "listed")

	proceed := make(chan struct{})
	ctx := &pausedSummaryReviewContext{
		Context: context.Background(),
		entered: make(chan struct{}),
		proceed: proceed,
	}
	response := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		request := httptest.NewRequest(http.MethodPost, "/v1/sessions/snapshots", nil).WithContext(ctx)
		backend.ServeHTTP(response, request)
	}()
	select {
	case <-ctx.entered:
	case <-time.After(time.Second):
		close(proceed)
		t.Fatal("snapshot request did not enter the store")
	}
	if err := backend.Close(); err != nil {
		close(proceed)
		t.Fatal(err)
	}
	close(proceed)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("in-flight snapshot request did not settle")
	}
	backend.snapshotMu.Lock()
	remaining := len(backend.snapshots)
	backend.snapshotMu.Unlock()
	if remaining != 0 {
		t.Errorf("closed server retained %d late snapshots and their expiration timers", remaining)
	}
	backend.snapshotMu.Lock()
	builders := backend.snapshotBuilders
	cancelRetained := backend.snapshotBuildCancel != nil
	backend.snapshotMu.Unlock()
	if builders != 0 || cancelRetained {
		t.Errorf("closed server retained builders=%d cancellation=%v", builders, cancelRetained)
	}
	if response.Code != http.StatusServiceUnavailable {
		t.Errorf("in-flight snapshot after close returned status %d, want 503", response.Code)
	}
}

func TestSessionSnapshotInFlightBudgetAndCancellation(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	createListedSession(t, server.URL, t.TempDir(), "listed")
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	proceed := make(chan struct{})
	ctx := &pausedSummaryReviewContext{Context: parent, entered: make(chan struct{}), proceed: proceed}
	first := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		backend.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/sessions/snapshots", nil).WithContext(ctx))
	}()
	select {
	case <-ctx.entered:
	case <-time.After(time.Second):
		close(proceed)
		t.Fatal("snapshot builder did not start")
	}

	second := httptest.NewRecorder()
	backend.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/v1/sessions/snapshots", nil))
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Errorf("concurrent builder returned %d Retry-After=%q", second.Code, second.Header().Get("Retry-After"))
	}
	cancel()
	close(proceed)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("cancelled builder did not settle")
	}
	backend.snapshotMu.Lock()
	builders, retained := backend.snapshotBuilders, len(backend.snapshots)
	backend.snapshotMu.Unlock()
	if builders != 0 || retained != 0 {
		t.Fatalf("cancelled builder retained builders=%d snapshots=%d", builders, retained)
	}
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/snapshots", nil, nil); code != http.StatusOK {
		t.Fatalf("request after cancellation returned %d", code)
	}
}

func TestSessionSnapshotFullBudgetRejectsBeforeStoreWork(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	item := createListedSession(t, server.URL, t.TempDir(), "listed")
	for range maxSessionSnapshots {
		if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/snapshots", nil, nil); code != http.StatusOK {
			t.Fatalf("initial snapshot status=%d", code)
		}
	}
	if err := os.WriteFile(store.TranscriptPath(item.ID), []byte("invalid transcript\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/snapshots", nil, nil); code != http.StatusTooManyRequests {
		t.Fatalf("full budget request read corrupt storage before rejecting: status=%d", code)
	}
}

func TestSessionSnapshotFailedBuildReleasesAdmission(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	item := createListedSession(t, server.URL, t.TempDir(), "listed")
	path := store.TranscriptPath(item.ID)
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid transcript\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/snapshots", nil, nil); code != http.StatusInternalServerError {
		t.Fatalf("corrupt snapshot status=%d", code)
	}
	backend.snapshotMu.Lock()
	builders, retained := backend.snapshotBuilders, len(backend.snapshots)
	backend.snapshotMu.Unlock()
	if builders != 0 || retained != 0 {
		t.Fatalf("failed builder retained builders=%d snapshots=%d", builders, retained)
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/snapshots", nil, nil); code != http.StatusOK {
		t.Fatalf("recovered storage snapshot status=%d", code)
	}
}

func TestSessionSnapshotEmptyPageReturnsAnArray(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	var info struct {
		SnapshotID string `json:"snapshot_id"`
		TotalCount int    `json:"total_count"`
	}
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/snapshots", nil, &info); code != http.StatusOK {
		t.Fatalf("empty snapshot status=%d", code)
	}
	var page map[string]json.RawMessage
	if code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions/snapshots/"+info.SnapshotID, nil, &page); code != http.StatusOK {
		t.Fatalf("empty snapshot page status=%d", code)
	}
	if string(page["sessions"]) != "[]" || string(page["next_offset"]) != "null" || info.TotalCount != 0 {
		t.Fatalf("empty snapshot page must contain sessions:[] and next_offset:null: info=%+v page=%s", info, page["sessions"])
	}
}
