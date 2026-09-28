package protocol

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

type providerIntegrationStage struct {
	name  string
	model string
	new   func(string) ai.Client
	write func(http.ResponseWriter)
	check func([]byte) error
}

func TestProviderAdaptersPreserveCanonicalHistoryAcrossChain(t *testing.T) {
	const (
		callID     = "call-history-1"
		callName   = "read_file"
		callArgs   = `{"path":"notes.txt"}`
		toolOutput = "contents from tool"
	)
	history := []Message{
		{Role: RoleUser, Content: []ContentBlock{{Type: ContentText, Text: "Please inspect notes.txt."}}},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: callID, Name: callName, Arguments: json.RawMessage(callArgs)}}},
		{Role: RoleTool, ToolCallID: callID, Name: callName, Content: []ContentBlock{{Type: ContentText, Text: toolOutput}}},
		{Role: RoleUser, Content: []ContentBlock{{Type: ContentText, Text: "Summarize it."}}},
	}
	for i := range history {
		if err := history[i].Validate(); err != nil {
			t.Fatalf("initial history[%d]: %v", i, err)
		}
	}

	stages := []providerIntegrationStage{
		{
			name:  "chat-completions",
			model: "chat-model",
			new: func(url string) ai.Client {
				return ai.New(url, "test-key")
			},
			write: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"chat\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":3,\"prompt_tokens_details\":{\"cached_tokens\":2}}}\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
			},
			check: checkChatHistory,
		},
		{
			name:  "responses",
			model: "responses-model",
			new: func(url string) ai.Client {
				return ai.NewResponses(url, "test-key")
			},
			write: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"responses\"}\n\n")
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"text\":\"responses\"}]}],\"usage\":{\"input_tokens\":12,\"output_tokens\":4,\"input_tokens_details\":{\"cached_tokens\":3,\"cache_write_tokens\":1}}}}\n\n")
			},
			check: checkResponsesHistory,
		},
		{
			name:  "gemini",
			model: "gemini-test",
			new: func(url string) ai.Client {
				return ai.NewGemini(url, "test-key")
			},
			write: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"gemini\"}]}}],\"usageMetadata\":{\"promptTokenCount\":13,\"candidatesTokenCount\":5,\"cachedContentTokenCount\":4}}\n\n")
				fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":13,\"candidatesTokenCount\":5,\"cachedContentTokenCount\":4}}\n\n")
			},
			check: checkGeminiHistory,
		},
		{
			name:  "chat-completions-final",
			model: "chat-final-model",
			new: func(url string) ai.Client {
				return ai.New(url, "test-key")
			},
			write: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"chat-final\"}}]}\n\n")
				fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":14,\"completion_tokens\":6}}\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
			},
			check: checkChatHistory,
		},
	}
	wantText := []string{"chat", "responses", "gemini", "chat-final"}
	wantRawStop := []string{"stop", "completed", "stop", "stop"}
	wantUsage := []Usage{
		{InputTokens: 11, OutputTokens: 3, CachedTokens: 2},
		{InputTokens: 12, OutputTokens: 4, CachedTokens: 3, CacheWriteTokens: 1},
		{InputTokens: 13, OutputTokens: 5, CachedTokens: 4},
		{InputTokens: 14, OutputTokens: 6},
	}

	current := history
	for i, stage := range stages {
		stage := stage
		t.Run(stage.name, func(t *testing.T) {
			var requests int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if requests != 1 {
					http.Error(w, "unexpected second request", http.StatusBadRequest)
					return
				}
				if r.Header.Get("Authorization") != "Bearer test-key" && stage.name != "gemini" {
					http.Error(w, "missing authorization", http.StatusUnauthorized)
					return
				}
				if stage.name == "gemini" {
					if r.URL.Query().Get("key") != "test-key" {
						http.Error(w, "missing Gemini key", http.StatusUnauthorized)
						return
					}
					if r.URL.Path != "/v1beta/models/gemini-test:streamGenerateContent" {
						http.Error(w, "wrong Gemini endpoint", http.StatusNotFound)
						return
					}
				} else {
					wantPath := "/chat/completions"
					if stage.name == "responses" {
						wantPath = "/responses"
					}
					if r.URL.Path != wantPath {
						http.Error(w, "wrong endpoint", http.StatusNotFound)
						return
					}
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				if err := stage.check(body); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				stage.write(w)
			}))
			defer srv.Close()

			requestMessages := make([]ai.Message, 0, len(current))
			for j, message := range current {
				converted, err := message.ToAIMessage()
				if err != nil {
					t.Fatalf("history conversion[%d]: %v", j, err)
				}
				requestMessages = append(requestMessages, converted)
			}
			var text strings.Builder
			got, usage, err := stage.new(srv.URL).Stream(t.Context(), ai.Request{
				Model: stage.model, Messages: requestMessages,
			}, func(delta string) { text.WriteString(delta) }, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			got.Usage = &usage
			if got.Content != wantText[i] || text.String() != wantText[i] {
				t.Fatalf("text = %q, callback = %q, want %q", got.Content, text.String(), wantText[i])
			}
			if got.StopReason != ai.StopReasonStop || got.RawStopReason != wantRawStop[i] {
				t.Fatalf("terminal = %q/%q, want stop/%s", got.StopReason, got.RawStopReason, wantRawStop[i])
			}
			canonical, err := FromAIMessageValidated(got)
			if err != nil {
				t.Fatal(err)
			}
			assertCanonicalUsage(t, canonical, wantUsage[i])
			if len(canonical.Content) != 1 || canonical.Content[0].Text != wantText[i] || canonical.StopReason != string(ai.StopReasonStop) {
				t.Fatalf("canonical result = %+v", canonical)
			}
			assertCanonicalToolHistory(t, current, callID, callName, callArgs, toolOutput)
			next := append([]Message(nil), current...)
			next = append(next, canonical)
			current = next
		})
	}
}

func TestGeminiToolRoundsSwitchToChatWithoutDuplicateCanonicalIDs(t *testing.T) {
	var geminiRequests []map[string]any
	var chatRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		if r.URL.Path == "/v1beta/models/gemini-test:streamGenerateContent" {
			var request map[string]any
			if err := json.Unmarshal(body, &request); err != nil {
				t.Fatal(err)
			}
			geminiRequests = append(geminiRequests, request)
			w.Header().Set("Content-Type", "text/event-stream")
			if len(geminiRequests) == 2 {
				encoded, _ := json.Marshal(request["contents"])
				if !strings.Contains(string(encoded), `"thoughtSignature":"sig-round-1"`) || !strings.Contains(string(encoded), `"id":"gemini-call-`) {
					t.Errorf("Gemini follow-up omitted thought signature or call ID: %s", encoded)
				}
				fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"read_file\",\"args\":{\"path\":\"two.txt\"},\"thoughtSignature\":\"sig-round-2\"}}]}}]}\n\n")
				fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")
				return
			}
			if len(geminiRequests) == 3 {
				encoded, _ := json.Marshal(request["contents"])
				if !strings.Contains(string(encoded), `"thoughtSignature":"sig-round-2"`) || !strings.Contains(string(encoded), `"id":"gemini-call-`) {
					t.Errorf("new Gemini client did not recover second-round signature: %s", encoded)
				}
				fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"recovered\"}]}}]}\n\n")
				fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")
				return
			}
			fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"name\":\"read_file\",\"args\":{\"path\":\"one.txt\"},\"thoughtSignature\":\"sig-round-1\"}}]}}]}\n\n")
			fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n")
			return
		}
		if r.URL.Path == "/chat/completions" {
			if err := json.Unmarshal(body, &chatRequest); err != nil {
				t.Fatal(err)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"switched\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	gemini, err := ai.NewClient(ai.ClientOptions{API: ai.APIGemini, BaseURL: server.URL, APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	current := []Message{{Role: RoleUser, Content: []ContentBlock{{Type: ContentText, Text: "read one"}}}}
	var calls []ToolCall
	for round := 0; round < 2; round++ {
		requestMessages := make([]ai.Message, 0, len(current))
		for _, message := range current {
			converted, err := message.ToAIMessage()
			if err != nil {
				t.Fatal(err)
			}
			requestMessages = append(requestMessages, converted)
		}
		response, _, err := gemini.Stream(t.Context(), ai.Request{Model: "gemini-test", Messages: requestMessages}, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		canonical := FromAIMessage(response)
		if len(canonical.ToolCalls) != 1 {
			t.Fatalf("round %d canonical calls = %+v", round+1, canonical.ToolCalls)
		}
		if round > 0 && canonical.ToolCalls[0].ID == calls[0].ID {
			t.Fatalf("round %d reused tool ID %q", round+1, canonical.ToolCalls[0].ID)
		}
		calls = append(calls, canonical.ToolCalls[0])
		current = append(current, canonical, Message{Role: RoleTool, ToolCallID: canonical.ToolCalls[0].ID, Name: canonical.ToolCalls[0].Name, Content: []ContentBlock{{Type: ContentText, Text: fmt.Sprintf("result %d", round+1)}}})
		current = append(current, Message{Role: RoleUser, Content: []ContentBlock{{Type: ContentText, Text: fmt.Sprintf("read %d", round+2)}}})
	}

	chat, err := ai.NewClient(ai.ClientOptions{API: ai.APIChatCompletions, BaseURL: server.URL, APIKey: "test-key"})
	if err != nil {
		t.Fatal(err)
	}
	requestMessages := make([]ai.Message, 0, len(current))
	for _, message := range current {
		converted, err := message.ToAIMessage()
		if err != nil {
			t.Fatal(err)
		}
		requestMessages = append(requestMessages, converted)
	}
	chatResponse, _, err := chat.Stream(t.Context(), ai.Request{Model: "chat-model", Messages: requestMessages}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	requestMessages = append(requestMessages, chatResponse)
	newGemini := ai.NewGemini(server.URL, "test-key")
	if recovered, _, err := newGemini.Stream(t.Context(), ai.Request{Model: "gemini-test", Messages: requestMessages}, nil, nil, nil); err != nil {
		t.Fatal(err)
	} else if recovered.Content != "recovered" {
		t.Fatalf("new Gemini follow-up = %+v", recovered)
	}
	var wire struct {
		Messages []struct {
			ToolCalls []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	encoded, err := json.Marshal(chatRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, message := range wire.Messages {
		for _, call := range message.ToolCalls {
			if call.ID == "" || seen[call.ID] {
				t.Fatalf("Chat history has missing or duplicate tool ID %q: %s", call.ID, encoded)
			}
			seen[call.ID] = true
		}
	}
	if len(seen) != 2 || !seen[calls[0].ID] || !seen[calls[1].ID] {
		t.Fatalf("Chat history IDs = %v, want %v and %v", seen, calls[0].ID, calls[1].ID)
	}
}

func TestProviderAdaptersSurfaceErrorsWithCanonicalUsage(t *testing.T) {
	cases := []struct {
		name   string
		new    func(string) ai.Client
		write  func(http.ResponseWriter)
		wantIn int
	}{
		{
			name: "chat",
			new:  func(url string) ai.Client { return ai.New(url, "test-key") },
			write: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":17,\"completion_tokens\":2}}\n\n")
				fmt.Fprint(w, "data: {\"error\":{\"message\":\"fixture boom\"}}\n\n")
			},
			wantIn: 17,
		},
		{
			name: "responses",
			new:  func(url string) ai.Client { return ai.NewResponses(url, "test-key") },
			write: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"usage\":{\"input_tokens\":18,\"output_tokens\":2}}}\n\n")
				fmt.Fprint(w, "event: error\ndata: {\"message\":\"fixture boom\"}\n\n")
			},
			wantIn: 18,
		},
		{
			name: "gemini",
			new:  func(url string) ai.Client { return ai.NewGemini(url, "test-key") },
			write: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"usageMetadata\":{\"promptTokenCount\":19,\"candidatesTokenCount\":2}}\n\n")
				fmt.Fprint(w, "data: {\"error\":{\"message\":\"fixture boom\"},\"usageMetadata\":{\"promptTokenCount\":19,\"candidatesTokenCount\":2}}\n\n")
			},
			wantIn: 19,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.name == "gemini" {
					if r.URL.Query().Get("key") != "test-key" {
						http.Error(w, "missing key", http.StatusUnauthorized)
						return
					}
				} else if r.Header.Get("Authorization") != "Bearer test-key" {
					http.Error(w, "missing authorization", http.StatusUnauthorized)
					return
				}
				tc.write(w)
			}))
			defer srv.Close()
			msg, usage, err := tc.new(srv.URL).Stream(t.Context(), ai.Request{Model: "model"}, nil, nil, nil)
			if err == nil || !strings.Contains(err.Error(), "fixture boom") {
				t.Fatalf("error = %v", err)
			}
			if msg.Content != "" || len(msg.ToolCalls) != 0 || usage.PromptTokens != tc.wantIn || usage.CompletionTokens != 2 {
				t.Fatalf("message = %+v, usage = %+v", msg, usage)
			}
		})
	}
}

func TestProviderAdaptersMapOutputLimitsToCanonicalTerminal(t *testing.T) {
	cases := []struct {
		name string
		new  func(string) ai.Client
		body string
		raw  string
	}{
		{
			name: "chat",
			new:  func(url string) ai.Client { return ai.New(url, "test-key") },
			body: "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n",
			raw:  "length",
		},
		{
			name: "responses",
			new:  func(url string) ai.Client { return ai.NewResponses(url, "test-key") },
			body: "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\ndata: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n",
			raw:  "incomplete.max_output_tokens",
		},
		{
			name: "gemini",
			new:  func(url string) ai.Client { return ai.NewGemini(url, "test-key") },
			body: "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial\"}]}}]}\n\ndata: {\"candidates\":[{\"finishReason\":\"MAX_TOKENS\"}]}\n\n",
			raw:  "length",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.name == "gemini" {
					if r.URL.Query().Get("key") != "test-key" {
						http.Error(w, "missing key", http.StatusUnauthorized)
						return
					}
				} else if r.Header.Get("Authorization") != "Bearer test-key" {
					http.Error(w, "missing authorization", http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			var streamed strings.Builder
			got, usage, err := tc.new(srv.URL).Stream(t.Context(), ai.Request{Model: "model"}, func(delta string) { streamed.WriteString(delta) }, nil, nil)
			if err != nil || got.StopReason != ai.StopReasonLength || got.RawStopReason != tc.raw || len(got.ToolCalls) != 0 {
				t.Fatalf("message = %+v, usage = %+v, error = %v", got, usage, err)
			}
			canonical, err := FromAIMessageValidated(got)
			if err != nil {
				t.Fatal(err)
			}
			if canonical.StopReason != string(ai.StopReasonLength) || !strings.Contains(canonical.Content[0].Text, "response truncated") || streamed.String() != canonical.Content[0].Text {
				t.Fatalf("canonical terminal = %+v, streamed = %q", canonical, streamed.String())
			}
		})
	}
}

func assertCanonicalUsage(t *testing.T, message Message, want Usage) {
	t.Helper()
	if message.Usage == nil || *message.Usage != want {
		t.Fatalf("canonical usage = %+v, want %+v", message.Usage, want)
	}
}

func assertCanonicalToolHistory(t *testing.T, history []Message, id, name, args, output string) {
	t.Helper()
	var calls, results int
	for _, message := range history {
		if message.Role == RoleAssistant {
			for _, call := range message.ToolCalls {
				if call.ID == id && call.Name == name && string(call.Arguments) == args {
					calls++
				}
			}
		}
		if message.Role == RoleTool && message.ToolCallID == id && message.Name == name && len(message.Content) == 1 && message.Content[0].Text == output {
			results++
		}
	}
	if calls != 1 || results != 1 {
		t.Fatalf("canonical tool history calls=%d results=%d, history=%+v", calls, results, history)
	}
}

func checkChatHistory(body []byte) error {
	var request struct {
		Messages []struct {
			Role      string          `json:"role"`
			Content   json.RawMessage `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			ToolCallID string `json:"tool_call_id"`
			Name       string `json:"name"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return err
	}
	var callOK, resultOK bool
	for _, message := range request.Messages {
		for _, call := range message.ToolCalls {
			if call.ID == "call-history-1" && call.Function.Name == "read_file" && call.Function.Arguments == `{"path":"notes.txt"}` {
				callOK = true
			}
		}
		if message.Role == "tool" && message.ToolCallID == "call-history-1" && message.Name == "read_file" {
			var content string
			if err := json.Unmarshal(message.Content, &content); err == nil && content == "contents from tool" {
				resultOK = true
			}
		}
	}
	if !callOK || !resultOK {
		return fmt.Errorf("Chat history lost tool identity or result: %s", body)
	}
	return nil
}

func checkResponsesHistory(body []byte) error {
	var request struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return err
	}
	var callOK, resultOK bool
	for _, item := range request.Input {
		var typ string
		_ = json.Unmarshal(item["type"], &typ)
		switch typ {
		case "function_call":
			var id, name, args string
			_ = json.Unmarshal(item["call_id"], &id)
			_ = json.Unmarshal(item["name"], &name)
			_ = json.Unmarshal(item["arguments"], &args)
			callOK = id == "call-history-1" && name == "read_file" && args == `{"path":"notes.txt"}`
		case "function_call_output":
			var id, output string
			_ = json.Unmarshal(item["call_id"], &id)
			_ = json.Unmarshal(item["output"], &output)
			resultOK = id == "call-history-1" && output == "contents from tool"
		}
	}
	if !callOK || !resultOK {
		return fmt.Errorf("Responses history lost tool identity or result: %s", body)
	}
	return nil
}

func checkGeminiHistory(body []byte) error {
	var request struct {
		Contents []struct {
			Role  string `json:"role"`
			Parts []struct {
				FunctionCall *struct {
					ID               string         `json:"id"`
					Name             string         `json:"name"`
					Args             map[string]any `json:"args"`
					ThoughtSignature string         `json:"thoughtSignature"`
				} `json:"functionCall"`
				FunctionResponse *struct {
					ID       string         `json:"id"`
					Name     string         `json:"name"`
					Response map[string]any `json:"response"`
				} `json:"functionResponse"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return err
	}
	var callOK, resultOK bool
	for _, content := range request.Contents {
		for _, part := range content.Parts {
			if part.FunctionCall != nil {
				path, _ := part.FunctionCall.Args["path"].(string)
				callOK = content.Role == "model" && part.FunctionCall.Name == "read_file" && path == "notes.txt"
			}
			if part.FunctionResponse != nil {
				value, _ := part.FunctionResponse.Response["content"].(string)
				resultOK = content.Role == "user" && part.FunctionResponse.ID == "call-history-1" && part.FunctionResponse.Name == "read_file" && value == "contents from tool"
			}
		}
	}
	if !callOK || !resultOK {
		return fmt.Errorf("Gemini history lost tool name or result (Gemini has no wire tool ID): %s", body)
	}
	return nil
}
