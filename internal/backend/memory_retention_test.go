package backend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
	"github.com/Stack-Cairn/K-brain/internal/tools"
	"github.com/Stack-Cairn/K-brain/internal/tools/bashrun"
)

func auditHeap() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}
func auditServer(t *testing.T) *Server {
	st, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s, err := New(Options{Store: st, EventDir: t.TempDir(), DefaultCWD: t.TempDir(), Models: []protocol.ModelRef{{Provider: "fixture", Model: "fixture-model"}}, Factory: func(_ context.Context, _ string, m protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(&scriptedClient{}, m.Model, 1024, "system"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func seedAuditHistory(t *testing.T, s *Server, n int) []string {
	ids := []string{}
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("audit-%04d", i)
		_, err := s.store.ImportHistory(id, id, "v1", "content", "{}", t.TempDir(), "fixture-model", "fixture", "audit", time.Time{}, time.Time{}, false, false, false, "", []ai.Message{{Role: "user", Content: strings.Repeat("x", 1<<20)}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}
func TestMemoryRuntimeRetention(t *testing.T) {
	s := auditServer(t)
	ids := seedAuditHistory(t, s, 24)
	base := auditHeap()
	for i, id := range ids {
		rt, err := s.loadRuntimeByID(id)
		if err != nil {
			t.Fatal(err)
		}
		s.releaseRuntime(rt)
		if (i+1)%8 == 0 {
			t.Logf("loaded=%d runtimes=%d retained_delta_MiB=%.1f", i+1, len(s.sessions), float64(uint64(max(0, int64(auditHeap())-int64(base))))/(1<<20))
		}
	}
	for _, id := range ids {
		w := httptest.NewRecorder()
		s.deleteSession(w, httptest.NewRequest(http.MethodDelete, "/", nil), id)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	if len(s.sessions) != 0 {
		t.Fatalf("deleted runtimes retained: %d", len(s.sessions))
	}
	t.Logf("after deleting all: runtimes=%d retained_delta_MiB=%.1f", len(s.sessions), float64(uint64(max(0, int64(auditHeap())-int64(base))))/(1<<20))
	runtime.KeepAlive(s)
}
func TestMemoryEventRetention(t *testing.T) {
	s := auditServer(t)
	id := seedAuditHistory(t, s, 1)[0]
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	base := auditHeap()
	for i := 0; i < 120; i++ {
		_, err := s.publish(rt, protocol.EventToolStatus, map[string]string{"tool_call_id": "tool", "status": "running", "message": strings.Repeat("x", i*(4<<10))}, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	retained := 0
	for _, e := range rt.events {
		retained += len(e.Payload)
	}
	if retained > maxCachedEventBytes {
		t.Fatalf("event cache exceeds budget: %d", retained)
	}
	all, err := rt.allEventsLocked()
	if err != nil || len(all) != 120 {
		t.Fatalf("journal lost events: %d %v", len(all), err)
	}
	all = nil
	t.Logf("events=%d latest_output_KiB=%d retained_payload_MiB=%.1f retained_heap_delta_MiB=%.1f", len(rt.events), 119*4, float64(retained)/(1<<20), float64(uint64(max(0, int64(auditHeap())-int64(base))))/(1<<20))
	runtime.KeepAlive(s)
}
func TestMemoryImportAndColdTrajectory(t *testing.T) {
	s := auditServer(t)
	base := auditHeap()
	ids := seedAuditHistory(t, s, 24)
	t.Logf("imported=%d runtimes=%d heap_delta_MiB=%.1f", len(ids), len(s.sessions), float64(int64(auditHeap())-int64(base))/(1<<20))
	for round := 0; round < 3; round++ {
		for _, id := range ids {
			_, err := s.store.ImportHistory(id, id, "v1", "content", "{}", "", "fixture-model", "fixture", "audit", time.Time{}, time.Time{}, false, false, false, "", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.trajectorySnapshot(id); err != nil {
				t.Fatal(err)
			}
		}
		if len(s.sessions) != 0 {
			t.Fatal("read-only import/trajectory instantiated runtimes")
		}
		t.Logf("repeat=%d runtimes=%d heap_delta_MiB=%.1f", round+1, len(s.sessions), float64(int64(auditHeap())-int64(base))/(1<<20))
	}
	runtime.KeepAlive(s)
}

func TestMemoryActualBashCumulativeOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	s := auditServer(t)
	id := seedAuditHistory(t, s, 1)[0]
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	rt.runID = "audit-run"
	var callbackMu sync.Mutex
	var callbackErr error
	active := true
	cmd := `python3 -c 'import sys,time; [(sys.stdout.write("x"*16384),sys.stdout.flush(),time.sleep(.1)) for _ in range(24)]'`
	result := bashrun.Run(t.Context(), bashrun.Options{Command: cmd, Timeout: 10 * time.Second, OnUpdate: func(output string) {
		callbackMu.Lock()
		defer callbackMu.Unlock()
		if !active {
			return
		}
		if _, err := rt.publish(protocol.EventToolStatus, protocol.ToolStatus{ToolCallID: "audit", Status: "running", Message: output}, ""); err != nil {
			callbackErr = err
		}
	}})
	if result.Exit != "" {
		t.Fatal(result.Exit)
	}
	callbackMu.Lock()
	active = false
	err = callbackErr
	callbackMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	size := 0
	for _, e := range rt.events {
		size += len(e.Payload)
	}
	if len(result.Output) != 384<<10 || size > maxCachedEventBytes {
		t.Fatalf("output or cache bound violated: output=%d cache=%d", len(result.Output), size)
	}
	t.Logf("actual bash final_output_KiB=%d snapshots=%d retained_payload_MiB=%.2f amplification=%.1fx", len(result.Output)/1024, len(rt.events), float64(size)/(1<<20), float64(size)/float64(len(result.Output)))
}
func TestMemoryColdTrajectoryAllocation(t *testing.T) {
	s := auditServer(t)
	id := seedAuditHistory(t, s, 1)[0]
	cold := &runtimeSession{id: id, runID: "audit-run", eventDir: s.eventDir, changed: make(chan struct{})}
	for i := 0; i < 64; i++ {
		if _, err := cold.publish(protocol.EventToolStatus, protocol.ToolStatus{ToolCallID: "audit", Status: "running", Message: strings.Repeat("x", 64<<10)}, ""); err != nil {
			t.Fatal(err)
		}
	}
	cold = nil
	before := auditHeap()
	var first, last runtime.MemStats
	runtime.ReadMemStats(&first)
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodGet, "/trajectory?max_segments=1&redact_tool_content=true", nil)
		w := httptest.NewRecorder()
		s.trajectory(w, req, id, false)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	runtime.ReadMemStats(&last)
	after := auditHeap()
	t.Logf("cold 4MiB journal: requests=10 runtime_count=%d allocated_MiB=%.1f post_GC_heap_delta_MiB=%.1f", len(s.sessions), float64(last.TotalAlloc-first.TotalAlloc)/(1<<20), float64(int64(after)-int64(before))/(1<<20))
	runtime.KeepAlive(s)
}

func TestMemoryIdleEvictionAndReload(t *testing.T) {
	s := auditServer(t)
	ids := seedAuditHistory(t, s, 24)
	for _, id := range ids {
		rt, err := s.loadRuntimeByID(id)
		if err != nil {
			t.Fatal(err)
		}
		s.releaseRuntime(rt)
	}
	s.reapRuntimes(time.Now())
	if len(s.sessions) > maxIdleRuntimes {
		t.Fatalf("cache has %d idle runtimes", len(s.sessions))
	}
	s.reapRuntimes(time.Now().Add(runtimeIdleTTL))
	if len(s.sessions) != 0 {
		t.Fatal("idle cache not empty")
	}
	rt, err := s.loadRuntimeByID(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseRuntime(rt)
	messages := rt.agent.MessagesSnapshot()
	found := false
	for _, message := range messages {
		if message.Role == "user" && len(message.Content) == 1<<20 {
			found = true
		}
	}
	if !found {
		t.Fatalf("history lost on reload: %d messages", len(messages))
	}
}

func TestMemoryReaperPreservesLeasedAndActiveRuntimes(t *testing.T) {
	s := auditServer(t)
	ids := seedAuditHistory(t, s, 3)
	first, _ := s.loadRuntimeByID(ids[0])
	active, _ := s.loadRuntimeByID(ids[1])
	inactive, _ := s.loadRuntimeByID(ids[2])
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = ctx
	active.cancel = cancel
	s.releaseRuntime(active)
	s.releaseRuntime(inactive)
	s.reapRuntimes(time.Now().Add(runtimeIdleTTL))
	if s.sessions[ids[0]] != first || s.sessions[ids[1]] != active || s.sessions[ids[2]] != nil {
		t.Fatal("reaper dropped a leased/active runtime or retained an idle one")
	}
	s.releaseRuntime(first)
	active.mu.Lock()
	active.cancel = nil
	active.runDone = true
	active.mu.Unlock()
	s.reapRuntimes(time.Now().Add(runtimeIdleTTL))
	if len(s.sessions) != 0 {
		t.Fatal("completed runtimes retained")
	}
}

func TestMemoryBoundedEventsReplayAcrossRestart(t *testing.T) {
	s := auditServer(t)
	id := seedAuditHistory(t, s, 1)[0]
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxCachedEvents+100; i++ {
		if _, err = s.publish(rt, protocol.EventToolStatus, protocol.ToolStatus{Status: "running", Message: "tail"}, ""); err != nil {
			t.Fatal(err)
		}
	}
	rt.mu.Lock()
	firstSeq := rt.events[0].Seq
	rt.mu.Unlock()
	if firstSeq <= 1 {
		t.Fatal("cache did not evict old events")
	}
	s.releaseRuntime(rt)
	s.reapRuntimes(time.Now().Add(runtimeIdleTTL))
	rt, err = s.loadRuntimeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(rt.events) > maxCachedEvents || rt.eventBytes > maxCachedEventBytes {
		t.Fatal("journal reload exceeded cache budget")
	}
	w := httptest.NewRecorder()
	s.events(w, httptest.NewRequest(http.MethodGet, "/events?after_seq=0", nil), id)
	if w.Code != 200 || strings.Count(w.Body.String(), "\ndata:") != maxCachedEvents+100 {
		t.Fatalf("incomplete SSE replay: status=%d events=%d", w.Code, strings.Count(w.Body.String(), "\ndata:"))
	}
	s.releaseRuntime(rt)
}

func TestMemoryConcurrentAcquireAndReap(t *testing.T) {
	s := auditServer(t)
	id := seedAuditHistory(t, s, 1)[0]
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 25 {
				rt, err := s.loadRuntimeByID(id)
				if err != nil {
					t.Error(err)
					return
				}
				rt.mu.Lock()
				if rt.agent == nil || rt.closing {
					t.Error("leased runtime was evicted")
				}
				rt.mu.Unlock()
				s.releaseRuntime(rt)
			}
		}()
	}
	for range 100 {
		s.reapRuntimes(time.Now().Add(runtimeIdleTTL))
	}
	wg.Wait()
	s.reapRuntimes(time.Now().Add(runtimeIdleTTL))
	if len(s.sessions) != 0 {
		t.Fatal("leases leaked")
	}
}

func TestMemoryReaperPreservesQuarantine(t *testing.T) {
	s := auditServer(t)
	id := seedAuditHistory(t, s, 1)[0]
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	rt.runtimeErr = errors.New("persistence failed")
	s.releaseRuntime(rt)
	s.reapRuntimes(time.Now().Add(runtimeIdleTTL))
	if s.sessions[id] != rt {
		t.Fatal("reaper bypassed persistence quarantine")
	}
}

func TestMemoryConcurrentDeleteAndLoad(t *testing.T) {
	s := auditServer(t)
	id := seedAuditHistory(t, s, 1)[0]
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 12 {
				rt, err := s.loadRuntimeByID(id)
				if err != nil {
					if !errors.Is(err, session.ErrNotFound) {
						t.Error(err)
					}
					return
				}
				s.releaseRuntime(rt)
			}
		}()
	}
	w := httptest.NewRecorder()
	s.deleteSession(w, httptest.NewRequest(http.MethodDelete, "/", nil), id)
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	wg.Wait()
	if _, err := s.loadRuntimeByID(id); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("deleted session revived: %v", err)
	}
	if len(s.sessions) != 0 {
		t.Fatal("deleted session retained")
	}
}

func TestMemoryRunAcceptanceSurvivesEviction(t *testing.T) {
	s := auditServer(t)
	id := seedAuditHistory(t, s, 1)[0]
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.PromptRequest{ConversationID: id, ClientRequestID: "original-request", Prompt: "hello"}
	accepted, status, err := s.startCanonicalRun(t.Context(), rt, request)
	if err != nil || status != http.StatusAccepted {
		t.Fatalf("start: %d %v", status, err)
	}
	rt.runWorkers.Wait()
	s.releaseRuntime(rt)
	s.reapRuntimes(time.Now().Add(runtimeIdleTTL))
	if len(s.sessions) != 0 {
		t.Fatal("runtime not evicted")
	}
	rt, err = s.loadRuntimeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseRuntime(rt)
	replay, status, err := s.startCanonicalRun(t.Context(), rt, request)
	if err != nil || status != http.StatusOK || replay.RunID != accepted.RunID {
		t.Fatalf("acceptance lost: %+v %d %v", replay, status, err)
	}
}

func TestMemoryReaperPreservesLiveTerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	s := auditServer(t)
	id := seedAuditHistory(t, s, 1)[0]
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(tools.WithRunIdentity(tools.WithWorkingDir(t.Context(), t.TempDir()), tools.RunIdentity{ConversationID: id, RunID: "terminal-run"}))
	defer cancel()
	if _, err := rt.terminalManager.Start(ctx, "sleep 30", tools.WorkingDir(ctx)); err != nil {
		t.Fatal(err)
	}
	s.releaseRuntime(rt)
	s.reapRuntimes(time.Now().Add(runtimeIdleTTL))
	if s.sessions[id] != rt {
		t.Fatal("live terminal evicted")
	}
	rt.terminalManager.CloseAll()
	s.reapRuntimes(time.Now().Add(runtimeIdleTTL))
	if len(s.sessions) != 0 {
		t.Fatal("completed terminal runtime retained")
	}
}

func TestMemoryOversizedEventReplay(t *testing.T) {
	s := auditServer(t)
	id := seedAuditHistory(t, s, 1)[0]
	rt, err := s.loadRuntimeByID(id)
	if err != nil {
		t.Fatal(err)
	}
	defer s.releaseRuntime(rt)
	event, err := s.publish(rt, protocol.EventToolStatus, protocol.ToolStatus{Status: "running", Message: strings.Repeat("x", maxCachedEventBytes+1)}, "")
	if err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.events) != 0 || rt.eventBytes != 0 {
		t.Fatal("oversized event retained")
	}
	replay, err := rt.eventsAfterLocked(0)
	if err != nil || len(replay) != 1 || replay[0].Seq != event.Seq || string(replay[0].Payload) != string(event.Payload) {
		t.Fatalf("oversized event lost: count=%d err=%v", len(replay), err)
	}
	cold := &runtimeSession{id: id, runs: map[string]runRecord{}}
	if err := cold.loadJournal(s.eventDir); err != nil || cold.nextSeq != event.Seq || len(cold.events) != 0 {
		t.Fatalf("oversized journal reload: seq=%d err=%v", cold.nextSeq, err)
	}
}
