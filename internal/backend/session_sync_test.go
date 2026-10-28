package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func createListedSession(t *testing.T, base, cwd, title string) protocol.Session {
	t.Helper()
	var created protocol.Session
	code := doJSON(t, http.MethodPost, base+"/v1/sessions", protocol.CreateSessionRequest{
		CWD:      cwd,
		Title:    title,
		Model:    protocol.ModelRef{Provider: "fixture", Model: "fixture-model"},
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: title}}}},
	}, &created)
	if code != http.StatusCreated {
		t.Fatalf("create %q status = %d", title, code)
	}
	return created
}

func TestSessionSnapshotIsImmutableAndChangesResumeFromItsRevision(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	cwd := t.TempDir()
	first := createListedSession(t, server.URL, cwd, "first")
	createListedSession(t, server.URL, cwd, "second")

	var info protocol.SessionSnapshotInfo
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/snapshots", nil, &info); code != http.StatusOK {
		t.Fatalf("snapshot status=%d", code)
	}
	if info.SnapshotID == "" || info.TotalCount != 2 || info.Revision != "0" || len(info.Workdirs) != 1 || info.Workdirs[0].Count != 2 {
		t.Fatalf("snapshot info=%+v", info)
	}

	newTitle := "updated after snapshot"
	var updated protocol.Session
	if code := doJSON(t, http.MethodPatch, server.URL+"/v1/sessions/"+first.ID, protocol.UpdateSessionRequest{Title: &newTitle}, &updated); code != http.StatusOK {
		t.Fatalf("patch status=%d", code)
	}
	if updated.SyncEpoch != info.Epoch || updated.SyncRevision == "" || updated.SyncRevision == info.Revision {
		t.Fatalf("sync confirmation=%+v", updated)
	}

	var page protocol.SessionSnapshotPage
	if code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions/snapshots/"+info.SnapshotID+"?offset=0&limit=1", nil, &page); code != http.StatusOK {
		t.Fatalf("snapshot page status=%d", code)
	}
	if len(page.Sessions) != 1 || page.NextOffset == nil || *page.NextOffset != 1 || page.Revision != info.Revision {
		t.Fatalf("snapshot page=%+v", page)
	}
	for offset := page.NextOffset; offset != nil; {
		var next protocol.SessionSnapshotPage
		if code := doJSON(t, http.MethodGet, fmt.Sprintf("%s/v1/sessions/snapshots/%s?offset=%d&limit=1", server.URL, info.SnapshotID, *offset), nil, &next); code != http.StatusOK {
			t.Fatalf("next page status=%d", code)
		}
		for _, item := range next.Sessions {
			if item.ID == first.ID && item.Title == newTitle {
				t.Fatal("snapshot changed after creation")
			}
		}
		offset = next.NextOffset
	}

	var changes protocol.SessionChanges
	url := server.URL + "/v1/sessions/changes?epoch=" + info.Epoch + "&since=" + info.Revision + "&wait=0"
	if code := doJSON(t, http.MethodGet, url, nil, &changes); code != http.StatusOK {
		t.Fatalf("changes status=%d", code)
	}
	if changes.Reset || len(changes.Changes) != 1 || changes.Changes[0].Session == nil || changes.Changes[0].Session.Title != newTitle || changes.NextSince == nil {
		t.Fatalf("changes=%+v", changes)
	}
	if *changes.NextSince != updated.SyncRevision {
		t.Fatalf("next_since=%s confirmation=%s", *changes.NextSince, updated.SyncRevision)
	}
}

func TestSessionChangesDeletePinnedWorkdirsAndReset(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	cwd := t.TempDir()
	item := createListedSession(t, server.URL, cwd, "item")
	var info protocol.SessionSnapshotInfo
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/snapshots", nil, &info); code != http.StatusOK {
		t.Fatal(code)
	}
	pinned := true
	var updated protocol.Session
	if code := doJSON(t, http.MethodPatch, server.URL+"/v1/sessions/"+item.ID, protocol.UpdateSessionRequest{Pinned: &pinned}, &updated); code != http.StatusOK {
		t.Fatal(code)
	}
	var pinnedPage protocol.SessionPage
	if code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions?pinned=true", nil, &pinnedPage); code != http.StatusOK || len(pinnedPage.Sessions) != 1 || pinnedPage.Sessions[0].ID != item.ID {
		t.Fatalf("pinned=%d %+v", code, pinnedPage)
	}
	var workdirs struct {
		Workdirs []protocol.SessionWorkdir `json:"workdirs"`
	}
	if code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions/workdirs", nil, &workdirs); code != http.StatusOK || len(workdirs.Workdirs) != 1 || workdirs.Workdirs[0].CWD != cwd {
		t.Fatalf("workdirs=%d %+v", code, workdirs)
	}
	var deleted struct {
		SyncEpoch    string `json:"sync_epoch"`
		SyncRevision string `json:"sync_revision"`
	}
	if code := doJSON(t, http.MethodDelete, server.URL+"/v1/sessions/"+item.ID, nil, &deleted); code != http.StatusOK {
		t.Fatal(code)
	}
	since, _ := strconv.ParseUint(updated.SyncRevision, 10, 64)
	var changes protocol.SessionChanges
	if code := doJSON(t, http.MethodGet, fmt.Sprintf("%s/v1/sessions/changes?epoch=%s&since=%d", server.URL, info.Epoch, since), nil, &changes); code != http.StatusOK {
		t.Fatal(code)
	}
	if len(changes.Changes) != 1 || changes.Changes[0].Kind != "delete" || changes.Changes[0].ID != item.ID || changes.Changes[0].Session != nil || changes.NextSince == nil || *changes.NextSince != deleted.SyncRevision {
		t.Fatalf("delete changes=%+v confirm=%+v", changes, deleted)
	}
	var reset protocol.SessionChanges
	if code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions/changes?epoch=wrong&since=0", nil, &reset); code != http.StatusOK || !reset.Reset || reset.ResetReason != "epoch" || reset.NextSince != nil {
		t.Fatalf("reset=%d %+v", code, reset)
	}
}

func TestSessionChangesRejectUnauthorizedAndInvalidInputs(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend, err := New(Options{Store: store, Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(&scriptedClient{}, "fixture", 100, "system"), nil
	}, EventDir: t.TempDir(), Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(backend)
	defer func() { server.Close(); _ = backend.Close() }()
	if code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions/changes?epoch=x&since=0", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", code)
	}
	req, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/sessions/changes?epoch=x&since=-1", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid since status=%d", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodGet, server.URL+"/v1/sessions/changes?epoch=x&since=0&rescan=2", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid rescan status=%d", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodGet, server.URL+"/v1/sessions/changes?epoch=x&since=9007199254740993", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var precise protocol.SessionChanges
	if err := json.NewDecoder(resp.Body).Decode(&precise); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !precise.Reset || precise.NextSince != nil {
		t.Fatalf("large cursor status=%d response=%+v", resp.StatusCode, precise)
	}
}

func TestSessionChangesCapabilityContract(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	var health struct {
		Capabilities map[string]any `json:"capabilities"`
	}
	if code := doJSON(t, http.MethodGet, server.URL+"/v1/health", nil, &health); code != http.StatusOK {
		t.Fatalf("health status=%d", code)
	}
	if health.Capabilities["session_changes"] != float64(2) {
		t.Fatalf("capabilities=%+v", health.Capabilities)
	}
}

func TestSessionSnapshotBudgetAndIdempotentDelete(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	createListedSession(t, server.URL, t.TempDir(), "item")
	var ids []string
	for range maxSessionSnapshots {
		var info protocol.SessionSnapshotInfo
		if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/snapshots", nil, &info); code != http.StatusOK {
			t.Fatalf("snapshot status=%d", code)
		}
		ids = append(ids, info.SnapshotID)
	}
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/snapshots", nil, nil); code != http.StatusTooManyRequests {
		t.Fatalf("snapshot over budget status=%d", code)
	}
	for range 2 {
		if code := doJSON(t, http.MethodDelete, server.URL+"/v1/sessions/snapshots/"+ids[0], nil, nil); code != http.StatusNoContent {
			t.Fatalf("snapshot delete status=%d", code)
		}
	}
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/snapshots", nil, nil); code != http.StatusOK {
		t.Fatalf("snapshot after release status=%d", code)
	}
}

func TestSessionSnapshotCloseReleasesResources(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	var info protocol.SessionSnapshotInfo
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/snapshots", nil, &info); code != http.StatusOK {
		t.Fatal(code)
	}
	backend.snapshotMu.Lock()
	stored := backend.snapshots[info.SnapshotID]
	backend.snapshotMu.Unlock()
	if stored.timer == nil {
		t.Fatal("snapshot has no expiration timer")
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	backend.snapshotMu.Lock()
	remaining := len(backend.snapshots)
	backend.snapshotMu.Unlock()
	if remaining != 0 {
		t.Fatalf("snapshots retained after close: %d", remaining)
	}
}

func TestSessionSnapshotExpiryReturnsGoneAndReleasesBudget(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	var info protocol.SessionSnapshotInfo
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/snapshots", nil, &info); code != http.StatusOK {
		t.Fatal(code)
	}
	backend.snapshotMu.Lock()
	stored := backend.snapshots[info.SnapshotID]
	stored.expiresAt = time.Now().Add(-time.Second)
	backend.snapshots[info.SnapshotID] = stored
	backend.snapshotMu.Unlock()
	if code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions/snapshots/"+info.SnapshotID, nil, nil); code != http.StatusGone {
		t.Fatalf("expired snapshot status=%d", code)
	}
	backend.snapshotMu.Lock()
	remaining := len(backend.snapshots)
	backend.snapshotMu.Unlock()
	if remaining != 0 {
		t.Fatalf("expired snapshot retained budget: %d", remaining)
	}
}
