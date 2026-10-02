package backend

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestCanonicalAcceptancePersistenceFailureStopsSession(t *testing.T) {
	for _, failure := range []string{"journal", "run-record"} {
		t.Run(failure, func(t *testing.T) {
			store, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			client := &scriptedClient{response: "must not run"}
			dir := t.TempDir()
			service, server := newTestServer(t, store, dir, client)
			sess := createTestSession(t, server.URL)
			suffix := ".jsonl"
			if failure == "run-record" {
				suffix = ".runs.json"
			}
			if err := os.Mkdir(filepath.Join(dir, sess.ID+suffix), 0700); err != nil {
				t.Fatal(err)
			}
			response := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ClientRequestID: "failure", Prompt: "hello"}, nil)
			response.Body.Close()
			if response.StatusCode != 500 {
				t.Fatalf("status=%d", response.StatusCode)
			}
			rt, err := service.loadRuntimeByID(sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			rt.mu.Lock()
			active, quarantined := rt.activeLocked(), rt.runtimeErr != nil
			rt.mu.Unlock()
			if active || !quarantined {
				t.Fatalf("active=%v quarantined=%v", active, quarantined)
			}
			client.mu.Lock()
			calls := client.calls
			client.mu.Unlock()
			if calls != 0 {
				t.Fatalf("model called despite failed acceptance: %d", calls)
			}
		})
	}
}

func TestCanonicalAcceptanceRetriesActiveRequestWithoutModelSwitch(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &scriptedClient{wait: true}
	service, server := newTestServer(t, store, t.TempDir(), client)
	sess := createTestSession(t, server.URL)
	input := protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "retry-active", Prompt: "hello", Model: &protocol.ModelRef{Provider: "fixture", Model: "fixture-model"}}
	var first protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs", input, &first)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first status=%d", resp.StatusCode)
	}
	rt, err := service.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	originalAgent := rt.agent
	rt.mu.Unlock()
	var retry protocol.RunAccepted
	resp = postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs", input, &retry)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || retry != first {
		t.Fatalf("retry status=%d first=%+v retry=%+v", resp.StatusCode, first, retry)
	}
	input.Model = &protocol.ModelRef{Provider: "fixture", Model: "different-model"}
	resp = postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs", input, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("conflicting status=%d", resp.StatusCode)
	}
	rt.mu.Lock()
	unchanged := rt.agent == originalAgent && rt.agent.Model == "fixture-model"
	rt.mu.Unlock()
	if !unchanged {
		t.Fatal("duplicate changed the active agent")
	}
	resp = postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs/"+first.RunID+"/cancel", nil, nil)
	resp.Body.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitForCanonicalRun(rt, first.RunID, ctx); err != context.Canceled {
		t.Fatalf("cancel=%v", err)
	}
	client.mu.Lock()
	calls := client.calls
	client.mu.Unlock()
	if calls > 1 {
		t.Fatalf("duplicate model calls=%d", calls)
	}
}

func TestCanonicalWaitTracksRequestedRunWhileNextRunIsActive(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &scriptedClient{response: "first answer"}
	service, server := newTestServer(t, store, t.TempDir(), client)
	sess := createTestSession(t, server.URL)
	first := runRequest(t, server.URL, sess.ID, "first-wait", "first")
	waitForRun(t, server.URL, sess.ID, 0)
	rt, err := service.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitForCanonicalRun(rt, first.RunID, context.Background()); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.wait = true
	client.mu.Unlock()
	second := runRequest(t, server.URL, sess.ID, "second-wait", "second")
	defer func() {
		r := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs/"+second.RunID+"/cancel", nil, nil)
		r.Body.Close()
		if err := waitForCanonicalRun(rt, second.RunID, context.Background()); err != context.Canceled {
			t.Errorf("second run cancellation: %v", err)
		}
	}()
	done := make(chan error, 1)
	go func() { done <- waitForCanonicalRun(rt, first.RunID, context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("completed run waiter followed a later active run")
	}
}

func TestCanonicalAcceptanceRejectsMismatchedConversation(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "unused"})
	sess := createTestSession(t, server.URL)
	response := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: "another-conversation", ClientRequestID: "wrong", Prompt: "hello"}, nil)
	defer response.Body.Close()
	if response.StatusCode != 400 {
		t.Fatalf("status=%d", response.StatusCode)
	}
}
