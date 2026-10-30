package approval

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// reviewServer is an OpenAI-compatible provider whose catalog lists the review model. The
// model is absent from cfg.Models, so the first Review resolves it through a catalog refresh
// (routing.ResolveWithRefreshContext). holdModels makes the first /models request wait until
// the caller's context is cancelled.
func reviewServer(t *testing.T, holdModels bool, chatStatus int, chatBody string) (*Reviewer, chan struct{}, *atomic.Int32) {
	t.Helper()
	t.Setenv("K_BRAIN_HOME", t.TempDir())
	modelsSeen := make(chan struct{}, 8)
	var modelsCalls, chatCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			n := modelsCalls.Add(1)
			modelsSeen <- struct{}{}
			if holdModels && n == 1 {
				<-r.Context().Done()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"object":"list","data":[{"id":"review-small","object":"model"}]}`)
		case "/v1/chat/completions":
			chatCalls.Add(1)
			if chatStatus != 0 {
				http.Error(w, chatBody, chatStatus)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}]}`, low)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	cfg := &config.Config{
		Providers:  map[string]config.Provider{"reviewer": {API: "openai-completions", BaseURL: srv.URL + "/v1", APIKey: "sk-review-secret-key-0123456789"}},
		AIApproval: config.AIApprovalConfig{Mode: "auto", Provider: "reviewer", Model: "review-small"},
	}
	r := New(t.Context(), cfg)
	if r.unavailable {
		t.Fatal("reviewer unavailable")
	}
	return r, modelsSeen, &chatCalls
}

// A review cancelled while the route is still being resolved (a stopped subagent's tool call,
// or the parallel call that happened to enter sync.Once first) is cached as the route error
// for the rest of the run. Every later review is then reported as "model could not be
// resolved", which LiveAgent shows as "找不到 AI 审查设置里的审查模型" although the model
// exists and the provider would answer.
func TestReviewAfterCancelledRouteResolutionDoesNotBlameTheModel(t *testing.T) {
	r, modelsSeen, chatCalls := reviewServer(t, true, 0, "")
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan Report, 1)
	go func() { done <- r.Review(ctx, Evidence{Phase: "action", Tool: "Bash", Action: "ls"}) }()
	select {
	case <-modelsSeen:
	case <-time.After(10 * time.Second):
		t.Fatal("catalog refresh never reached the provider")
	}
	cancel()
	first := <-done
	if first.Reason != "Review cancelled; manual approval required" {
		t.Fatalf("cancelled review reason = %q", first.Reason)
	}

	second := r.Review(t.Context(), Evidence{Phase: "action", Tool: "Bash", Action: "ls"})
	if strings.Contains(second.Reason, "could not be resolved") {
		t.Fatalf("a cancelled first review poisons the run: second reason = %q (chat calls %d); the model is configured and the provider lists it", second.Reason, chatCalls.Load())
	}
}

// Probe: a real provider HTTP error is classified by status and the upstream body (which here
// echoes the request's credential) never reaches the reason.
func TestReviewerHTTPFailureThroughRealClientHidesBody(t *testing.T) {
	r, _, chatCalls := reviewServer(t, false, http.StatusUnauthorized, `{"error":{"message":"invalid key sk-review-secret-key-0123456789"}}`)
	report := r.Review(t.Context(), Evidence{Phase: "action", Tool: "Bash", Action: "ls"})
	if report.Reason != "Reviewer request failed (HTTP 401); manual approval required" {
		t.Fatalf("reason = %q (chat calls %d)", report.Reason, chatCalls.Load())
	}
	if strings.Contains(report.Reason, "sk-") || strings.Contains(report.Description(), "sk-") {
		t.Fatalf("upstream body leaked: %q", report.Description())
	}
}
