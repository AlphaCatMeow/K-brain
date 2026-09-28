// Package backend exposes K-brain's canonical session and Agent runtime over HTTP.
package backend

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/session/recording"
	"github.com/Stack-Cairn/K-brain/internal/tools"
)

const maxBodyBytes = 4 << 20

type Factory func(context.Context, string, protocol.ModelRef) (*agent.Agent, error)

type Options struct {
	Store      *session.Store
	Factory    Factory
	EventDir   string
	Token      string
	Models     []protocol.ModelRef
	DefaultCWD string
	Settings   *SettingsStore
}

type Server struct {
	store      *session.Store
	factory    Factory
	token      string
	models     []protocol.ModelRef
	settings   *SettingsStore
	eventDir   string
	defaultCWD string

	mu       sync.Mutex
	sessions map[string]*runtimeSession
	closed   bool
}

type runRecord struct {
	ClientRequestID string `json:"client_request_id"`
	PromptHash      string `json:"prompt_hash"`
	RunID           string `json:"run_id"`
	AcceptedSeq     int64  `json:"accepted_seq"`
	Terminal        bool   `json:"terminal"`
}

type permissionWaiter struct {
	request protocol.PermissionRequest
	result  chan tools.GateDecision
	reason  chan string
}

type runtimeSession struct {
	settingsRevision uint64
	id               string
	deleted          bool
	mu               sync.Mutex
	agent            *agent.Agent
	recorder         *recording.Recorder
	cancel           context.CancelFunc
	runID            string
	runDone          bool
	nextSeq          int64
	events           []protocol.Event
	changed          chan struct{}
	runs             map[string]runRecord
	permissions      map[string]*permissionWaiter
	eventDir         string
}

func New(opts Options) (*Server, error) {
	if opts.Store == nil {
		return nil, errors.New("backend store is required")
	}
	if opts.Factory == nil {
		return nil, errors.New("backend agent factory is required")
	}
	if opts.EventDir == "" {
		opts.EventDir = filepath.Join(opts.Store.SessionsDir(), "backend-events")
	}
	if err := os.MkdirAll(opts.EventDir, 0o700); err != nil {
		return nil, fmt.Errorf("create backend event directory: %w", err)
	}
	return &Server{
		store: opts.Store, factory: opts.Factory, token: opts.Token, models: append([]protocol.ModelRef(nil), opts.Models...),
		settings: opts.Settings,
		eventDir: opts.EventDir, defaultCWD: opts.DefaultCWD, sessions: make(map[string]*runtimeSession),
	}, nil
}

func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	runtimes := make([]*runtimeSession, 0, len(s.sessions))
	for _, rt := range s.sessions {
		runtimes = append(runtimes, rt)
	}
	s.mu.Unlock()
	for _, rt := range runtimes {
		rt.mu.Lock()
		if rt.cancel != nil {
			rt.cancel()
		}

		rt.mu.Unlock()
	}
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.cors(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method == http.MethodGet && (strings.HasPrefix(r.URL.Path, "/v1/shares/") || strings.HasPrefix(r.URL.Path, "/share/")) {
		s.publicShare(w, r)
		return
	}
	if !s.authorized(r) {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/health" {
		writeJSON(w, http.StatusOK, map[string]any{"version": protocol.Version, "status": "ok"})
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
		s.handleModels(w)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path == "/v1/text/generate" {
		s.generateText(w, r)
		return
	}
	if r.URL.Path == "/v1/settings" {
		s.handleSettings(w, r)
		return
	}
	if r.URL.Path == "/v1/sessions" {
		if r.Method == http.MethodGet {
			s.listSessions(w, r)
			return
		}
		if r.Method == http.MethodPost {
			s.createSession(w, r)
			return
		}
	}
	const prefix = "/v1/sessions/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		writeJSONError(w, http.StatusNotFound, "route not found")
		return
	}
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSONError(w, http.StatusNotFound, "session not found")
		return
	}
	id, err := urlPathID(parts[0])
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(parts) == 1 && r.Method == http.MethodDelete {
		s.deleteSession(w, r, id)
		return
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "history":
			if r.Method == http.MethodGet {
				s.history(w, r, id)
				return
			}
		case "branch", "edit":
			if r.Method == http.MethodPost {
				s.mutateHistory(w, r, id, parts[1] == "edit")
				return
			}
		case "share":
			if r.Method == http.MethodGet || r.Method == http.MethodPost {
				s.share(w, r, id)
				return
			}
		}
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		s.getSession(w, r, id)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodPatch {
		s.updateSession(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodGet {
		s.events(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "runs" && r.Method == http.MethodPost {
		s.startRun(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "close" && r.Method == http.MethodPost {
		s.closeSession(w, r, id)
		return
	}
	if len(parts) == 4 && parts[1] == "runs" && parts[2] != "" && parts[3] == "cancel" && r.Method == http.MethodPost {
		s.cancelRun(w, r, id, parts[2])
		return
	}
	if len(parts) == 3 && parts[1] == "permissions" && parts[2] != "" && r.Method == http.MethodPost {
		s.permission(w, r, id, parts[2])
		return
	}
	writeJSONError(w, http.StatusNotFound, "route not found")
}

func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" {
		return true
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(got) != len(s.token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

func (s *Server) cors(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
}

func (s *Server) createSession(w http.ResponseWriter, r *http.Request) {
	var in protocol.CreateSessionRequest
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	cwd := strings.TrimSpace(in.CWD)
	if cwd == "" {
		cwd = s.defaultCWD
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	if strings.TrimSpace(in.Model.Model) == "" {
		writeJSONError(w, http.StatusBadRequest, "model.model is required")
		return
	}
	id, err := s.store.Create(cwd, in.Model.Model, in.Model.Provider)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if len(in.Messages) > 0 {
		aiMessages := make([]ai.Message, 0, len(in.Messages))
		for _, message := range in.Messages {
			converted, convertErr := message.ToAIMessage()
			if convertErr != nil {
				_ = s.store.Delete(id)
				writeJSONError(w, http.StatusBadRequest, "invalid history message: "+convertErr.Error())
				return
			}
			aiMessages = append(aiMessages, converted)
		}
		if err := s.store.Save(id, 0, aiMessages, in.Model.Model, in.Model.Provider); err != nil {
			_ = s.store.Delete(id)
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if in.Title != "" {
		if err := s.store.SetTitle(id, in.Title); err != nil {
			_ = s.store.Delete(id)
			writeJSONError(w, 500, err.Error())
			return
		}
	}
	rt, err := s.loadRuntime(id, in.Model, cwd)
	if err != nil {
		_ = s.store.Delete(id)
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, s.sessionView(rt))
}

func (s *Server) getSession(w http.ResponseWriter, r *http.Request, id string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.sessionView(rt))
}

func (s *Server) sessionView(rt *runtimeSession) protocol.Session {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	out, _ := s.sessionViewLocked(rt)
	return out
}

func (s *Server) sessionViewLocked(rt *runtimeSession) (protocol.Session, error) {
	if rt.deleted {
		return protocol.Session{}, session.ErrNotFound
	}
	snap, err := s.store.HistorySnapshot(rt.id)
	if err != nil {
		return protocol.Session{}, err
	}
	out := protocol.Session{SessionSummary: summary(snap.Meta, len(snap.Messages)), LastSeq: rt.nextSeq, Revision: snap.Revision, Messages: []protocol.Message{}}
	out.CreatedAt = snap.CreatedAt
	for _, msg := range snap.Messages {
		out.Messages = append(out.Messages, protocol.FromAIMessage(msg))
	}
	seen := make(map[string]bool)
	if rt.agent != nil {
		for _, task := range rt.agent.Tasks().List() {
			out.Tasks = append(out.Tasks, subagentView(task))
			seen[task.ID] = true
		}
	}
	storedTasks, err := s.store.LoadTasks(rt.id)
	if err != nil {
		return protocol.Session{}, err
	}
	for _, task := range storedTasks {
		if !seen[task.ID] {
			out.Tasks = append(out.Tasks, storedSubagentView(task))
		}
	}
	return out, nil
}

func summary(meta session.Meta, count int) protocol.SessionSummary {
	return protocol.SessionSummary{ID: meta.ID, Title: meta.Title, CWD: meta.CWD, Model: protocol.ModelRef{Provider: meta.Provider, Model: meta.Model}, CreatedAt: meta.CreatedAt, UpdatedAt: meta.UpdatedAt, MessageCount: count, Pinned: meta.Pinned, Archived: meta.Archived, Shared: meta.Shared}
}

func (s *Server) loadRuntimeByID(id string) (*runtimeSession, error) {
	if !validID(id) {
		return nil, session.ErrNotFound
	}
	s.mu.Lock()
	if rt := s.sessions[id]; rt != nil {
		s.mu.Unlock()
		rt.mu.Lock()
		deleted := rt.deleted
		rt.mu.Unlock()
		if deleted {
			return nil, session.ErrNotFound
		}
		return rt, nil
	}
	s.mu.Unlock()
	meta, _, err := s.store.Load(id)
	if err != nil {
		return nil, err
	}
	return s.loadRuntime(id, protocol.ModelRef{Provider: meta.Provider, Model: meta.Model}, meta.CWD)
}

func (s *Server) switchModelLocked(rt *runtimeSession, selected protocol.ModelRef) error {
	if strings.TrimSpace(selected.Model) == "" {
		return errors.New("model.model is required")
	}
	if rt.agent != nil && rt.agent.ModelName == selected.Model && rt.agent.Provider == selected.Provider {
		return nil
	}
	cwd := ""
	if rt.agent != nil {
		cwd = rt.agent.WorkingDir
	}
	if cwd == "" {
		if meta, _, err := s.store.Load(rt.id); err == nil {
			cwd = meta.CWD
		}
	}
	ag, err := s.factory(context.Background(), cwd, selected)
	if err != nil {
		return err
	}
	ag.WorkingDir = cwd
	ag.ModelName, ag.Provider = selected.Model, selected.Provider
	ag.SetSessionID(rt.id)
	rec, err := recording.Open(s.store, rt.id, ag)
	if err != nil {
		return err
	}
	if err := s.store.SetModel(rt.id, selected.Model, selected.Provider); err != nil {
		return err
	}
	s.wireTasks(rt, ag)
	rt.agent = ag
	rt.settingsRevision = 0
	rt.recorder = rec
	return nil
}

func (s *Server) loadRuntime(id string, model protocol.ModelRef, cwd string) (*runtimeSession, error) {
	s.mu.Lock()
	if rt := s.sessions[id]; rt != nil {
		s.mu.Unlock()
		rt.mu.Lock()
		deleted := rt.deleted
		rt.mu.Unlock()
		if deleted {
			return nil, session.ErrNotFound
		}
		return rt, nil
	}
	s.mu.Unlock()
	ag, err := s.factory(context.Background(), cwd, model)
	if err != nil {
		return nil, err
	}
	ag.WorkingDir = cwd
	ag.ModelName, ag.Provider = model.Model, model.Provider
	ag.SetSessionID(id)
	rec, err := recording.Open(s.store, id, ag)
	if err != nil {
		return nil, err
	}
	rt := &runtimeSession{id: id, agent: ag, recorder: rec, changed: make(chan struct{}), runs: map[string]runRecord{}, permissions: map[string]*permissionWaiter{}, eventDir: s.eventDir}
	if err := rt.loadJournal(s.eventDir); err != nil {
		return nil, err
	}
	s.wireTasks(rt, ag)
	s.mu.Lock()
	if existing := s.sessions[id]; existing != nil {
		s.mu.Unlock()
		existing.mu.Lock()
		deleted := existing.deleted
		existing.mu.Unlock()
		if deleted {
			return nil, session.ErrNotFound
		}
		return existing, nil
	}
	s.sessions[id] = rt
	s.mu.Unlock()
	return rt, nil
}

func (s *Server) wireTasks(rt *runtimeSession, ag *agent.Agent) {
	ag.Tasks().SetSessionID(rt.id)
	ag.Tasks().OnRecord = func(id string, task *agent.BackgroundTask) {
		model, provider, _ := strings.Cut(task.SubModel, " @ ")
		stored := session.Task{Model: model, Provider: provider, ID: task.ID, Description: task.Description, Prompt: task.Prompt, Status: string(task.Status), Report: task.Report, StartedAt: task.StartedAt, EndedAt: task.EndedAt}
		if err := s.store.SaveTask(id, stored); err != nil {
			_, _ = rt.publish(protocol.EventToolStatus, protocol.ToolStatus{Tool: "subagent", Status: "error", Message: "save task: " + err.Error()}, "")
			return
		}
		if task.Status != agent.TaskRunning && task.SubMessages != nil {
			model, provider, _ := strings.Cut(task.SubModel, " @ ")
			if _, err := s.store.SaveSubagentTranscript(id, task.ID, task.SubMessages, model, provider); err != nil {
				_, _ = rt.publish(protocol.EventToolStatus, protocol.ToolStatus{Tool: "subagent", Status: "error", Message: "save task transcript: " + err.Error()}, "")
				return
			}
		}
		rt.taskEvent(task)
	}
}

func (rt *runtimeSession) taskEvent(t *agent.BackgroundTask) {
	view := subagentView(*t)
	typ := protocol.EventSubagentUpdate
	switch t.Status {
	case agent.TaskRunning:
		typ = protocol.EventSubagentStarted
	case agent.TaskDone:
		typ = protocol.EventSubagentCompleted
	case agent.TaskError, agent.TaskCancelled:
		typ = protocol.EventSubagentFailed
	}
	_, _ = rt.publish(typ, protocol.SubagentEvent{Subagent: view}, "")
}

func subagentView(t agent.BackgroundTask) protocol.Subagent {
	view := storedSubagentView(session.Task{ID: t.ID, Description: t.Description, Status: string(t.Status), Report: t.Report, StartedAt: t.StartedAt, EndedAt: t.EndedAt})
	model, provider, _ := strings.Cut(t.SubModel, " @ ")
	view.Model = protocol.ModelRef{Provider: provider, Model: model}
	return view
}
func storedSubagentView(t session.Task) protocol.Subagent {
	view := protocol.Subagent{ID: t.ID, Description: t.Description, Status: t.Status, Report: t.Report, Model: protocol.ModelRef{Model: t.Model, Provider: t.Provider}, StartedAt: ptrTime(t.StartedAt), EndedAt: ptrTime(t.EndedAt)}
	if t.Status == string(agent.TaskError) {
		view.Error = t.Report
	}
	return view
}
func ptrTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	x := t
	return &x
}

func (rt *runtimeSession) loadJournal(dir string) error {
	path := filepath.Join(dir, rt.id+".jsonl")
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return rt.loadRuns(dir)
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxBodyBytes)
	for sc.Scan() {
		var e protocol.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return err
		}
		if err := e.Validate(); err != nil {
			return err
		}
		rt.events = append(rt.events, e)
		if e.Seq > rt.nextSeq {
			rt.nextSeq = e.Seq
		}
		rt.runID = e.RunID
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return rt.loadRuns(dir)
}
func (rt *runtimeSession) loadRuns(dir string) error {
	b, err := os.ReadFile(filepath.Join(dir, rt.id+".runs.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, &rt.runs)
}
func (rt *runtimeSession) persistEvent(dir string, e protocol.Event) error {
	f, err := os.OpenFile(filepath.Join(dir, rt.id+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, _ := json.Marshal(e)
	_, err = f.Write(append(b, '\n'))
	return err
}
func (rt *runtimeSession) persistRuns(dir string) error {
	b, err := json.MarshalIndent(rt.runs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, rt.id+".runs.json"), b, 0o600)
}

func (rt *runtimeSession) publish(typ string, payload any, parent string) (protocol.Event, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.deleted {
		return protocol.Event{}, session.ErrNotFound
	}
	rt.nextSeq++
	e, err := protocol.NewEvent(rt.nextSeq, rt.id, rt.runID, typ, payload)
	if err != nil {
		return protocol.Event{}, err
	}
	e.ParentRunID = parent
	if err = rt.persistEvent(rt.eventDir, e); err != nil {
		return protocol.Event{}, err
	}
	rt.events = append(rt.events, e)
	old := rt.changed
	rt.changed = make(chan struct{})
	close(old)
	return e, nil
}

// publishWithDir is the durable form used by handlers; publish is kept small for task callbacks.
func (s *Server) publish(rt *runtimeSession, typ string, payload any, parent string) (protocol.Event, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.runID == "" {
		rt.runID = "recovery"
	}
	if rt.deleted {
		return protocol.Event{}, session.ErrNotFound
	}
	rt.nextSeq++
	e, err := protocol.NewEvent(rt.nextSeq, rt.id, rt.runID, typ, payload)
	if err != nil {
		return protocol.Event{}, err
	}
	e.ParentRunID = parent
	if err = rt.persistEvent(s.eventDir, e); err != nil {
		return protocol.Event{}, err
	}
	rt.events = append(rt.events, e)
	old := rt.changed
	rt.changed = make(chan struct{})
	close(old)
	return e, nil
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request, id string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	var in protocol.PromptRequest
	if err = decodeJSON(w, r, &in); err != nil {
		return
	}
	if strings.TrimSpace(in.ClientRequestID) == "" {
		writeJSONError(w, http.StatusBadRequest, "client_request_id is required")
		return
	}
	if strings.TrimSpace(in.Prompt) == "" && len(in.Content) == 0 {
		writeJSONError(w, http.StatusBadRequest, "prompt or content is required")
		return
	}
	userMessage := protocol.Message{Role: protocol.RoleUser}
	if in.Prompt != "" {
		userMessage.Content = append(userMessage.Content, protocol.ContentBlock{Type: protocol.ContentText, Text: in.Prompt})
	}
	userMessage.Content = append(userMessage.Content, in.Content...)
	if err := userMessage.Validate(); err != nil {
		writeJSONError(w, 400, err.Error())
		return
	}
	for _, block := range userMessage.Content {
		if block.Type == protocol.ContentThinking {
			writeJSONError(w, 400, "user content must be text or image")
			return
		}
	}
	promptHash := hashPrompt(in)
	rt.mu.Lock()
	if old, ok := rt.runs[in.ClientRequestID]; ok {
		rt.mu.Unlock()
		if old.PromptHash != promptHash {
			writeJSONError(w, http.StatusConflict, "client_request_id was already used with a different prompt")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"version": protocol.Version, "conversation_id": rt.id, "run_id": old.RunID, "accepted_seq": old.AcceptedSeq})
		return
	}
	if rt.deleted {
		rt.mu.Unlock()
		writeJSONError(w, 404, "session not found")
		return
	}
	if rt.activeLocked() {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "session already has a running turn")
		return
	}
	if in.Model != nil && (in.Model.Model != "" || in.Model.Provider != "") {
		selected := *in.Model
		if err := s.switchModelLocked(rt, selected); err != nil {
			rt.mu.Unlock()
			writeJSONError(w, http.StatusBadRequest, "model switch failed: "+err.Error())
			return
		}
	}
	if err := s.refreshSettingsLocked(rt); err != nil {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.ResumeMessageID != "" {
		messages := rt.agent.MessagesSnapshot()
		submitted, _ := userMessage.ToAIMessage()
		if len(messages) == 0 || messages[len(messages)-1].Role != "user" || messages[len(messages)-1].ID != in.ResumeMessageID || !sameUserContent(messages[len(messages)-1], submitted) {
			rt.mu.Unlock()
			writeJSONError(w, 409, "resume_message_id and content must match the tail user message")
			return
		}
		userMessage = protocol.FromAIMessage(messages[len(messages)-1])
	}
	runID := newRunID()
	rt.runID = runID
	rt.runDone = false
	ctx, cancel := context.WithCancel(context.Background())
	rt.cancel = cancel
	rt.mu.Unlock()
	accepted, err := rt.publish(protocol.EventRunAccepted, map[string]any{"client_request_id": in.ClientRequestID}, "")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if in.ResumeMessageID == "" {
		if _, err := rt.publish(protocol.EventUserMessage, userMessage, ""); err != nil {
			cancel()
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	rec := runRecord{ClientRequestID: in.ClientRequestID, PromptHash: promptHash, RunID: runID, AcceptedSeq: accepted.Seq}
	rt.mu.Lock()
	rt.runs[in.ClientRequestID] = rec
	_ = rt.persistRuns(s.eventDir)
	rt.mu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]any{"version": protocol.Version, "conversation_id": id, "run_id": runID, "accepted_seq": accepted.Seq})
	go s.executeRun(rt, ctx, runID, in)
}

func (s *Server) executeRun(rt *runtimeSession, ctx context.Context, runID string, in protocol.PromptRequest) {
	message := protocol.Message{Role: protocol.RoleUser}
	if in.Prompt != "" {
		message.Content = append(message.Content, protocol.ContentBlock{Type: protocol.ContentText, Text: in.Prompt})
	}
	message.Content = append(message.Content, in.Content...)
	converted, _ := message.ToAIMessage()
	gate := func(req tools.GateRequest) (tools.GateDecision, string) { return s.waitPermission(rt, runID, req, ctx) }
	ctx = tools.WithGate(ctx, gate)
	ev := agent.Events{OnText: func(d string) { _, _ = rt.publish(protocol.EventTextDelta, protocol.TextDelta{Text: d}, "") }, OnThink: func(d string) { _, _ = rt.publish(protocol.EventThinkingDelta, protocol.TextDelta{Text: d}, "") }, OnToolStart: func(id, name, args string) {
		_, _ = rt.publish(protocol.EventToolCall, protocol.ToolCallEvent{ToolCall: protocol.ToolCall{ID: id, Name: name, Arguments: json.RawMessage(args)}}, "")
	}, OnToolResult: func(id, name string, result tools.Result) {
		_, _ = rt.publish(protocol.EventToolResult, protocol.ToolResultEvent{ToolResult: protocol.ToolResult{ID: id, Name: name, Output: result.Text, Failed: result.Failed, Cancelled: result.Cancelled}}, "")
	}, OnToolOutput: func(id, output string) {
		_, _ = rt.publish(protocol.EventToolStatus, protocol.ToolStatus{ToolCallID: id, Status: "running", Message: output}, "")
	}, OnUsage: func(u ai.Usage) {
		_, _ = rt.publish(protocol.EventUsage, protocol.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, CachedTokens: u.Cached(), CacheWriteTokens: u.CacheWrite()}, "")
	}}
	ev = agent.FanIn(rt.recorder.Events(), ev)
	var err error
	if in.ResumeMessageID != "" {
		_, err = rt.agent.ContinueUser(ctx, in.ResumeMessageID, ev)
	} else {
		_, err = rt.agent.TurnParts(ctx, converted.Content, converted.Parts, ev)
	}
	rt.mu.Lock()
	if rt.recorder != nil {
		err = errors.Join(err, rt.recorder.Save())
	}
	rt.mu.Unlock()
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		_, _ = rt.publish(protocol.EventRunCancelled, protocol.RunTerminal{State: "cancelled"}, "")
	} else if err != nil {
		_, _ = rt.publish(protocol.EventRunFailed, protocol.RunTerminal{State: "failed", Error: err.Error()}, "")
	} else {
		messages := rt.agent.MessagesSnapshot()
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role != protocol.RoleAssistant {
				continue
			}
			if message, messageErr := protocol.FromAIMessageValidated(messages[i]); messageErr == nil {
				_, _ = rt.publish(protocol.EventAssistantMessage, message, "")
			}
			break
		}
		_, _ = rt.publish(protocol.EventRunCompleted, protocol.RunTerminal{State: "completed"}, "")
	}
	rt.mu.Lock()
	rt.runDone = true
	rt.cancel = nil
	if rec, ok := rt.runs[in.ClientRequestID]; ok {
		rec.Terminal = true
		rt.runs[in.ClientRequestID] = rec
		_ = rt.persistRuns(s.eventDir)
	}
	old := rt.changed
	rt.changed = make(chan struct{})
	close(old)
	rt.mu.Unlock()
}

func (s *Server) waitPermission(rt *runtimeSession, runID string, req tools.GateRequest, ctx context.Context) (tools.GateDecision, string) {
	id := newRunID()
	p := &permissionWaiter{request: protocol.PermissionRequest{PermissionID: id, Tool: req.Tool, Command: req.Command, Rule: req.Rule, Options: []protocol.PermissionOption{{ID: "allow_once", Label: "Allow once", Kind: "allow_once"}, {ID: "reject", Label: "Reject", Kind: "reject_once"}}}, result: make(chan tools.GateDecision, 1), reason: make(chan string, 1)}
	rt.mu.Lock()
	rt.permissions[id] = p
	rt.mu.Unlock()
	s.publish(rt, protocol.EventPermissionRequest, p.request, "")
	defer func() { rt.mu.Lock(); delete(rt.permissions, id); rt.mu.Unlock() }()
	select {
	case d := <-p.result:
		reason := ""
		select {
		case reason = <-p.reason:
		default:
		}
		s.publish(rt, protocol.EventPermissionResult, protocol.PermissionDecision{PermissionID: id, Decision: decisionName(d), Reason: reason}, "")
		return d, reason
	case <-ctx.Done():
		return tools.GateReject, ctx.Err().Error()
	}
}
func decisionName(d tools.GateDecision) string {
	if d == tools.GateAllowOnce {
		return "allow_once"
	}
	if d == tools.GateAllowAlways {
		return "allow_always"
	}
	return "reject"
}

func (s *Server) events(w http.ResponseWriter, r *http.Request, id string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	for {
		rt.mu.Lock()
		pending := make([]protocol.Event, 0)
		for _, e := range rt.events {
			if e.Seq > after {
				pending = append(pending, e)
			}
		}
		changed := rt.changed
		active := rt.cancel != nil && !rt.runDone
		rt.mu.Unlock()
		for _, e := range pending {
			if err := writeSSE(w, e); err != nil {
				return
			}
			after = e.Seq
			if e.Type == protocol.EventRunCompleted || e.Type == protocol.EventRunFailed || e.Type == protocol.EventRunCancelled {
				flusher.Flush()
				return
			}
		}
		flusher.Flush()
		if !active {
			return
		}
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		}
	}
}
func writeSSE(w io.Writer, e protocol.Event) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.Seq, b)
	return err
}

func (s *Server) cancelRun(w http.ResponseWriter, r *http.Request, id, run string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	rt.mu.Lock()
	if rt.runID != run || rt.cancel == nil {
		rt.mu.Unlock()
		writeJSONError(w, http.StatusConflict, "run is not active")
		return
	}
	cancel := rt.cancel
	rt.mu.Unlock()
	cancel()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
func (s *Server) closeSession(w http.ResponseWriter, r *http.Request, id string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	rt.mu.Lock()
	if rt.cancel != nil {
		rt.cancel()
	}

	rt.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": id})
}
func (s *Server) permission(w http.ResponseWriter, r *http.Request, id, pid string) {
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	var in protocol.PermissionDecisionRequest
	if err = decodeJSON(w, r, &in); err != nil {
		return
	}
	rt.mu.Lock()
	p := rt.permissions[pid]
	rt.mu.Unlock()
	if p == nil {
		writeJSONError(w, http.StatusNotFound, "permission request not found")
		return
	}
	var d tools.GateDecision
	switch in.Decision.Decision {
	case "allow_once":
		d = tools.GateAllowOnce
	case "allow_always":
		d = tools.GateAllowAlways
	case "reject":
		d = tools.GateReject
	default:
		writeJSONError(w, http.StatusBadRequest, "unsupported permission decision")
		return
	}
	if in.Decision.Reason != "" {
		select {
		case p.reason <- in.Decision.Reason:
		default:
		}
	}
	p.result <- d
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return err
	}
	return nil
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"version": protocol.Version, "error": msg})
}
func validID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func urlPathID(raw string) (string, error) {
	id, err := urlPathUnescape(raw)
	if err != nil || !validID(id) {
		return "", errors.New("invalid session id")
	}
	return id, nil
}
func urlPathUnescape(raw string) (string, error) { return strings.ReplaceAll(raw, "%2F", "/"), nil }
func hashPrompt(in protocol.PromptRequest) string {
	b, _ := json.Marshal(in)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func newRunID() string { return fmt.Sprintf("run-%d", time.Now().UnixNano()) }
