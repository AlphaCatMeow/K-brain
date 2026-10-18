package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func queueTestGet(t *testing.T, url string) queueResponse {
	t.Helper()
	var out queueResponse
	if code := doJSON(t, http.MethodGet, url, nil, &out); code != 200 {
		t.Fatalf("get queue=%d %+v", code, out)
	}
	return out
}

func queueTestAction(t *testing.T, url, action, id string, revision uint64) queueResponse {
	t.Helper()
	var out queueResponse
	if code := doJSON(t, http.MethodPost, url+"/actions", queueAction{Action: action, ItemID: id, Revision: revision}, &out); code != 200 {
		t.Fatalf("action %s=%d %+v", action, code, out)
	}
	return out
}

func queueTestSubmit(t *testing.T, url, id, prompt string) queueResponse {
	t.Helper()
	var out queueResponse
	if code := doJSON(t, http.MethodPost, url, queueSubmit{Request: protocol.PromptRequest{ClientRequestID: id, Prompt: prompt}}, &out); code != 202 {
		t.Fatalf("submit=%d %+v", code, out)
	}
	return out
}

func queueTestWait(t *testing.T, url string, check func(queueResponse) bool) queueResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out := queueTestGet(t, url)
		if check(out) {
			return out
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("queue condition timed out: %+v", queueTestGet(t, url))
	return queueResponse{}
}

func TestBackendQueueRevisionEditRemovalAndDedupe(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, h := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	sess := createTestSession(t, h.URL)
	url := h.URL + "/v1/sessions/" + sess.ID + "/queue"
	paused := queueTestAction(t, url, "pause", "", 0)
	first := queueTestSubmit(t, url, "first", "one")
	second := queueTestSubmit(t, url, "second", "two")
	if first.Snapshot.Revision != paused.Snapshot.Revision+1 || len(second.Snapshot.Items) != 2 {
		t.Fatalf("bad queue: %+v", second)
	}
	var out queueResponse
	if code := doJSON(t, http.MethodPost, url, queueSubmit{Request: protocol.PromptRequest{ClientRequestID: "first", Prompt: "one"}}, &out); code != 200 || out.Receipt.ID != first.Receipt.ID || len(out.Snapshot.Items) != 2 {
		t.Fatalf("replay=%d %+v", code, out)
	}
	if code := doJSON(t, http.MethodPost, url, queueSubmit{Request: protocol.PromptRequest{ClientRequestID: "first", Prompt: "different"}}, &out); code != 409 || out.ErrorCode != "conflict" {
		t.Fatalf("collision=%d %+v", code, out)
	}
	if code := doJSON(t, http.MethodPost, url+"/actions", queueAction{Action: "remove", ItemID: first.Receipt.ID, Revision: first.Snapshot.Revision}, &out); code != 409 || len(out.Snapshot.Items) != 2 {
		t.Fatalf("stale action=%d %+v", code, out)
	}
	if code := doJSON(t, http.MethodPost, url+"/actions", queueAction{Action: "move", ItemID: second.Receipt.ID, Direction: "up", Revision: second.Snapshot.Revision}, &out); code != 200 || out.Snapshot.Items[0].ID != second.Receipt.ID {
		t.Fatalf("move=%d %+v", code, out)
	}
	edit := queueTestAction(t, url, "edit_begin", first.Receipt.ID, out.Snapshot.Revision)
	if edit.Item == nil || edit.Item.Request.Prompt != "one" || len(edit.Snapshot.Items) != 1 {
		t.Fatalf("edit=%+v", edit)
	}
	req := *edit.Item.Request
	req.Prompt = "edited"
	if code := doJSON(t, http.MethodPost, url+"/actions", queueAction{Action: "edit_commit", ItemID: first.Receipt.ID, Revision: edit.Snapshot.Revision, Request: &req, DraftJSON: `{"text":"edited","segments":[]}`, UploadedFilesJSON: "[]"}, &out); code != 200 || out.Snapshot.Items[1].PreviewText != "edited" {
		t.Fatalf("commit=%d %+v", code, out)
	}
	edit = queueTestAction(t, url, "edit_begin", first.Receipt.ID, out.Snapshot.Revision)
	restored := queueTestAction(t, url, "edit_cancel", first.Receipt.ID, edit.Snapshot.Revision)
	removed := queueTestAction(t, url, "remove", first.Receipt.ID, restored.Snapshot.Revision)
	if code := doJSON(t, http.MethodPost, url, queueSubmit{Request: protocol.PromptRequest{ClientRequestID: "first", Prompt: "one"}}, &out); code != 200 || out.Receipt.State != "cancelled" || out.Snapshot.Revision != removed.Snapshot.Revision {
		t.Fatalf("deleted replay resurrected=%d %+v", code, out)
	}
	if code := doJSON(t, http.MethodPost, h.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ClientRequestID: "bypass", Prompt: "jump queue"}, nil); code != 409 {
		t.Fatalf("immediate bypass=%d", code)
	}
}

func TestBackendQueueFIFOInterruptAndShutdown(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &scriptedClient{wait: true}
	s, h := newTestServer(t, store, t.TempDir(), client)
	sess := createTestSession(t, h.URL)
	url := h.URL + "/v1/sessions/" + sess.ID + "/queue"
	first := queueTestSubmit(t, url, "first", "one")
	queueTestWait(t, url+"?client_request_id=first", func(out queueResponse) bool { return out.Receipt.Run != nil })
	second := queueTestSubmit(t, url, "second", "two")
	third := queueTestSubmit(t, url, "third", "three")
	if len(third.Snapshot.Items) != 2 {
		t.Fatalf("expected waiting queue: %+v", third)
	}
	client.mu.Lock()
	client.wait = false
	client.response = "done"
	client.mu.Unlock()
	queueTestAction(t, url, "run_now", third.Receipt.ID, third.Snapshot.Revision)
	queueTestWait(t, url+"?client_request_id=third", func(out queueResponse) bool { return out.Receipt.State == "completed" })
	queueTestWait(t, url+"?client_request_id=second", func(out queueResponse) bool { return out.Receipt.State == "completed" })
	client.mu.Lock()
	var prompts []string
	for _, messages := range client.requests {
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				prompts = append(prompts, messages[i].Content)
				break
			}
		}
	}
	calls := client.calls
	client.mu.Unlock()
	if calls != 3 || fmt.Sprint(prompts) != "[one three two]" {
		t.Fatalf("dispatch order calls=%d prompts=%v", calls, prompts)
	}
	if first.Receipt.ID == second.Receipt.ID {
		t.Fatal("duplicate queue identities")
	}
	client.mu.Lock()
	client.wait = true
	client.mu.Unlock()
	queueTestSubmit(t, url, "shutdown-active", "active")
	queueTestWait(t, url+"?client_request_id=shutdown-active", func(out queueResponse) bool { return out.Receipt.Run != nil })
	queueTestSubmit(t, url, "shutdown-waiting", "waiting")
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown blocked on queue worker")
	}
}

func TestBackendQueueRestartPausedAndPersistenceRollback(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dir := t.TempDir()
	client := &scriptedClient{response: "restored"}
	s, h := newTestServer(t, store, dir, client)
	sess := createTestSession(t, h.URL)
	path := "/v1/sessions/" + sess.ID + "/queue"
	url := h.URL + path
	queueTestAction(t, url, "pause", "", 0)
	queued := queueTestSubmit(t, url, "restart", "restore me")
	s.Close()
	restored, err := New(Options{Store: store, EventDir: dir, DefaultCWD: t.TempDir(), Factory: func(_ context.Context, _ string, model protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(client, model.Model, 1024, "system"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	h2 := httptest.NewServer(restored)
	defer h2.Close()
	out := queueTestGet(t, h2.URL+path)
	if !out.Snapshot.Paused || len(out.Snapshot.Items) != 1 || out.Snapshot.Items[0].ID != queued.Receipt.ID {
		t.Fatalf("bad restored snapshot: %+v", out)
	}
	client.mu.Lock()
	calls := client.calls
	client.mu.Unlock()
	if calls != 0 {
		t.Fatalf("restored queue executed without resume: %d", calls)
	}
	queueTestAction(t, h2.URL+path, "resume", "", out.Snapshot.Revision)
	queueTestWait(t, h2.URL+path+"?client_request_id=restart", func(out queueResponse) bool { return out.Receipt.State == "completed" })
	current := queueTestGet(t, h2.URL+path)
	queueTestAction(t, h2.URL+path, "pause", "", current.Snapshot.Revision)
	file := filepath.Join(dir, sess.ID+".queue.json")
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0700); err != nil {
		t.Fatal(err)
	}
	before := queueTestGet(t, h2.URL+path)
	if code := doJSON(t, http.MethodPost, h2.URL+path, queueSubmit{Request: protocol.PromptRequest{ClientRequestID: "disk-failure", Prompt: "never accepted"}}, nil); code != 500 {
		t.Fatalf("disk failure=%d", code)
	}
	after := queueTestGet(t, h2.URL+path)
	if after.Snapshot.Revision != before.Snapshot.Revision || len(after.Snapshot.Items) != 0 {
		t.Fatalf("failed commit changed memory: %+v", after)
	}
}

func TestBackendQueueConcurrentReplayAndCAS(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, h := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	sess := createTestSession(t, h.URL)
	url := h.URL + "/v1/sessions/" + sess.ID + "/queue"
	queueTestAction(t, url, "pause", "", 0)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			var out queueResponse
			code := doJSON(t, http.MethodPost, url, queueSubmit{Request: protocol.PromptRequest{ClientRequestID: "same", Prompt: "once"}}, &out)
			if code != 200 && code != 202 {
				t.Errorf("concurrent replay status=%d", code)
			}
		})
	}
	wg.Wait()
	snapshot := queueTestGet(t, url)
	if len(snapshot.Snapshot.Items) != 1 || snapshot.Snapshot.Revision != 2 {
		t.Fatalf("duplicate acceptance: %+v", snapshot)
	}
	var mu sync.Mutex
	successes := 0
	for range 8 {
		wg.Go(func() {
			code := doJSON(t, http.MethodPost, url+"/actions", queueAction{Action: "remove", ItemID: snapshot.Snapshot.Items[0].ID, Revision: snapshot.Snapshot.Revision}, nil)
			mu.Lock()
			defer mu.Unlock()
			if code == 200 {
				successes++
			} else if code != 409 {
				t.Errorf("CAS status=%d", code)
			}
		})
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("CAS successes=%d", successes)
	}
}

func TestBackendQueueMalformedAndCapacity(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s, h := newTestServer(t, store, t.TempDir(), &scriptedClient{})
	sess := createTestSession(t, h.URL)
	url := h.URL + "/v1/sessions/" + sess.ID + "/queue"
	for _, input := range []queueSubmit{{}, {Request: protocol.PromptRequest{ClientRequestID: "x", Prompt: "hello"}, Policy: "unknown"}, {Request: protocol.PromptRequest{ClientRequestID: "x", Prompt: "hello"}, UploadedFilesJSON: `[{"path":"unresolved"}]`}, {Request: protocol.PromptRequest{ClientRequestID: "x", Prompt: "hello"}, DraftJSON: `[]`}} {
		if code := doJSON(t, http.MethodPost, url, input, nil); code != 400 {
			data, _ := json.Marshal(input)
			t.Fatalf("invalid input %s status=%d", data, code)
		}
	}
	rt, err := s.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseRuntime(rt)
	rt.mu.Lock()
	for i := range maxQueueReceipts {
		rt.queue.Receipts[fmt.Sprint(i)] = queueReceipt{ID: fmt.Sprint(i)}
	}
	rt.mu.Unlock()
	if code := doJSON(t, http.MethodPost, url, queueSubmit{Request: protocol.PromptRequest{ClientRequestID: "full", Prompt: "full"}}, nil); code != 429 {
		t.Fatalf("capacity=%d", code)
	}
}

func TestBackendQueueJournalAcceptanceRecoveryDoesNotReplay(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dir := t.TempDir()
	client := &scriptedClient{response: "must not run"}
	s, h := newTestServer(t, store, dir, client)
	sess := createTestSession(t, h.URL)
	path := "/v1/sessions/" + sess.ID + "/queue"
	queueTestAction(t, h.URL+path, "pause", "", 0)
	queued := queueTestSubmit(t, h.URL+path, "crash-window", "accepted before crash")
	rt, err := s.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	err = rt.publishEventLocked("accepted-run", protocol.EventRunAccepted, map[string]string{"client_request_id": "crash-window"})
	rt.mu.Unlock()
	s.releaseRuntime(rt)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	// Simulate a crash after the journal acceptance but before runs.json or the
	// queue dispatch receipt was committed.
	_ = os.Remove(filepath.Join(dir, sess.ID+".runs.json"))
	restored, err := New(Options{Store: store, EventDir: dir, DefaultCWD: t.TempDir(), Factory: func(_ context.Context, _ string, model protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(client, model.Model, 1024, "system"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	h2 := httptest.NewServer(restored)
	defer h2.Close()
	out := queueTestGet(t, h2.URL+path+"?client_request_id=crash-window")
	if out.Receipt.Run == nil || out.Receipt.Run.RunID != "accepted-run" || out.Receipt.State != "interrupted" {
		t.Fatalf("lost acceptance: %+v", out.Receipt)
	}
	queueTestAction(t, h2.URL+path, "resume", "", queued.Snapshot.Revision)
	queueTestWait(t, h2.URL+path, func(out queueResponse) bool { return len(out.Snapshot.Items) == 0 })
	client.mu.Lock()
	calls := client.calls
	client.mu.Unlock()
	if calls != 0 {
		t.Fatalf("accepted run replayed %d times", calls)
	}
	var duplicate queueResponse
	if code := doJSON(t, http.MethodPost, h2.URL+path, queueSubmit{Request: protocol.PromptRequest{ClientRequestID: "crash-window", Prompt: "accepted before crash"}}, &duplicate); code != 200 || duplicate.Receipt.State != "interrupted" {
		t.Fatalf("duplicate=%d %+v", code, duplicate)
	}
}

func TestBackendQueueLongPollWakeCancelAndShutdown(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	s, h := newTestServer(t, store, t.TempDir(), &scriptedClient{})
	sess := createTestSession(t, h.URL)
	url := h.URL + "/v1/sessions/" + sess.ID + "/queue"
	paused := queueTestAction(t, url, "pause", "", 0)
	wait := func(ctx context.Context, revision uint64) <-chan error {
		done := make(chan error, 1)
		go func() {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s?wait=1&after_revision=%d", url, revision), nil)
			resp, err := http.DefaultClient.Do(req)
			if resp != nil {
				resp.Body.Close()
			}
			done <- err
		}()
		return done
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := wait(ctx, paused.Snapshot.Revision)
	select {
	case err := <-done:
		t.Fatalf("long poll returned early: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel ignored")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel blocked")
	}
	done = wait(context.Background(), paused.Snapshot.Revision)
	queued := queueTestSubmit(t, url, "wake", "wake")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("mutation did not wake reader")
	}
	done = wait(context.Background(), queued.Snapshot.Revision)
	s.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked reader")
	}
}
