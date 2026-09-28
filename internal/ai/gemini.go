package ai

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// Gemini implements Google's native generateContent API while exposing the
// same provider-neutral Client interface as the other adapters.
type Gemini struct {
	BaseURL    string
	APIKey     string
	HTTP       *http.Client
	MaxRetries int
	OnRetry    func(RetryEvent)

	mu         sync.Mutex
	usedIDs    map[string]bool
	signatures map[string]string
}

func NewGemini(baseURL, apiKey string) *Gemini {
	return &Gemini{BaseURL: strings.TrimRight(baseURL, "/"), APIKey: apiKey, HTTP: &http.Client{Timeout: 10 * time.Minute}, usedIDs: make(map[string]bool), signatures: make(map[string]string)}
}

func (c *Gemini) Clone() Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := Gemini{
		BaseURL:    c.BaseURL,
		APIKey:     c.APIKey,
		HTTP:       c.HTTP,
		MaxRetries: c.MaxRetries,
		OnRetry:    c.OnRetry,
	}
	cp.usedIDs = make(map[string]bool, len(c.usedIDs))
	for id, used := range c.usedIDs {
		cp.usedIDs[id] = used
	}
	cp.signatures = make(map[string]string, len(c.signatures))
	for id, signature := range c.signatures {
		cp.signatures[id] = signature
	}
	return &cp
}
func (c *Gemini) SetCacheKey(string)             {}
func (c *Gemini) Endpoint() string               { return c.BaseURL }
func (c *Gemini) SetMaxRetries(n int)            { c.MaxRetries = n }
func (c *Gemini) SetOnRetry(fn func(RetryEvent)) { c.OnRetry = fn }

func (c *Gemini) Models(ctx context.Context) ([]ModelInfo, error) {
	return nil, nil
}

func (c *Gemini) Complete(ctx context.Context, req Request) (string, Usage, error) {
	resp, err := c.do(ctx, req, false)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()
	var result geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", Usage{}, err
	}
	usage := result.usage()
	var msg Message
	msg.Role = "assistant"
	if _, err := result.emit(&msg, nil, nil); err != nil {
		return "", usage, err
	}
	return msg.Content, usage, nil
}

func (c *Gemini) Stream(ctx context.Context, req Request, onText, onThink func(string), onToolCall func(id, name, args string)) (Message, Usage, error) {
	resp, err := c.do(ctx, req, true)
	if err != nil {
		return Message{}, Usage{}, err
	}
	defer resp.Body.Close()
	sse := newSSEReader(resp.Body)
	var msg Message
	msg.Role = "assistant"
	var usage Usage
	var calls []ToolCall
	for {
		event, ok := sse.next()
		if !ok {
			return Message{}, usage, sse.endError("Gemini")
		}
		var result geminiResponse
		if err := decodeStreamEvent(event.Data, &result); err != nil {
			return Message{}, usage, err
		}
		if result.Error != nil {
			return Message{}, usage, providerStreamError(result.Error, "Gemini request failed")
		}
		usage = result.usage()
		partCalls, err := result.emit(&msg, onText, onThink)
		if err != nil {
			return Message{}, usage, err
		}
		for _, emitted := range partCalls {
			call := emitted.call
			call.ID, err = c.canonicalCallID(call.ID)
			if err != nil {
				return Message{}, usage, err
			}
			if err := c.rememberSignature(ctx, req.Model, call.ID, emitted.thoughtSignature); err != nil {
				return Message{}, usage, fmt.Errorf("persist Gemini tool signature: %w", err)
			}
			calls = append(calls, call)
			if onToolCall != nil {
				onToolCall(call.ID, call.Function.Name, call.Function.Arguments)
			}
		}
		if len(result.Candidates) > 0 && result.Candidates[0].FinishReason != "" {
			return finishMessage(msg, calls, normalizeGeminiFinishReason(result.Candidates[0].FinishReason), onText), usage, nil
		}
	}
}

type geminiResponse struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata geminiUsage       `json:"usageMetadata"`
	Error         json.RawMessage   `json:"error"`
}
type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
}
type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}
type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}
type geminiFunctionCall struct {
	ID               string         `json:"id,omitempty"`
	Name             string         `json:"name"`
	Args             map[string]any `json:"args"`
	ThoughtSignature string         `json:"thoughtSignature,omitempty"`
}
type geminiFunctionResponse struct {
	ID       string         `json:"id,omitempty"`
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}
type geminiUsage struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
}

func (u geminiUsage) normalized() Usage {
	return Usage{PromptTokens: u.PromptTokenCount, CompletionTokens: u.CandidatesTokenCount + u.ThoughtsTokenCount, PromptCacheHitTokens: u.CachedContentTokenCount}
}
func (r geminiResponse) usage() Usage { return r.UsageMetadata.normalized() }

type geminiEmittedCall struct {
	call             ToolCall
	thoughtSignature string
}

func (r geminiResponse) emit(msg *Message, onText, onThink func(string)) ([]geminiEmittedCall, error) {
	if len(r.Candidates) == 0 {
		return nil, nil
	}
	var calls []geminiEmittedCall
	for _, p := range r.Candidates[0].Content.Parts {
		if p.Text != "" {
			if p.Thought {
				if onThink != nil {
					onThink(p.Text)
				}
			} else {
				msg.Content += p.Text
				if onText != nil {
					onText(p.Text)
				}
			}
		}
		if p.FunctionCall != nil {
			args, err := json.Marshal(p.FunctionCall.Args)
			if err != nil {
				return nil, err
			}
			call := ToolCall{ID: p.FunctionCall.ID, Type: "function"}
			call.Function.Name, call.Function.Arguments = p.FunctionCall.Name, string(args)
			calls = append(calls, geminiEmittedCall{call: call, thoughtSignature: firstNonEmpty(p.FunctionCall.ThoughtSignature, p.ThoughtSignature)})
		}
	}
	return calls, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (c *Gemini) canonicalCallID(nativeID string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if nativeID != "" && !c.usedIDs[nativeID] {
		c.usedIDs[nativeID] = true
		return nativeID, nil
	}
	for {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", fmt.Errorf("generate Gemini tool call ID: %w", err)
		}
		id := "gemini-call-" + hex.EncodeToString(random[:])
		if !c.usedIDs[id] {
			c.usedIDs[id] = true
			return id, nil
		}
	}
}

// Signatures are provider-private: canonical transcripts contain only call IDs.
// This path mirrors config.Dir without introducing an ai/config import cycle.
func (c *Gemini) signaturePath(model string) (string, error) {
	dir := os.Getenv("K_BRAIN_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".k-brain")
	}
	key := sha256.Sum256([]byte(strings.TrimRight(c.BaseURL, "/") + "\x00" + model))
	return filepath.Join(dir, "cache", "gemini-signatures", hex.EncodeToString(key[:])+".json"), nil
}

func readGeminiSignatures(path string) (map[string]string, error) {
	records := make(map[string]string)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return records, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	if records == nil {
		return nil, fmt.Errorf("invalid Gemini signature cache")
	}
	return records, nil
}

func (c *Gemini) rememberSignature(ctx context.Context, model, id, signature string) error {
	if id == "" || signature == "" {
		return nil
	}
	path, err := c.signaturePath(model)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock := flock.New(path+".lock", flock.SetPermissions(0o600))
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil {
		return err
	}
	if !locked {
		return ctx.Err()
	}
	defer lock.Unlock()
	records, err := readGeminiSignatures(path)
	if err != nil {
		return err
	}
	if old := records[id]; old != "" && old != signature {
		return fmt.Errorf("conflicting Gemini signature for call %q", id)
	}
	records[id] = signature
	data, err := json.Marshal(records)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".signature-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	c.mu.Lock()
	c.signatures[model+"\x00"+id] = signature
	c.mu.Unlock()
	return nil
}

func (c *Gemini) loadSignatures(model string) error {
	path, err := c.signaturePath(model)
	if err != nil {
		return err
	}
	records, err := readGeminiSignatures(path)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, signature := range records {
		c.signatures[model+"\x00"+id] = signature
	}
	return nil
}

func (c *Gemini) signature(model, id string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.signatures[model+"\x00"+id]
}

func (c *Gemini) do(ctx context.Context, req Request, stream bool) (*http.Response, error) {
	c.reserveHistoryIDs(req.Messages)
	if err := c.loadSignatures(req.Model); err != nil {
		return nil, fmt.Errorf("load Gemini tool signatures: %w", err)
	}
	body, err := json.Marshal(c.geminiPayloadWithState(req))
	if err != nil {
		return nil, err
	}
	path := geminiPath(c.BaseURL, req.Model, stream)
	var response *http.Response
	err = newRetryPolicy(c.MaxRetries, c.OnRetry).run(ctx, func() error {
		u := strings.TrimRight(c.BaseURL, "/") + path
		parsed, parseErr := url.Parse(u)
		if parseErr != nil {
			return nonRetryable{parseErr}
		}
		q := parsed.Query()
		q.Set("key", c.APIKey)
		parsed.RawQuery = q.Encode()
		reqHTTP, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(body))
		if reqErr != nil {
			return nonRetryable{reqErr}
		}
		reqHTTP.Header.Set("Content-Type", "application/json")
		if stream {
			reqHTTP.Header.Set("Accept", "text/event-stream")
		}
		response, reqErr = c.HTTP.Do(reqHTTP)
		if reqErr != nil {
			return reqErr
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			defer response.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
			return newHTTPError(response, string(b))
		}
		return nil
	}, nil)
	return response, err
}

func geminiPath(baseURL, model string, stream bool) string {
	prefix := "/v1beta"
	if strings.HasSuffix(strings.TrimRight(baseURL, "/"), "/v1beta") {
		prefix = ""
	}
	path := prefix + "/models/" + url.PathEscape(model) + ":generateContent"
	if stream {
		path = prefix + "/models/" + url.PathEscape(model) + ":streamGenerateContent?alt=sse"
	}
	return path
}

func normalizeGeminiFinishReason(reason string) string {
	switch strings.ToUpper(reason) {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII":
		return "other"
	case "MALFORMED_FUNCTION_CALL":
		return "tool_calls"
	default:
		return strings.ToLower(reason)
	}
}

func (c *Gemini) reserveHistoryIDs(messages []Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			if call.ID != "" {
				c.usedIDs[call.ID] = true
			}
		}
	}
}

func geminiPayload(req Request) map[string]any {
	return NewGemini("", "").geminiPayloadWithState(req)
}

func (c *Gemini) geminiPayloadWithState(req Request) map[string]any {
	contents := make([]map[string]any, 0, len(req.Messages))
	var system []any
	for _, m := range req.Messages {
		parts := make([]any, 0)
		if m.Content != "" {
			parts = append(parts, map[string]any{"text": m.Content})
		}
		for _, p := range m.Parts {
			if p.Type == "text" {
				parts = append(parts, map[string]any{"text": p.Text})
			} else if p.ImageURL != nil {
				parts = append(parts, map[string]any{"text": p.ImageURL.URL})
			}
		}
		for _, tc := range m.ToolCalls {
			var args any
			_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
			functionCall := map[string]any{"name": tc.Function.Name, "args": args}
			if tc.ID != "" {
				functionCall["id"] = tc.ID
			}
			part := map[string]any{"functionCall": functionCall}
			if signature := c.signature(req.Model, tc.ID); signature != "" {
				part["thoughtSignature"] = signature
			}
			parts = append(parts, part)
		}
		if m.Role == "system" || m.Role == "developer" {
			system = append(system, parts...)
			continue
		}
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		if m.Role == "tool" {
			functionResponse := map[string]any{"name": m.Name, "response": map[string]any{"content": m.Content}}
			if m.ToolCallID != "" {
				functionResponse["id"] = m.ToolCallID
			}
			parts = []any{map[string]any{"functionResponse": functionResponse}}
			role = "user"
		}
		contents = append(contents, map[string]any{"role": role, "parts": parts})
	}
	payload := map[string]any{"contents": contents}
	if len(system) > 0 {
		payload["systemInstruction"] = map[string]any{"parts": system}
	}
	if req.MaxTokens > 0 || req.Temperature != nil || req.TopP != nil {
		cfg := map[string]any{}
		if req.MaxTokens > 0 {
			cfg["maxOutputTokens"] = req.MaxTokens
		}
		if req.Temperature != nil {
			cfg["temperature"] = *req.Temperature
		}
		if req.TopP != nil {
			cfg["topP"] = *req.TopP
		}
		payload["generationConfig"] = cfg
	}
	if len(req.Tools) > 0 {
		decls := make([]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			var params any
			_ = json.Unmarshal(t.Function.Parameters, &params)
			decls = append(decls, map[string]any{"name": t.Function.Name, "description": t.Function.Description, "parameters": params})
		}
		payload["tools"] = []any{map[string]any{"functionDeclarations": decls}}
	}
	return payload
}
