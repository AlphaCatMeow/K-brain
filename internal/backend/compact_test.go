package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestCompactSessionHTTPPersistsHistoryAndEmitsEvents(t *testing.T) {
	store, _, server, sess := compactFixture(t, &scriptedClient{response: "compact summary"})
	defer store.Close()
	seedCompactHistory(t, server.URL, sess.ID)
	before := getSession(t, server.URL, sess.ID)

	var accepted protocol.CompactAccepted
	resp := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/compact", protocol.CompactRequest{
		ConversationID: sess.ID, ClientRequestID: "compact-1", ExpectedRevision: before.Revision,
	}, &accepted)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || accepted.RunID == "" {
		t.Fatalf("compact response = %d %+v", resp.StatusCode, accepted)
	}
	events := waitForRun(t, server.URL, sess.ID, before.LastSeq)
	assertCompactTerminal(t, events)

	var history struct {
		ActiveMessages []protocol.Message `json:"active_messages"`
	}
	resp, err := http.Get(server.URL + "/v1/sessions/" + sess.ID + "/history?max_messages=100&include_active=true")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&history); err != nil {
		t.Fatal(err)
	}
	if len(history.ActiveMessages) < 1 || history.ActiveMessages[0].Role != protocol.RoleSystem {
		t.Fatalf("active history was not compacted: %+v", history.ActiveMessages)
	}
}

func TestCompactSessionHTTPUsesTheSameSummaryRouteAsAutomaticCompaction(t *testing.T) {
	store, backend, server, sess := compactFixture(t, &scriptedClient{response: "automatic summary"})
	defer store.Close()
	rt, err := backend.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	rt.agent.ContextLimit = 1000
	rt.agent.CompactThreshold = 0.99
	rt.agent.MaxTokens = 200
	for range 4 {
		rt.agent.Messages = append(rt.agent.Messages,
			ai.Message{Role: "user", Content: strings.Repeat("u", 400)},
			ai.Message{Role: "assistant", Content: strings.Repeat("a", 400)},
		)
	}

	accepted := runRequest(t, server.URL, sess.ID, "pre-send", "continue")
	events := waitForRun(t, server.URL, sess.ID, accepted.AcceptedSeq-1)
	if !hasEvent(events, protocol.EventRunCompleted) {
		t.Fatalf("run did not complete: %+v", events)
	}
	messages := rt.agent.MessagesSnapshot()
	if len(messages) < 2 || !strings.Contains(messages[1].Content, "Summary of the conversation") {
		t.Fatalf("pre-send compaction did not install a summary: %+v", messages)
	}
}

func TestCompactSessionIsIdempotentAndUsesRevisionCAS(t *testing.T) {
	client := &scriptedClient{response: "summary"}
	store, backend, server, sess := compactFixture(t, client)
	defer store.Close()
	seedCompactHistory(t, server.URL, sess.ID)
	before := getSession(t, server.URL, sess.ID)
	request := protocol.CompactRequest{ConversationID: sess.ID, ClientRequestID: "same-request", ExpectedRevision: before.Revision}
	var first, second protocol.CompactAccepted
	resp := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/compact", request, &first)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first status = %d", resp.StatusCode)
	}
	resp = postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/compact", request, &second)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || first.RunID != second.RunID || first.AcceptedSeq != second.AcceptedSeq || first.AcceptedSeq == 0 {
		t.Fatalf("replay = %d %+v; first = %+v", resp.StatusCode, second, first)
	}
	assertCompactTerminal(t, waitForRun(t, server.URL, sess.ID, before.LastSeq))
	rt, _ := backend.loadRuntimeByID(sess.ID)
	waitCompactDone(t, rt)
	after := getSession(t, server.URL, sess.ID)
	resp = postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/compact", request, &second)
	resp.Body.Close()
	if second.Status != "completed" || first.RunID != second.RunID || second.AcceptedSeq != first.AcceptedSeq {
		t.Fatalf("completed replay = %+v", second)
	}
	for _, changed := range []protocol.CompactRequest{
		{ClientRequestID: "stale-request", ExpectedRevision: before.Revision},
		{ClientRequestID: request.ClientRequestID, ExpectedRevision: after.Revision},
	} {
		resp = postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/compact", changed, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("conflict status = %d", resp.StatusCode)
		}
	}
	if got := getSession(t, server.URL, sess.ID); got.LastSeq != after.LastSeq || got.Revision != after.Revision {
		t.Fatal("replay or stale request mutated session")
	}
	if got := client.requestedModels(); len(got) != 3 {
		t.Fatalf("expected two turns and one compaction, got %v", got)
	}
	restored := &runtimeSession{runs: map[string]runRecord{}}
	restored.id = sess.ID
	if err := restored.loadRuns(backend.eventDir); err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	equal := reflect.DeepEqual(restored.runs[request.ClientRequestID], rt.runs[request.ClientRequestID])
	rt.mu.Unlock()
	if !equal {
		t.Fatal("idempotency record was not persisted")
	}
}

func TestCompactSessionRejectsMissingIdentityAndRevision(t *testing.T) {
	store, _, server, sess := compactFixture(t, &scriptedClient{response: "summary"})
	defer store.Close()
	missingID := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/compact", protocol.CompactRequest{ExpectedRevision: "x"}, nil)
	missingID.Body.Close()
	if missingID.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing identity status = %d", missingID.StatusCode)
	}
	missingRevision := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/compact", protocol.CompactRequest{ClientRequestID: "x"}, nil)
	missingRevision.Body.Close()
	if missingRevision.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing revision status = %d", missingRevision.StatusCode)
	}
}

func TestRestoreCompactionRuntimeRejectsChangedAgent(t *testing.T) {
	store, server, _, sess := compactFixture(t, &scriptedClient{response: "summary"})
	defer store.Close()
	rt, err := server.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	recorder := rt.recorder
	other := agent.New(&scriptedClient{}, "fixture-model", 1024, "system")
	if err := server.restoreCompactionRuntime(rt, other, recorder); err == nil {
		t.Fatal("restore should fail when the live Agent changed")
	}
}

func TestPublishDoesNotAdvanceSequenceWhenJournalFails(t *testing.T) {
	store, server, _, sess := compactFixture(t, &scriptedClient{response: "summary"})
	defer store.Close()
	rt, err := server.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	rt.eventDir = t.TempDir() + "/missing-parent/events"
	before := rt.nextSeq
	if _, err := rt.publish(protocol.EventCompactionStarted, nil, ""); err == nil {
		t.Fatal("expected journal error")
	}
	if rt.nextSeq != before {
		t.Fatalf("sequence advanced on journal error: %d -> %d", before, rt.nextSeq)
	}
}

func compactFixture(t *testing.T, client *scriptedClient) (*session.Store, *Server, *httptest.Server, protocol.Session) {
	t.Helper()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	backend, server := newTestServer(t, store, t.TempDir(), client)
	return store, backend, server, createTestSession(t, server.URL)
}

func seedCompactHistory(t *testing.T, base, id string) {
	t.Helper()
	for _, prompt := range []string{"first", "second"} {
		resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+id+"/runs", protocol.PromptRequest{ConversationID: id, ClientRequestID: prompt, Prompt: prompt}, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("run status = %d", resp.StatusCode)
		}
		_ = waitForRun(t, base, id, getSession(t, base, id).LastSeq-1)
	}
}

func assertCompactTerminal(t *testing.T, events []protocol.Event) {
	t.Helper()
	foundCompleted, foundHistory, foundRunCompleted := false, false, false
	for _, event := range events {
		foundCompleted = foundCompleted || event.Type == protocol.EventCompactionCompleted
		foundHistory = foundHistory || event.Type == protocol.EventHistoryUpdated
		foundRunCompleted = foundRunCompleted || event.Type == protocol.EventRunCompleted
	}
	if !foundCompleted || !foundHistory || !foundRunCompleted {
		t.Fatalf("compaction events = %+v", events)
	}
}

func TestCompactSuccessReleasesContextAndPersistsUsage(t *testing.T) {
	store, backend, server, sess := compactFixture(t, &scriptedClient{response: "answer"})
	defer store.Close()
	seedCompactHistory(t, server.URL, sess.ID)
	rt, err := backend.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := getSession(t, server.URL, sess.ID)
	contextCh := make(chan context.Context, 1)
	rt.agent.CompactClient = &compactCallbackClient{scriptedClient: &scriptedClient{}, complete: func(ctx context.Context) error {
		contextCh <- ctx
		return nil
	}}
	resp := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/compact", protocol.CompactRequest{ClientRequestID: "success", ExpectedRevision: before.Revision}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	events := waitForRun(t, server.URL, sess.ID, before.LastSeq)
	assertCompactTerminal(t, events)
	waitCompactDone(t, rt)
	select {
	case ctx := <-contextCh:
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("successful context was not cancelled")
		}
	case <-time.After(time.Second):
		t.Fatal("compaction client was not called")
	}
	if got := store.Compactions(sess.ID); len(got) != 1 || got[0].Usage.PromptTokens != 7 {
		t.Fatalf("persisted compactions = %+v", got)
	}
	foundUsage := false
	for _, event := range events {
		foundUsage = foundUsage || event.Type == protocol.EventUsage
	}
	if !foundUsage {
		t.Fatal("missing usage event")
	}
	if got := formatCompactionProgress(12, 345); got != "compacting history (12 messages, 345 estimated tokens)" {
		t.Fatalf("progress = %s", got)
	}
}
