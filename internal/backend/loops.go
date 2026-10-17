package backend

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// Largest "*/n" step recognised per unit when listing loops back as an interval.
const (
	maxLoopMinutes = 59
	maxLoopHours   = 23
	maxLoopDays    = 31
)

// loopEntry is one interval prompt bound to a conversation. Interval is the compact
// form the composer accepts ("10m", "2h", "1d"); Cron is what the scheduler stores.
type loopEntry struct {
	ID         string `json:"id"`
	Interval   string `json:"interval"`
	Cron       string `json:"cron"`
	Prompt     string `json:"prompt"`
	Enabled    bool   `json:"enabled"`
	NextFireAt int64  `json:"nextFireAt,omitempty"`
	ExpiresAt  int64  `json:"expiresAt,omitempty"`
}

type loopCreateInput struct {
	Cron      string `json:"cron"`
	Interval  string `json:"interval"`
	Prompt    string `json:"prompt"`
	Name      string `json:"name,omitempty"`
	Workdir   string `json:"workdir,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
}

type loopList struct {
	Version string      `json:"version"`
	Loops   []loopEntry `json:"loops"`
}

// loopInterval reads back the compact interval of a generated cron expression. Only
// the shapes produced by the composer are recognised; other expressions list verbatim.
func loopInterval(expression string) string {
	fields := strings.Fields(expression)
	if len(fields) != 6 {
		return expression
	}
	step := func(field string, max int) string {
		n, err := strconv.Atoi(strings.TrimPrefix(field, "*/"))
		if !strings.HasPrefix(field, "*/") || err != nil || n < 1 || n > max {
			return ""
		}
		return strconv.Itoa(n)
	}
	if n := step(fields[1], maxLoopMinutes); n != "" && fields[0] == "0" && strings.Join(fields[2:], " ") == "* * * *" {
		return n + "m"
	}
	if n := step(fields[2], maxLoopHours); n != "" && fields[0] == "0" && fields[1] == "0" && strings.Join(fields[3:], " ") == "* * *" {
		return n + "h"
	}
	if n := step(fields[3], maxLoopDays); n != "" && fields[0] == "0" && fields[1] == "0" && fields[2] == "0" && fields[4] == "*" && fields[5] == "*" {
		return n + "d"
	}
	return expression
}

// validateLoopCron keeps conversation loops at most once a minute: the seconds field
// must be one fixed second, so a loop cannot fire every second.
func validateLoopCron(expression string) error {
	if err := validateCronExpression(expression); err != nil {
		return err
	}
	if n, err := strconv.Atoi(strings.Fields(expression)[0]); err != nil || n < 0 || n > 59 {
		return errors.New("loop interval must be at least one minute: the seconds field must be a single fixed second")
	}
	return nil
}

// loopIDPrefix marks conversation loops; the cron apply route rejects it for other
// tasks, so the prefix alone identifies a loop even after its fields are edited.
const loopIDPrefix = "loop-"

func isLoopTask(task *CronTask) bool {
	return task != nil && strings.HasPrefix(task.ID, loopIDPrefix)
}

// newLoopID is short enough to type in "/loop cancel <id>".
func newLoopID() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return loopIDPrefix + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return loopIDPrefix + hex.EncodeToString(b[:])
}

func loopView(task *CronTask) loopEntry {
	entry := loopEntry{ID: task.ID, Interval: loopInterval(task.Cron), Cron: task.Cron, Prompt: task.Prompt, Enabled: task.Enabled}
	if task.Loop != nil {
		entry.Interval = compactLoopInterval(task.Loop.IntervalSeconds)
		entry.NextFireAt = task.Loop.NextFireAt
		entry.ExpiresAt = task.Loop.ExpiresAt
	}
	return entry
}

// addSessionLoop appends a prompt task already bound to the conversation. Clients cannot
// pass sessionId through the cron apply route, so binding happens here instead.
func (c *cronStore) addSessionLoop(task *CronTask, sessionID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if task.ID == "" {
		task.ID = newRunID()
	}
	for _, existing := range c.disk.Tasks {
		if existing != nil && existing.ID == task.ID {
			return errors.New("duplicate cron task id")
		}
	}
	if task.Loop != nil {
		active := 0
		for _, existing := range c.disk.Tasks {
			if isLoopTask(existing) && existing.Enabled && (existing.Loop == nil || existing.Loop.ExpiresAt > time.Now().UnixMilli()) {
				active++
			}
		}
		if active >= maxSessionLoops {
			return errors.New("maximum of 50 active loops reached")
		}
	}
	task.SessionID = strings.TrimSpace(sessionID)
	next := append(cloneCronTasks(c.disk.Tasks), cloneCronTasks([]*CronTask{task})[0])
	if err := validateCronTasks(next); err != nil {
		return err
	}
	old := c.disk
	c.disk.Tasks = next
	c.disk.Revision++
	if err := c.persistLocked(); err != nil {
		c.disk = old
		return err
	}
	return nil
}

func (s *Server) handleSessionLoops(w http.ResponseWriter, r *http.Request, id string, tail []string) {
	if s.cron == nil {
		writeJSONError(w, http.StatusNotImplemented, "backend cron is unavailable")
		return
	}
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	defer s.releaseRuntime(rt)
	if len(tail) == 0 {
		if r.Method == http.MethodGet {
			s.listSessionLoops(w, id)
			return
		}
		if r.Method == http.MethodPost {
			s.createSessionLoop(w, r, rt)
			return
		}
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if len(tail) == 2 && tail[1] == "cancel" && r.Method == http.MethodPost {
		s.cancelSessionLoop(w, id, tail[0])
		return
	}
	if len(tail) == 1 && r.Method == http.MethodDelete {
		s.cancelSessionLoop(w, id, tail[0])
		return
	}
	writeJSONError(w, http.StatusNotFound, "loop route not found")
}

func (s *Server) listSessionLoops(w http.ResponseWriter, id string) {
	snapshot := s.cron.store.snapshot()
	loops := make([]loopEntry, 0)
	for _, task := range snapshot.Tasks {
		if isLoopTask(task) && task.SessionID == id && task.Type == "prompt" {
			loops = append(loops, loopView(task))
		}
	}
	writeJSON(w, http.StatusOK, loopList{Version: protocol.Version, Loops: loops})
}

func (s *Server) createSessionLoop(w http.ResponseWriter, r *http.Request, rt *runtimeSession) {
	var in loopCreateInput
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	cron := strings.TrimSpace(in.Cron)
	interval := strings.TrimSpace(in.Interval)
	if (cron == "" && interval == "") || strings.TrimSpace(in.Prompt) == "" {
		writeJSONError(w, http.StatusBadRequest, "loop interval (or legacy cron) and prompt are required")
		return
	}
	var schedule *loopSchedule
	if interval != "" {
		seconds, _, err := parseLoopInterval(interval)
		if err != nil {
			writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		now := time.Now()
		schedule = &loopSchedule{IntervalSeconds: seconds, NextFireAt: now.UnixMilli(), ExpiresAt: now.Add(loopLifetime).UnixMilli()}
		cron = "0 * * * * *"
	}
	if err := validateLoopCron(cron); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	// A loop runs with the conversation's current model and workspace.
	rt.mu.Lock()
	selected, workdir := protocol.ModelRef{}, strings.TrimSpace(in.Workdir)
	if rt.agent != nil {
		selected = protocol.ModelRef{Provider: rt.agent.Provider, Model: rt.agent.ModelName}
		if workdir == "" {
			workdir = rt.agent.WorkingDir
		}
	}
	rt.mu.Unlock()
	reasoning := strings.TrimSpace(in.Reasoning)
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = strings.TrimSpace(in.Prompt)
	}
	if len([]rune(name)) > 60 {
		name = string([]rune(name)[:60])
	}
	task := &CronTask{
		ID: newLoopID(), Name: name, Description: "loop", Cron: cron, Enabled: true, Loop: schedule,
		TimeoutSeconds: 600, Type: "prompt", Prompt: strings.TrimSpace(in.Prompt),
		SelectedModel: &CronModelRef{CustomProviderID: selected.Provider, Model: selected.Model},
		Reasoning:     reasoning,
		Workdir:       workdir,
	}
	if err := s.cron.store.addSessionLoop(task, rt.id); err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	entry := loopView(task)
	writeJSON(w, http.StatusCreated, entry)
}

// loopCancelBeforeApply lets tests change the cron store between snapshot and apply.
var loopCancelBeforeApply func()

func (s *Server) cancelSessionLoop(w http.ResponseWriter, id, loopID string) {
	// apply rejects a stale revision with a "conflict" status instead of an error, so a
	// concurrent cron change is retried against a fresh snapshot.
	for attempt := 0; attempt < 3; attempt++ {
		snapshot := s.cron.store.snapshot()
		found := false
		for _, task := range snapshot.Tasks {
			if isLoopTask(task) && task.ID == loopID && task.SessionID == id && task.Type == "prompt" {
				found = true
				break
			}
		}
		if !found {
			writeJSONError(w, http.StatusNotFound, "loop not found")
			return
		}
		if loopCancelBeforeApply != nil {
			loopCancelBeforeApply()
		}
		out, err := s.cron.store.apply(CronApplyInput{BaseRevision: snapshot.Revision, Ops: []CronOperation{{Op: "delete", ID: loopID}}})
		if err != nil {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		if out.Status == "ok" {
			// Deletion prevents new leases; cancel only this loop's existing worker.
			s.cron.cancelRemovedLoop(loopID)
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
			return
		}
	}
	writeJSONError(w, http.StatusConflict, "scheduled tasks changed concurrently; retry the cancel")
}

func (m *cronManager) cancelRemovedLoop(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if active := m.active[id]; active != nil {
		active.cancel()
	}
}

// forgetSession runs after a conversation is deleted: its loops are removed, and other
// prompt tasks bound to it are unbound so their next run opens a fresh conversation.
func (c *cronStore) forgetSession(sessionID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	old := cloneCronDisk(c.disk)
	changed := false
	tasks := make([]*CronTask, 0, len(c.disk.Tasks))
	removed := map[string]bool{}
	for _, task := range c.disk.Tasks {
		if task == nil || task.SessionID != sessionID {
			tasks = append(tasks, task)
			continue
		}
		changed = true
		if isLoopTask(task) {
			removed[task.ID] = true
			continue
		}
		task.SessionID = ""
		tasks = append(tasks, task)
	}
	if !changed {
		return nil
	}
	runs := make([]CronRunRecord, 0, len(c.disk.Runs))
	for _, run := range c.disk.Runs {
		if !removed[run.TaskID] {
			runs = append(runs, run)
		}
	}
	c.disk.Tasks, c.disk.Runs = tasks, runs
	c.disk.Revision++
	return c.commitLocked(old)
}
