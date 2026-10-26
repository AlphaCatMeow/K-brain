package backend

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestSessionsExistReportsOnlyLoadableSessions(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	kept := createTestSession(t, server.URL)
	gone := createTestSession(t, server.URL)
	archived := createTestSession(t, server.URL)
	for _, id := range []string{kept.ID, gone.ID, archived.ID} {
		if err := store.Save(id, 0, []ai.Message{{Role: "user", Content: "hi"}}, "fixture-model", "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	if code := doJSON(t, http.MethodDelete, server.URL+"/v1/sessions/"+gone.ID, nil, nil); code != http.StatusOK {
		t.Fatalf("delete status=%d", code)
	}
	archive := true
	if code := doJSON(t, http.MethodPatch, server.URL+"/v1/sessions/"+archived.ID, protocol.UpdateSessionRequest{Archived: &archive}, nil); code != http.StatusOK {
		t.Fatalf("archive status=%d", code)
	}

	var got struct {
		Existing []string `json:"existing"`
	}
	ids := []string{kept.ID, gone.ID, archived.ID, "missing-session", "../escape", kept.ID}
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/exists", map[string]any{"ids": ids}, &got); code != http.StatusOK {
		t.Fatalf("exists status=%d", code)
	}
	// Archived sessions are absent from the default list but still exist.
	if strings.Join(got.Existing, ",") != kept.ID+","+archived.ID {
		t.Fatalf("existing = %v", got.Existing)
	}

	tooMany := make([]string, session.MaxExistsIDs+1)
	for i := range tooMany {
		tooMany[i] = "id"
	}
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/exists", map[string]any{"ids": tooMany}, nil); code != http.StatusBadRequest {
		t.Fatalf("oversized exists status=%d", code)
	}
}

func TestSessionListCountsMessagesFromThePageRead(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	small := createTestSession(t, server.URL)
	large := createTestSession(t, server.URL)
	if err := store.Save(small.ID, 0, []ai.Message{{Role: "user", Content: "one"}}, "fixture-model", "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(large.ID, 0, []ai.Message{{Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}}, "fixture-model", "fixture"); err != nil {
		t.Fatal(err)
	}
	var page protocol.SessionPage
	if code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions?page=1&page_size=1", nil, &page); code != http.StatusOK {
		t.Fatalf("list status=%d", code)
	}
	if page.TotalCount != 2 || len(page.Sessions) != 1 {
		t.Fatalf("first page = %+v", page)
	}
	counts := map[string]int{page.Sessions[0].ID: page.Sessions[0].MessageCount}
	if code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions?page=2&page_size=1", nil, &page); code != http.StatusOK || len(page.Sessions) != 1 {
		t.Fatalf("second page code=%d %+v", code, page)
	}
	counts[page.Sessions[0].ID] = page.Sessions[0].MessageCount
	if counts[small.ID] != 1 || counts[large.ID] != 3 {
		t.Fatalf("message counts = %v", counts)
	}
}

func TestHealthAdvertisesSessionExists(t *testing.T) {
	w := imageHTTPRequest(&Server{token: "test-image-token"}, "GET", "/v1/health", nil)
	if !strings.Contains(w.Body.String(), `"session_exists":1`) {
		t.Fatal(w.Body.String())
	}
}
