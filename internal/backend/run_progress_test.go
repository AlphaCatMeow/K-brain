package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestRunProgressStagesAndFirstOffsets(t *testing.T) {
	var payloads []map[string]any
	p := &runProgress{started: time.Now().Add(-time.Second), publish: func(typ string, payload any, _ string) (protocol.Event, error) {
		if typ != protocol.EventRunProgress {
			t.Fatalf("unexpected type %s", typ)
		}
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, value)
		return protocol.Event{}, nil
	}}
	for _, stage := range []string{"preparing", "waiting_model", "thinking", "thinking", "responding", "responding", "tool", "waiting_model", "responding", "persisting"} {
		p.phase(stage)
	}
	if len(payloads) != 8 {
		t.Fatalf("duplicate phases published: %d", len(payloads))
	}
	firstThinking, firstText := payloads[2]["first_thinking_ms"], payloads[3]["first_text_ms"]
	for i, payload := range payloads {
		if payload["elapsed_ms"].(float64) < 0 {
			t.Fatal("negative elapsed")
		}
		if i >= 2 && payload["first_thinking_ms"] != firstThinking {
			t.Fatal("first reasoning offset changed")
		}
		if i >= 3 && payload["first_text_ms"] != firstText {
			t.Fatal("first text offset changed")
		}
	}
	if payloads[0]["model_request_ms"] != nil || payloads[1]["model_request_ms"] == nil {
		t.Fatal("request offset not scoped to model request")
	}
	if payloads[5]["model_request_ms"].(float64) < payloads[1]["model_request_ms"].(float64) {
		t.Fatal("request offset moved backwards")
	}
}

func TestStreamIdleHeartbeatDoesNotConsumeSequence(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, h := newTestServer(t, store, t.TempDir(), &scriptedClient{})
	sess := createTestSession(t, h.URL)
	rt, err := s.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseRuntime(rt)
	rt.mu.Lock()
	rt.runID = "idle"
	rt.cancel = func() {}
	seq := rt.nextSeq
	rt.mu.Unlock()
	heartbeats := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.eventsWithHeartbeat(w, r, sess.ID, 10*time.Millisecond)
	}))
	defer heartbeats.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, heartbeats.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() || scanner.Text() != ": heartbeat" {
		t.Fatalf("missing heartbeat: %s %v", scanner.Text(), scanner.Err())
	}
	cancel()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.nextSeq != seq {
		t.Fatalf("heartbeat consumed sequence: %d -> %d", seq, rt.nextSeq)
	}
}
