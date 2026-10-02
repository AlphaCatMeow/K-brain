package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/process"
)

const (
	maxBackendHooks      = 128
	maxHooksPerEvent     = 32
	minHookTimeout       = time.Second
	maxHookTimeout       = 10 * time.Minute
	maxHookBodyBytes     = 64 << 10
	maxHookResponseBytes = 4 << 10
	maxHookHeadersBytes  = 32 << 10
)

var backendHookEvents = map[string]bool{
	"agent_start": true, "turn_start": true, "message_start": true, "message_end": true,
	"tool_execution_start": true, "tool_execution_end": true, "turn_end": true, "agent_end": true,
}

type HookType string

const (
	HookCommand HookType = "command"
	HookHTTP    HookType = "http"
)

type HookRequest struct {
	ID      string            `json:"id"`
	URL     string            `json:"url"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    any               `json:"body,omitempty"`
}

type BackendHook struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Event       string        `json:"event"`
	Enabled     bool          `json:"enabled"`
	Type        HookType      `json:"type"`
	Script      string        `json:"script,omitempty"`
	Requests    []HookRequest `json:"requests,omitempty"`
	TimeoutMS   int           `json:"timeoutMs,omitempty"`
}

type HooksSnapshot struct {
	Revision uint64        `json:"revision"`
	Hooks    []BackendHook `json:"hooks"`
}

type HookOperation struct {
	Op    string         `json:"op"`
	ID    string         `json:"id,omitempty"`
	Item  map[string]any `json:"item,omitempty"`
	Patch map[string]any `json:"patch,omitempty"`
	IDs   []string       `json:"ids,omitempty"`
}

type HooksApplyInput struct {
	BaseRevision uint64          `json:"baseRevision"`
	Ops          []HookOperation `json:"ops"`
}

type HooksApplyResponse struct {
	Status string        `json:"status"`
	Hooks  HooksSnapshot `json:"hooks"`
}

type HookStore struct {
	mu       sync.RWMutex
	path     string
	revision uint64
	hooks    []BackendHook
}

func NewHookStore(path string) (*HookStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("hook store path is required")
	}
	store := &HookStore{path: path, revision: 1, hooks: []BackendHook{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read backend hooks: %w", err)
	}
	var snapshot HooksSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, fmt.Errorf("decode backend hooks: %w", err)
	}
	if err := validateHookList(snapshot.Hooks); err != nil {
		return nil, err
	}
	if snapshot.Revision > 0 {
		store.revision = snapshot.Revision
	}
	store.hooks = cloneBackendHooks(snapshot.Hooks)
	return store, nil
}

func (s *HookStore) Snapshot() HooksSnapshot {
	if s == nil {
		return HooksSnapshot{Revision: 1, Hooks: []BackendHook{}}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return HooksSnapshot{Revision: s.revision, Hooks: cloneBackendHooks(s.hooks)}
}

func (s *HookStore) Apply(in HooksApplyInput) (HooksApplyResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := HooksSnapshot{Revision: s.revision, Hooks: cloneBackendHooks(s.hooks)}
	if in.BaseRevision != s.revision {
		return HooksApplyResponse{Status: "conflict", Hooks: current}, nil
	}
	next := cloneBackendHooks(s.hooks)
	for _, op := range in.Ops {
		var err error
		next, err = applyHookOperation(next, op)
		if err != nil {
			return HooksApplyResponse{}, err
		}
	}
	if err := validateHookList(next); err != nil {
		return HooksApplyResponse{}, err
	}
	s.revision++
	s.hooks = next
	if err := s.persistLocked(); err != nil {
		s.revision--
		s.hooks = current.Hooks
		return HooksApplyResponse{}, err
	}
	return HooksApplyResponse{Status: "ok", Hooks: s.snapshotLocked()}, nil
}

func (s *HookStore) snapshotLocked() HooksSnapshot {
	return HooksSnapshot{Revision: s.revision, Hooks: cloneBackendHooks(s.hooks)}
}

func (s *HookStore) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.snapshotLocked(), "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func applyHookOperation(items []BackendHook, op HookOperation) ([]BackendHook, error) {
	switch op.Op {
	case "create":
		var item BackendHook
		data, err := json.Marshal(op.Item)
		if err != nil || json.Unmarshal(data, &item) != nil {
			return nil, errors.New("invalid hook")
		}
		if item.ID == "" {
			item.ID = newRunID()
		}
		for _, existing := range items {
			if existing.ID == item.ID {
				return nil, errors.New("duplicate hook id")
			}
		}
		return append(items, item), nil
	case "update":
		for i := range items {
			if items[i].ID != op.ID {
				continue
			}
			var merged map[string]any
			base, _ := json.Marshal(items[i])
			_ = json.Unmarshal(base, &merged)
			for key, value := range op.Patch {
				if key != "id" {
					merged[key] = value
				}
			}
			mergedData, _ := json.Marshal(merged)
			var updated BackendHook
			if err := json.Unmarshal(mergedData, &updated); err != nil {
				return nil, err
			}
			updated.ID = items[i].ID
			items[i] = updated
			return items, nil
		}
		return nil, errors.New("hook not found")
	case "delete":
		for i := range items {
			if items[i].ID == op.ID {
				return append(items[:i], items[i+1:]...), nil
			}
		}
		return nil, errors.New("hook not found")
	case "reorder":
		if len(op.IDs) != len(items) {
			return nil, errors.New("reorder must include every hook")
		}
		byID := make(map[string]BackendHook, len(items))
		for _, item := range items {
			byID[item.ID] = item
		}
		out := make([]BackendHook, 0, len(items))
		for _, id := range op.IDs {
			item, ok := byID[id]
			if !ok {
				return nil, errors.New("reorder contains unknown hook")
			}
			out = append(out, item)
			delete(byID, id)
		}
		if len(byID) != 0 {
			return nil, errors.New("reorder contains duplicate hook")
		}
		return out, nil
	default:
		return nil, errors.New("unsupported hook operation")
	}
}

func validateHookList(items []BackendHook) error {
	if len(items) > maxBackendHooks {
		return fmt.Errorf("at most %d hooks are allowed", maxBackendHooks)
	}
	seen := map[string]bool{}
	counts := map[string]int{}
	for i := range items {
		item := &items[i]
		if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Name) == "" {
			return errors.New("hook id and name are required")
		}
		if seen[item.ID] {
			return fmt.Errorf("duplicate hook id %q", item.ID)
		}
		seen[item.ID] = true
		if !backendHookEvents[item.Event] {
			return fmt.Errorf("unsupported hook event %q", item.Event)
		}
		counts[item.Event]++
		if counts[item.Event] > maxHooksPerEvent {
			return fmt.Errorf("at most %d hooks are allowed per event", maxHooksPerEvent)
		}
		if item.Type != HookCommand && item.Type != HookHTTP {
			return fmt.Errorf("unsupported hook type %q", item.Type)
		}
		if item.TimeoutMS == 0 {
			item.TimeoutMS = 60_000
		}
		if time.Duration(item.TimeoutMS)*time.Millisecond < minHookTimeout || time.Duration(item.TimeoutMS)*time.Millisecond > maxHookTimeout {
			return fmt.Errorf("hook timeout must be between %d and %d ms", minHookTimeout.Milliseconds(), maxHookTimeout.Milliseconds())
		}
		if item.Type == HookCommand {
			if strings.TrimSpace(item.Script) == "" {
				return errors.New("command hook script is required")
			}
			if len(item.Script) > maxHookBodyBytes {
				return errors.New("hook script is too large")
			}
		} else {
			if len(item.Requests) == 0 || len(item.Requests) > 32 {
				return errors.New("http hook requires a request")
			}
			for j := range item.Requests {
				if err := validateHookRequest(&item.Requests[j]); err != nil {
					return fmt.Errorf("hook %s request %d: %w", item.ID, j, err)
				}
			}
		}
	}
	return nil
}

func validateHookRequest(req *HookRequest) error {
	if strings.TrimSpace(req.ID) == "" {
		return errors.New("request id is required")
	}
	u, err := url.Parse(strings.TrimSpace(req.URL))
	if err != nil || u.User != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("url must be an absolute HTTP(S) URL without credentials")
	}
	req.URL = u.String()
	req.Method = strings.ToUpper(strings.TrimSpace(req.Method))
	if req.Method == "" {
		req.Method = http.MethodPost
	}
	switch req.Method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
	default:
		return fmt.Errorf("unsupported method %q", req.Method)
	}
	data, _ := json.Marshal(req.Body)
	if len(data) > maxHookBodyBytes {
		return errors.New("request body is too large")
	}
	headerBytes := 0
	for key, value := range req.Headers {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key, "\r\n") || strings.ContainsAny(value, "\r\n") {
			return errors.New("invalid request header")
		}
		headerBytes += len(key) + len(value)
	}
	if headerBytes > maxHookHeadersBytes {
		return errors.New("request headers are too large")
	}
	return nil
}

func cloneBackendHooks(items []BackendHook) []BackendHook {
	out := make([]BackendHook, len(items))
	for i, item := range items {
		out[i] = item
		out[i].Requests = append([]HookRequest(nil), item.Requests...)
		for j := range out[i].Requests {
			out[i].Requests[j].Headers = map[string]string{}
			for key, value := range item.Requests[j].Headers {
				out[i].Requests[j].Headers[key] = value
			}
		}
	}
	return out
}

type hookEnvelope struct {
	Event          string `json:"event"`
	ConversationID string `json:"conversationId"`
	RunID          string `json:"runId"`
	SessionID      string `json:"sessionId"`
	CWD            string `json:"cwd,omitempty"`
	Payload        any    `json:"payload,omitempty"`
}

type BackendHookRunner struct {
	store *HookStore
	slot  chan struct{}
}

func NewBackendHookRunner(store *HookStore) *BackendHookRunner {
	return &BackendHookRunner{store: store, slot: make(chan struct{}, 1)}
}

// Scope snapshots configuration once; event replay never invokes this callback.
func (r *BackendHookRunner) Scope(ctx context.Context, conversationID, runID, cwd string, warn func(BackendHook, string, error)) func(string) {
	items := r.store.Snapshot().Hooks
	return func(event string) {
		if ctx.Err() != nil {
			return
		}
		select {
		case r.slot <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-r.slot }()
		for _, hook := range items {
			if ctx.Err() != nil {
				return
			}
			if !hook.Enabled || hook.Event != event {
				continue
			}
			envelope := hookEnvelope{Event: event, ConversationID: conversationID, RunID: runID, SessionID: conversationID, CWD: cwd}
			if err := runBackendHook(ctx, hook, envelope); err != nil && ctx.Err() == nil && warn != nil {
				warn(hook, event, err)
			}
		}
	}
}

type hookOutput struct {
	mu   sync.Mutex
	data []byte
}

func (b *hookOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := min(len(p), maxHookBodyBytes-len(b.data))
	b.data = append(b.data, p[:n]...)
	return len(p), nil
}

func runBackendHook(ctx context.Context, hook BackendHook, envelope hookEnvelope) error {
	timeout := time.Duration(hook.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Minute
	}
	if hook.Type == HookCommand {
		hctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		shell, args := "sh", []string{"-c", hook.Script}
		if runtime.GOOS == "windows" {
			shell, args = "powershell.exe", []string{"-NoProfile", "-NonInteractive", "-Command", hook.Script}
		}
		cmd := exec.CommandContext(hctx, shell, args...)
		cmd.Dir = envelope.CWD
		data, _ := json.Marshal(envelope)
		cmd.Env = append(os.Environ(), "LIVEAGENT_HOOK_EVENT="+envelope.Event, "LIVEAGENT_HOOK_NAME="+hook.Name,
			"LIVEAGENT_CONVERSATION_ID="+envelope.ConversationID, "LIVEAGENT_RUN_ID="+envelope.RunID,
			"LIVEAGENT_WORKDIR="+envelope.CWD, "K_BRAIN_HOOK_EVENT="+string(data))
		output := &hookOutput{}
		cmd.Stdout, cmd.Stderr = output, output
		cmd.WaitDelay = time.Second
		process.Configure(cmd, true)
		err := cmd.Run()
		if hctx.Err() != nil {
			return hctx.Err()
		}
		if err != nil {
			return fmt.Errorf("command failed: %w", err)
		}
		return nil
	}
	var failures []error
	for _, request := range hook.Requests {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := runHookRequest(ctx, timeout, request, envelope); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func runHookRequest(ctx context.Context, timeout time.Duration, request HookRequest, envelope hookEnvelope) error {
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if request.Method == "POST" || request.Method == "PUT" || request.Method == "PATCH" || request.Method == "DELETE" {
		if request.Body != nil {
			data, err := json.Marshal(request.Body)
			if err != nil {
				return err
			}
			if len(data) > maxHookBodyBytes {
				return errors.New("hook request body is too large")
			}
			body = bytes.NewReader(data)
		}
	}
	req, err := http.NewRequestWithContext(hctx, request.Method, request.URL, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-LiveAgent-Hook-Event", envelope.Event)
	req.Header.Set("X-LiveAgent-Conversation-Id", envelope.ConversationID)
	req.Header.Set("X-LiveAgent-Run-Id", envelope.RunID)
	for key, value := range request.Headers {
		req.Header.Set(key, value)
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request %s failed", request.ID)
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, maxHookResponseBytes))
	if err != nil {
		return fmt.Errorf("request %s response failed", request.ID)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("request %s returned HTTP %d", request.ID, resp.StatusCode)
	}
	return nil
}
