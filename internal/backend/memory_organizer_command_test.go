package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/memoryruntime"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

func TestMemoryOrganizerCommandUsesCanonicalRuntime(t *testing.T) {
	store, err := memory.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"one", "two"} {
		if _, err := store.Write(memory.WriteArgs{Slug: slug, Scope: "global", MemoryType: "reference", Description: "same", Body: "same"}); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{token: "secret", memoryStore: store, memoryRuntimeFactory: func(context.Context, string, protocol.ModelRef) (*memoryruntime.Runtime, error) {
		return memoryruntime.New(memoryruntime.Config{Store: store, ResolveModel: func(context.Context, string) (ai.Client, string, error) {
			return nil, "", fmt.Errorf("must not run a model")
		}})
	}}
	request := func(body, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/memory/manage", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w
	}
	if got := request(`{"command":"memory_organizer","args":{"action":"run"}}`, "wrong"); got.Code != 401 {
		t.Fatalf("auth=%d", got.Code)
	}
	if got := request(`{"command":"memory_organizer","args":{"action":"run","trigger":"scheduled"}}`, "secret"); got.Code != 400 {
		t.Fatalf("scheduled=%d %s", got.Code, got.Body)
	}
	run := request(`{"command":"memory_organizer","args":{"action":"run"}}`, "secret")
	if run.Code != 200 {
		t.Fatal(run.Body.String())
	}
	var proposal memoryruntime.OrganizerRun
	if err := json.Unmarshal(run.Body.Bytes(), &proposal); err != nil {
		t.Fatal(err)
	}
	if proposal.Status != "pending_review" || len(proposal.Pending) != 1 {
		t.Fatalf("proposal=%+v", proposal)
	}
	for i := 0; i < 2; i++ {
		got := request(fmt.Sprintf(`{"command":"memory_organizer","args":{"action":"apply","runId":%q}}`, proposal.RunID), "secret")
		if got.Code != 200 {
			t.Fatal(got.Body.String())
		}
	}
	got := request(`{"command":"memory_organizer","args":{"action":"history"}}`, "secret")
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"status":"completed"`) {
		t.Fatalf("history=%s", got.Body)
	}
	entries, err := store.List(memory.ListArgs{Limit: 100})
	if err != nil || len(entries.Entries) != 1 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}
