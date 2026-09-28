package backend

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func createStoredHistorySession(t *testing.T, store *session.Store, cwd string, messages []ai.Message) string {
	t.Helper()
	id, err := store.Create(cwd, "fixture-model", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(id, 0, messages, "fixture-model", "fixture"); err != nil {
		t.Fatal(err)
	}
	return id
}

func getSessionPage(t *testing.T, url string) protocol.SessionPage {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("GET %s: status %d: %s", url, resp.StatusCode, body)
	}
	var page protocol.SessionPage
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	return page
}

type extendedHistoryResponse struct {
	Session           protocol.Session `json:"session"`
	Revision          string           `json:"revision"`
	OldestOffset      int              `json:"oldest_offset"`
	HasMoreBefore     bool             `json:"has_more_before"`
	TotalMessageCount int              `json:"total_message_count"`
	MessageOffsets    []int            `json:"message_offsets"`
}

func TestHistoryExtendedBranchPreservesReplyUntilNextUser(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "unused"})

	id := createStoredHistorySession(t, store, t.TempDir(), []ai.Message{
		{Role: protocol.RoleSystem, Content: "saved system"},
		{Role: protocol.RoleUser, Content: "first question"},
		{Role: protocol.RoleAssistant, Content: "first answer"},
		{Role: protocol.RoleTool, ToolCallID: "call-1", Name: "lookup", Content: "tool result"},
		{Role: protocol.RoleAssistant, Content: "first answer continued"},
		{Role: protocol.RoleUser, Content: "second question"},
		{Role: protocol.RoleAssistant, Content: "second answer"},
	})
	original := getSession(t, server.URL, id)
	var branched protocol.Session
	status := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/"+id+"/branch", protocol.BranchSessionRequest{
		ExpectedRevision: original.Revision,
		MessageRef:       protocol.HistoryMessageRef{MessageID: original.Messages[1].ID, Role: protocol.RoleUser},
		Title:            "first branch",
	}, &branched)
	if status != http.StatusCreated {
		t.Fatalf("branch status=%d", status)
	}
	if branched.ID == id || branched.Title != "first branch" {
		t.Fatalf("branch identity/title=%+v", branched)
	}
	if len(branched.Messages) != 5 {
		t.Fatalf("branch messages=%d: %+v", len(branched.Messages), branched.Messages)
	}
	want := []string{"saved system", "first question", "first answer", "tool result", "first answer continued"}
	for i, message := range branched.Messages {
		if message.Content[0].Text != want[i] {
			t.Fatalf("branch message %d=%+v, want %q", i, message, want[i])
		}
	}
	if branched.Messages[len(branched.Messages)-1].Role != protocol.RoleAssistant {
		t.Fatalf("branch included next user turn: %+v", branched.Messages)
	}
}

func TestSessionMetadataPatchPersistsAcrossRestart(t *testing.T) {
	dir, events := t.TempDir(), t.TempDir()
	store, err := session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &scriptedClient{response: "unused"}
	_, server := newTestServer(t, store, events, fixture)
	id := createStoredHistorySession(t, store, t.TempDir(), []ai.Message{{Role: protocol.RoleUser, Content: "metadata"}})

	body := protocol.UpdateSessionRequest{}
	title := "Pinned archive title"
	pinned, archived := true, true
	body.Title, body.Pinned, body.Archived = &title, &pinned, &archived
	var updated protocol.Session
	if status := doJSON(t, http.MethodPatch, server.URL+"/v1/sessions/"+id, body, &updated); status != http.StatusOK {
		t.Fatalf("PATCH status=%d", status)
	}
	if updated.Title != title || !updated.Pinned || !updated.Archived {
		t.Fatalf("patched metadata=%+v", updated)
	}
	server.Close()
	_ = store.Close()

	store, err = session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, restarted := newTestServer(t, store, events, fixture)
	recovered := getSession(t, restarted.URL, id)
	if recovered.Title != title || !recovered.Pinned || !recovered.Archived {
		t.Fatalf("recovered metadata=%+v", recovered)
	}
}

func TestSessionListingPaginationFiltersAndTotal(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "unused"})
	cwdA, cwdB := t.TempDir(), t.TempDir()
	const total = 205
	for i := 0; i < total; i++ {
		cwd := cwdA
		if i%2 == 1 {
			cwd = cwdB
		}
		id := createStoredHistorySession(t, store, cwd, []ai.Message{{Role: protocol.RoleUser, Content: "session " + strconv.Itoa(i)}})
		if i < 3 {
			if err := store.SetShared(id, "listing-token-"+strconv.Itoa(i), true, true); err != nil {
				t.Fatal(err)
			}
		}
	}
	page := getSessionPage(t, server.URL+"/v1/sessions?page=1&page_size=50")
	if page.TotalCount != total || len(page.Sessions) != 50 {
		t.Fatalf("page one count=%d sessions=%d", page.TotalCount, len(page.Sessions))
	}
	pageFive := getSessionPage(t, server.URL+"/v1/sessions?page=5&page_size=50")
	if pageFive.TotalCount != total || len(pageFive.Sessions) != 5 {
		t.Fatalf("page five count=%d sessions=%d", pageFive.TotalCount, len(pageFive.Sessions))
	}
	filtered := getSessionPage(t, server.URL+"/v1/sessions?page=1&page_size=50&cwd="+url.QueryEscape(cwdA))
	if filtered.TotalCount != (total+1)/2 || len(filtered.Sessions) != 50 {
		t.Fatalf("cwd filter count=%d sessions=%d", filtered.TotalCount, len(filtered.Sessions))
	}
	shared := getSessionPage(t, server.URL+"/v1/sessions?page=1&page_size=50&shared=true")
	if shared.TotalCount != 3 || len(shared.Sessions) != 3 {
		t.Fatalf("shared filter count=%d sessions=%d", shared.TotalCount, len(shared.Sessions))
	}
}

func TestHistoryMutationsRejectActiveRunAndChildTask(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := &scriptedClient{response: "unused", wait: true}
	_, server := newTestServer(t, store, t.TempDir(), fixture)
	id := createStoredHistorySession(t, store, t.TempDir(), []ai.Message{{Role: protocol.RoleUser, Content: "active"}})
	active := getSession(t, server.URL, id)
	run := runRequest(t, server.URL, id, "active-mutation", "wait")
	defer func() {
		resp, err := http.Post(server.URL+"/v1/sessions/"+id+"/runs/"+run.RunID+"/cancel", "application/json", nil)
		if err == nil {
			_ = resp.Body.Close()
		}
		_ = waitForRun(t, server.URL, id, 0)
	}()
	assertMutationConflict := func(path string, input any) {
		t.Helper()
		if status := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/"+id+path, input, nil); status != http.StatusConflict {
			t.Fatalf("active POST %s status=%d", path, status)
		}
	}
	assertMutationConflict("/branch", protocol.BranchSessionRequest{ExpectedRevision: active.Revision, MessageRef: protocol.HistoryMessageRef{MessageID: active.Messages[0].ID}})
	assertMutationConflict("/edit", protocol.EditSessionRequest{ExpectedRevision: active.Revision, MessageRef: protocol.HistoryMessageRef{MessageID: active.Messages[0].ID}, Replacement: protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "edit"}}}})
	if status := doJSON(t, http.MethodDelete, server.URL+"/v1/sessions/"+id, nil, nil); status != http.StatusConflict {
		t.Fatalf("active DELETE status=%d", status)
	}

	childID := createStoredHistorySession(t, store, t.TempDir(), []ai.Message{{Role: protocol.RoleUser, Content: "child"}})
	child := getSession(t, server.URL, childID)
	if err := store.SaveTask(childID, session.Task{ID: "child-active", Status: "running", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if status := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/"+childID+"/branch", protocol.BranchSessionRequest{ExpectedRevision: child.Revision, MessageRef: protocol.HistoryMessageRef{MessageID: child.Messages[0].ID}}, nil); status != http.StatusConflict {
		t.Fatalf("child branch status=%d", status)
	}
	if status := doJSON(t, http.MethodDelete, server.URL+"/v1/sessions/"+childID, nil, nil); status != http.StatusConflict {
		t.Fatalf("child DELETE status=%d", status)
	}
}

func TestShareDefaultRedactionPrivateProjectionRevokeAndDeleteRestart(t *testing.T) {
	dir, events := t.TempDir(), t.TempDir()
	store, err := session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &scriptedClient{response: "unused"}
	_, server := newTestServer(t, store, events, fixture)
	id := createStoredHistorySession(t, store, t.TempDir(), []ai.Message{
		{Role: protocol.RoleSystem, Content: "private system"},
		{Role: protocol.RoleDeveloper, Content: "private developer"},
		{Role: protocol.RoleUser, Content: "public question", Authored: true},
		{Role: protocol.RoleAssistant, Content: "public answer", ToolCalls: []ai.ToolCall{{ID: "secret-call", Type: "function", Function: struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		}{Name: "secret_tool", Arguments: `{"secret":"argument"}`}}}, Usage: &ai.Usage{PromptTokens: 99}},
		{Role: protocol.RoleTool, ToolCallID: "secret-call", Name: "secret_tool", Content: "secret result"},
	})
	var share protocol.ShareStatus
	if status := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/"+id+"/share", protocol.ShareUpdateRequest{Enabled: true}, &share); status != http.StatusOK {
		t.Fatalf("share status=%d", status)
	}
	resp, err := http.Get(server.URL + "/v1/shares/" + share.Token)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("public share status=%d body=%s err=%v", resp.StatusCode, body, err)
	}
	for _, private := range []string{"private system", "private developer", "secret_tool", "secret", "argument", "secret result", "fixture", "usage", "cwd", "provider"} {
		if bytes.Contains(body, []byte(private)) {
			t.Fatalf("public projection leaked %q: %s", private, body)
		}
	}
	for _, public := range []string{"public question", "public answer"} {
		if !bytes.Contains(body, []byte(public)) {
			t.Fatalf("public projection omitted %q: %s", public, body)
		}
	}
	token := share.Token
	if status := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/"+id+"/share", protocol.ShareUpdateRequest{Enabled: false}, &share); status != http.StatusOK {
		t.Fatalf("revoke status=%d", status)
	}
	if status := doJSON(t, http.MethodGet, server.URL+"/v1/shares/"+token, nil, nil); status != http.StatusNotFound {
		t.Fatalf("revoked token status=%d", status)
	}
	server.Close()
	_ = store.Close()
	store, err = session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, restarted := newTestServer(t, store, events, fixture)
	if status := doJSON(t, http.MethodDelete, restarted.URL+"/v1/sessions/"+id, nil, nil); status != http.StatusOK {
		t.Fatalf("delete status=%d", status)
	}
	restarted.Close()
	_ = store.Close()
	store, err = session.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, afterDelete := newTestServer(t, store, events, fixture)
	if status := doJSON(t, http.MethodGet, afterDelete.URL+"/v1/shares/"+token, nil, nil); status != http.StatusNotFound {
		t.Fatalf("deleted/restarted token status=%d", status)
	}
}
