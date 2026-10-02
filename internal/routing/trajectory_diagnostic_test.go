package routing

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestTrajectoryActualHTTPFailoverDiagnostics(t *testing.T) {
	ResetFailoverBreakers()
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "temporarily unavailable", 503) }))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"actual fallback\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer second.Close()
	primary, fallback := ai.New(first.URL, "private-primary-key"), ai.New(second.URL, "private-fallback-key")
	primary.MaxRetries = 1
	fallback.MaxRetries = 1
	client := &FailoverClient{candidates: []failoverCandidate{testCandidate("primary", primary, 1), testCandidate("fallback", fallback, 1)}}
	var events []ai.RuntimeDiagnostic
	ctx := ai.WithRuntimeDiagnosticObserver(context.Background(), func(event ai.RuntimeDiagnostic) { events = append(events, event) })
	msg, _, err := client.Stream(ctx, ai.Request{Model: "model", Messages: []ai.Message{{Role: "user", Content: "answer"}}}, nil, nil, nil)
	if err != nil || msg.Content != "actual fallback" {
		t.Fatalf("actual failover result=%+v err=%v", msg, err)
	}
	if len(events) != 3 || events[0].Kind != "attempt" || events[1].Kind != "attempt" || events[2].Kind != "failover" || events[2].From != "primary" || events[2].To != "fallback" || events[1].Endpoint != second.URL {
		t.Fatalf("diagnostics=%+v", events)
	}
}
