package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

type streamFlushRecorder struct {
	*httptest.ResponseRecorder
	chunks []string
	read   int
}

func (w *streamFlushRecorder) Flush() {
	w.chunks = append(w.chunks, w.Body.String()[w.read:])
	w.read = w.Body.Len()
	w.ResponseRecorder.Flush()
}

func TestStreamReplayFlushesFirstDeltasAndBoundedBatches(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, httpServer := newTestServer(t, store, t.TempDir(), &scriptedClient{})
	sess := createTestSession(t, httpServer.URL)
	rt, err := s.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseRuntime(rt)
	rt.mu.Lock()
	rt.runID = "replay-run"
	rt.mu.Unlock()
	for _, typ := range []string{protocol.EventThinkingDelta, protocol.EventTextDelta} {
		if _, err := rt.publish(typ, protocol.TextDelta{Text: "first"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	for range 70 {
		if _, err := rt.publish(protocol.EventTextDelta, protocol.TextDelta{Text: "more"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := rt.publish(protocol.EventRunCompleted, protocol.RunTerminal{}, ""); err != nil {
		t.Fatal(err)
	}
	w := &streamFlushRecorder{ResponseRecorder: httptest.NewRecorder()}
	s.events(w, httptest.NewRequest(http.MethodGet, "/events", nil), sess.ID)
	if w.Header().Get("X-Accel-Buffering") != "no" || !strings.Contains(w.Header().Get("Cache-Control"), "no-transform") {
		t.Fatalf("buffering headers: %v", w.Header())
	}
	if len(w.chunks) < 6 || w.chunks[0] != "" {
		t.Fatalf("missing early/bounded flushes: %d", len(w.chunks))
	}
	for i, typ := range []string{protocol.EventThinkingDelta, protocol.EventTextDelta} {
		if !strings.Contains(w.chunks[i+1], typ) || strings.Count(w.chunks[i+1], "data: ") != 1 {
			t.Fatalf("first delta was batched with later events: %q", w.chunks[i+1])
		}
	}
	for _, chunk := range w.chunks {
		if count := strings.Count(chunk, "data: "); count > 32 {
			t.Fatalf("unbounded replay batch: %d events", count)
		}
	}
	events := readSSE(t, w.Result())
	if len(events) != 73 || events[len(events)-1].Type != protocol.EventRunCompleted {
		t.Fatalf("replay lost events: %d", len(events))
	}
	for i, event := range events {
		if event.Seq != int64(i+1) {
			t.Fatalf("sequence at %d: %d", i, event.Seq)
		}
	}
	resumed := httptest.NewRecorder()
	s.events(resumed, httptest.NewRequest(http.MethodGet, "/events?after_seq=32", nil), sess.ID)
	tail := readSSE(t, resumed.Result())
	if len(tail) != 41 || tail[0].Seq != 33 {
		t.Fatalf("bad resumed replay: %+v", tail)
	}
}

type gatedStreamClient struct {
	scriptedClient
	release chan struct{}
}

func (c *gatedStreamClient) Clone() ai.Client { return c }
func (c *gatedStreamClient) Stream(ctx context.Context, _ ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	text("first")
	select {
	case <-c.release:
		text(" last")
		return ai.Message{Role: "assistant", Content: "first last", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
	case <-ctx.Done():
		return ai.Message{}, ai.Usage{}, ctx.Err()
	}
}

func TestStreamDeliversTextBeforeModelCompletion(t *testing.T) {
	client := &gatedStreamClient{release: make(chan struct{})}
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Store: store, EventDir: t.TempDir(), MemoryRoot: t.TempDir(), DefaultCWD: t.TempDir(), Factory: func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
		a := agent.New(client, selected.Model, 1024, "system")
		a.ModelName, a.Provider = selected.Model, selected.Provider
		return a, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(s)
	t.Cleanup(func() { _ = s.Close(); h.Close() })
	sess := createTestSession(t, h.URL)
	accepted := runRequest(t, h.URL, sess.ID, "first-delta", "hi")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.URL+"/v1/sessions/"+sess.ID+"/events?after_seq="+strconv.FormatInt(accepted.AcceptedSeq, 10), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	found := false
	stages := []string{}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event protocol.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == protocol.EventRunProgress {
			var payload struct {
				Stage string `json:"stage"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			stages = append(stages, payload.Stage)
		}
		if event.Type == protocol.EventTextDelta {
			if strings.Join(stages, ",") != "preparing,waiting_model,responding" {
				t.Fatalf("missing ordered progress before first text: %v", stages)
			}
			found = true
			break
		}
		if event.Type == protocol.EventRunCompleted {
			t.Fatal("completed before first text")
		}
	}
	if !found {
		t.Fatalf("first text not delivered while model waited: %v", scanner.Err())
	}
	close(client.release)
}
