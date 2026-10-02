package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/prompts"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/routing"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestBackendRunFallsBackAndPersistsContinuousSessionHistory(t *testing.T) {
	routing.ResetFailoverBreakers()
	var primaryCalls, fallbackCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"temporary upstream"}}`)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"fallback answer\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer fallback.Close()

	cfg := failoverTestConfig(primary.URL, fallback.URL)
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	factory := func(ctx context.Context, cwd string, selected protocol.ModelRef) (*agent.Agent, error) {
		route, routeErr := routing.ResolveFailoverRouteContext(ctx, cfg, selected.Model, selected.Provider, false)
		if routeErr != nil {
			return nil, routeErr
		}
		a := agent.New(route.Client, route.APIModel, route.MaxOutput, prompts.Build(cwd, time.Now()), agent.WithExperimental(nil))
		a.ModelName, a.Provider, a.ContextLimit, a.WorkingDir = route.ModelName, route.ProviderName, route.ContextLimit, cwd
		return a, nil
	}
	server, err := New(Options{Store: store, Factory: factory, EventDir: t.TempDir(), DefaultCWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	cwd := t.TempDir()
	var created protocol.Session
	resp := postJSON(t, http.DefaultClient, httpServer.URL+"/v1/sessions", protocol.CreateSessionRequest{CWD: cwd, Model: protocol.ModelRef{Provider: "primary", Model: "model"}}, &created)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d", resp.StatusCode)
	}
	accepted := runRequest(t, httpServer.URL, created.ID, "fallback-run", "answer this")
	resp, err = http.Get(httpServer.URL + "/v1/sessions/" + created.ID + "/events?after_seq=" + fmt.Sprint(accepted.AcceptedSeq-1))
	if err != nil {
		t.Fatal(err)
	}
	events := readSSE(t, resp)
	if !hasEvent(events, protocol.EventRunCompleted) {
		t.Fatalf("run did not complete: %+v", events)
	}
	var view protocol.Session
	resp, err = http.Get(httpServer.URL + "/v1/sessions/" + created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if len(view.Messages) < 2 || view.Messages[len(view.Messages)-1].Content[0].Text != "fallback answer" {
		t.Fatalf("session history = %+v", view.Messages)
	}
	if primaryCalls.Load() != 1 || fallbackCalls.Load() != 1 {
		t.Fatalf("provider calls = primary %d fallback %d", primaryCalls.Load(), fallbackCalls.Load())
	}
}

func TestBackendRunDoesNotFallbackAfterHTTPStreamCommit(t *testing.T) {
	routing.ResetFailoverBreakers()
	var fallbackCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"duplicate\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer fallback.Close()

	cfg := failoverTestConfig(primary.URL, fallback.URL)
	route, err := routing.ResolveFailoverRouteContext(t.Context(), cfg, "model", "primary", false)
	if err != nil {
		t.Fatal(err)
	}
	var text string
	_, _, err = route.Client.Stream(t.Context(), ai.Request{Model: "model"}, func(delta string) { text += delta }, nil, nil)
	if err == nil || !strings.Contains(text, "partial") || strings.Contains(text, "duplicate") || fallbackCalls.Load() != 0 {
		t.Fatalf("committed stream = text=%q err=%v fallbackCalls=%d", text, err, fallbackCalls.Load())
	}
}

func failoverTestConfig(primaryURL, fallbackURL string) *config.Config {
	policy := map[string]any{"mode": "off", "failover": map[string]any{"maxSwitches": 1, "failureThreshold": 2, "cooldownSeconds": 60}}
	return &config.Config{
		DefaultModel: "model",
		Providers: map[string]config.Provider{
			"primary":  {API: ai.APIChatCompletions, BaseURL: primaryURL, APIKey: "key", RetryPolicy: policy},
			"fallback": {API: ai.APIChatCompletions, BaseURL: fallbackURL, APIKey: "key", RetryPolicy: policy},
		},
		Models: map[string]config.Model{"model": {ID: "model", Providers: []string{"primary", "fallback"}, Context: 128000, MaxOut: 1024}},
	}
}

func hasEvent(events []protocol.Event, typ string) bool {
	for _, event := range events {
		if event.Type == typ {
			return true
		}
	}
	return false
}
