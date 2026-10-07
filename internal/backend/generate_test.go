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
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestUnifiedGatewaySwitchesFourProtocolsWithOneHistory(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "kept tool output") || !strings.Contains(string(body), "interrupted") {
			t.Errorf("canonical tool history missing in %s request", r.URL.Path)
		}
		if strings.Contains(string(body), "private-display-reasoning") || strings.Contains(string(body), "provider_replay") {
			t.Errorf("foreign reasoning leaked: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		switch {
		case r.URL.Path == "/chat/completions":
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"unified answer\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":2,\"prompt_cache_hit_tokens\":80}}\n\ndata: [DONE]\n\n")
		case r.URL.Path == "/responses":
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"unified answer\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":100,\"output_tokens\":2,\"input_tokens_details\":{\"cached_tokens\":80}}}}\n\n")
		case r.URL.Path == "/messages":
			fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":20,\"cache_read_input_tokens\":80}}}\n\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"unified answer\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\ndata: {\"type\":\"message_stop\"}\n\n")
		case strings.Contains(r.URL.Path, ":streamGenerateContent"):
			fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"unified answer\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":2,\"cachedContentTokenCount\":80}}\n\n")
		default:
			t.Errorf("unexpected upstream path %s", r.URL.Path)
		}
	}))
	defer upstream.Close()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	factory := func(_ context.Context, _ string, m protocol.ModelRef) (*agent.Agent, error) {
		c, err := ai.NewClient(ai.ClientOptions{API: m.Provider, BaseURL: upstream.URL, APIKey: "upstream-secret"})
		if err != nil {
			return nil, err
		}
		return agent.New(c, "fixture", 512, ""), nil
	}
	s, err := New(Options{Store: store, Factory: factory, MemoryRoot: t.TempDir(), Token: "local-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	apis := []string{ai.APIChatCompletions, ai.APIResponses, ai.APIMessages, ai.APIGemini}
	history := []protocol.Message{
		{Role: "system", Content: []protocol.ContentBlock{{Type: "text", Text: "stable rules"}}},
		{Role: "user", Content: []protocol.ContentBlock{{Type: "text", Text: "question"}}},
		{Role: "assistant", Content: []protocol.ContentBlock{{Type: "thinking", Text: "private-display-reasoning"}}, ToolCalls: []protocol.ToolCall{{ID: "call1", Name: "lookup", Arguments: json.RawMessage(`{}`)}}},
		{Role: "tool", ToolCallID: "call1", Content: []protocol.ContentBlock{{Type: "text", Text: "kept tool output"}}},
		{Role: "assistant", ToolCalls: []protocol.ToolCall{{ID: "unfinished", Name: "lookup", Arguments: json.RawMessage(`{}`)}}},
		{Role: "user", Content: []protocol.ContentBlock{{Type: "text", Text: "continue with selected model"}}},
	}
	for _, stream := range []bool{false, true} {
		for _, api := range apis {
			t.Run(fmt.Sprintf("%s/stream=%v", api, stream), func(t *testing.T) {
				body, _ := json.Marshal(protocol.GenerateRequest{Model: protocol.ModelRef{Provider: api, Model: "fixture"}, Messages: history, Stream: stream, Tools: []protocol.ToolDefinition{{Name: "lookup", Parameters: json.RawMessage(`{"type":"object"}`)}}})
				r := httptest.NewRequest("POST", "/v1/generate", strings.NewReader(string(body)))
				r.Header.Set("Authorization", "Bearer local-token")
				w := httptest.NewRecorder()
				s.ServeHTTP(w, r)
				if w.Code != 200 || !strings.Contains(w.Body.String(), "unified answer") || strings.Contains(w.Body.String(), "upstream-secret") {
					t.Fatalf("gateway: %d %s", w.Code, w.Body.String())
				}
				if stream {
					if !strings.Contains(w.Body.String(), `"type":"request.completed"`) {
						t.Fatalf("missing terminal event: %s", w.Body.String())
					}
				} else {
					var response protocol.GenerateResponse
					if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					if response.Message.Role != "assistant" || response.Model.Provider != api {
						t.Fatalf("wrong canonical response: %+v", response)
					}
					if response.Usage == nil || response.Usage.InputTokens != 100 || response.Usage.CachedTokens != 80 {
						t.Fatalf("usage not normalized: %+v", response.Usage)
					}
					history = append(history, response.Message, protocol.Message{Role: "user", Content: []protocol.ContentBlock{{Type: "text", Text: "continue after switching model"}}})
				}
			})
		}
	}
	if requests.Load() != 8 {
		t.Fatalf("expected one upstream call per request, got %d", requests.Load())
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "/v1/generate", strings.NewReader(`{}`)))
	if w.Code != 401 || requests.Load() != 8 {
		t.Fatal("gateway authentication bypass")
	}
	// LA's persistent session path must share the same adapter behavior.
	local := func(path string, input, output any, status int) {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
		r.Header.Set("Authorization", "Bearer local-token")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), output); err != nil {
			t.Fatal(err)
		}
	}
	var sess protocol.Session
	local("/v1/sessions", protocol.CreateSessionRequest{CWD: t.TempDir(), Model: protocol.ModelRef{Provider: apis[0], Model: "fixture"}, Messages: history}, &sess, http.StatusCreated)
	for i, api := range apis {
		var accepted protocol.RunAccepted
		selected := protocol.ModelRef{Provider: api, Model: "fixture"}
		local("/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ClientRequestID: fmt.Sprintf("switch-%d", i), Prompt: "continue", Model: &selected}, &accepted, http.StatusAccepted)
		runtime, err := s.loadRuntimeByID(sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = waitForCanonicalRun(runtime, accepted.RunID, ctx)
		cancel()
		if err != nil {
			t.Fatalf("persistent session switch to %s: %v", api, err)
		}
	}
	if requests.Load() != 12 {
		t.Fatalf("persistent session made unexpected calls: %d", requests.Load())
	}
}

func TestUnifiedGatewayValidationAndProviderErrorRedaction(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	client := &scriptedClient{streamErr: fmt.Errorf("provider-secret https://private-upstream.test")}
	s := &Server{factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(client, "m", 100, ""), nil
	}}
	for _, body := range []string{
		`{"model":{"model":"m"},"messages":[]}`,
		`{"model":{"model":"m"},"messages":[{"role":"tool","content":[{"type":"text","text":"orphan"}]}]}`,
		`{"model":{"model":"m"},"messages":[{"role":"user","content":[{"type":"image","image_url":"data:image/png;base64,aQ==","mime_type":"image/png"}]}]}`,
		`{"model":{"model":"m"},"messages":[{"role":"user"}],"tools":[{"name":"bad name","parameters":{}}]}`,
	} {
		w := httptest.NewRecorder()
		s.generate(w, httptest.NewRequest("POST", "/v1/generate", strings.NewReader(body)))
		if w.Code != 400 {
			t.Fatalf("invalid accepted: %d %s", w.Code, w.Body.String())
		}
	}
	for _, stream := range []bool{false, true} {
		body := fmt.Sprintf(`{"model":{"model":"m"},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"stream":%t}`, stream)
		w := httptest.NewRecorder()
		s.generate(w, httptest.NewRequest("POST", "/v1/generate", strings.NewReader(body)))
		if strings.Contains(w.Body.String(), "provider-secret") || strings.Contains(w.Body.String(), "private-upstream") {
			t.Fatal("upstream details leaked")
		}
		if stream && !strings.Contains(w.Body.String(), "request.failed") || !stream && w.Code != 502 {
			t.Fatalf("bad failure: %d %s", w.Code, w.Body.String())
		}
	}
}

type gatewayToolClient struct{ scriptedClient }

func (c *gatewayToolClient) Clone() ai.Client { return c }
func (c *gatewayToolClient) Stream(_ context.Context, _ ai.Request, _, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	call := ai.ToolCall{ID: "call-1", Type: "function"}
	call.Function.Name = "Bash"
	call.Function.Arguments = `{"command":"must-not-execute"}`
	return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}, StopReason: ai.StopReasonToolUse}, ai.Usage{PromptTokens: 10}, nil
}

func TestUnifiedGatewayReturnsToolsAsData(t *testing.T) {
	s := &Server{factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(&gatewayToolClient{}, "m", 100, ""), nil
	}}
	w := httptest.NewRecorder()
	s.generate(w, httptest.NewRequest("POST", "/v1/generate", strings.NewReader(`{"model":{"model":"m"},"messages":[{"role":"user","content":[{"type":"text","text":"fixture"}]}]}`)))
	var out protocol.GenerateResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(out.Message.ToolCalls) != 1 || out.Message.ToolCalls[0].Name != "Bash" || out.Message.ToolCalls[0].ID != "call-1" {
		t.Fatalf("tool result lost: %s", w.Body.String())
	}
}

func TestUnifiedGatewayRejectsUnknownAndDisabledRoutes(t *testing.T) {
	s := &Server{settings: NewSettingsStore(&config.Config{
		Providers: map[string]config.Provider{"p": {ActiveModels: []string{"enabled"}}},
		Models: map[string]config.Model{
			"disabled": {Providers: []string{"p"}},
			"enabled":  {Providers: []string{"p"}},
		},
	}, nil)}
	for _, model := range []string{"unknown", "disabled", "enabled"} {
		w := httptest.NewRecorder()
		body := fmt.Sprintf(`{"model":{"provider":"p","model":%q},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`, model)
		s.generate(w, httptest.NewRequest("POST", "/v1/generate", strings.NewReader(body)))
		want := http.StatusBadRequest
		if model == "enabled" {
			want = http.StatusServiceUnavailable
		}
		if w.Code != want {
			t.Fatalf("%s: %d %s", model, w.Code, w.Body.String())
		}
	}
}

type gatewayInvalidClient struct{ scriptedClient }

func (c *gatewayInvalidClient) Clone() ai.Client { return c }
func (c *gatewayInvalidClient) Stream(_ context.Context, _ ai.Request, _, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	call := ai.ToolCall{ID: "invalid-provider-call", Type: "function"}
	call.Function.Name = "lookup"
	call.Function.Arguments = `{"secret-invalid-json"`
	return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}}, ai.Usage{}, nil
}

func TestUnifiedGatewayRejectsMalformedProviderOutput(t *testing.T) {
	s := &Server{factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(&gatewayInvalidClient{}, "m", 100, ""), nil
	}}
	for _, stream := range []bool{false, true} {
		w := httptest.NewRecorder()
		body := fmt.Sprintf(`{"model":{"model":"m"},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"stream":%t}`, stream)
		s.generate(w, httptest.NewRequest("POST", "/v1/generate", strings.NewReader(body)))
		if strings.Contains(w.Body.String(), "secret-invalid-json") || strings.Contains(w.Body.String(), "request.completed") {
			t.Fatalf("invalid response escaped: %s", w.Body.String())
		}
		if stream && !strings.Contains(w.Body.String(), "request.failed") || !stream && w.Code != http.StatusBadGateway {
			t.Fatalf("bad terminal error: %d %s", w.Code, w.Body.String())
		}
	}
}

type gatewayCancelClient struct {
	scriptedClient
	cancelled bool
}

func (c *gatewayCancelClient) Clone() ai.Client { return c }
func (c *gatewayCancelClient) Stream(ctx context.Context, _ ai.Request, _, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.cancelled = ctx.Err() == context.Canceled
	return ai.Message{}, ai.Usage{}, ctx.Err()
}

func TestUnifiedGatewayPropagatesRequestCancellation(t *testing.T) {
	client := &gatewayCancelClient{}
	s := &Server{factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(client, "m", 100, ""), nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/generate", strings.NewReader(`{"model":{"model":"m"},"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)).WithContext(ctx)
	s.generate(w, r)
	if !client.cancelled || w.Code != http.StatusRequestTimeout {
		t.Fatalf("cancellation not propagated: %d %s", w.Code, w.Body.String())
	}
}
