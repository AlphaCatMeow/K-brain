package backend

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

func TestLoopElapsedIntervalParsing(t *testing.T) {
	for raw, want := range map[string]int64{"1s": 60, "60s": 60, "90s": 90, "90m": 5400, "25h": 90000, "7d": 604800, " 2H ": 7200} {
		got, _, err := parseLoopInterval(raw)
		if err != nil || got != want {
			t.Fatalf("%s: %d %v", raw, got, err)
		}
	}
	for _, raw := range []string{"0m", "-1s", "1.5h", "1w", "8d", "9223372036854775807d", "一m"} {
		if _, _, err := parseLoopInterval(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestLoopImmediateDetachedExecutionAndDelivery(t *testing.T) {
	client := &replyQueue{replies: []string{"parent secret", "loop result"}}
	server, store, base := newGoalTestServer(t, client)
	sess := createTestSession(t, base)
	var accepted protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ClientRequestID: "parent", Prompt: "PRIVATE_PARENT_CONTEXT"}, &accepted)
	resp.Body.Close()
	rt, _ := server.loadRuntimeByID(sess.ID)
	if err := waitForCanonicalRun(rt, accepted.RunID, context.Background()); err != nil {
		t.Fatal(err)
	}
	server.releaseRuntime(rt)
	var entry loopEntry
	resp = postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/loops", loopCreateInput{Interval: "90m", Prompt: "independent check"}, &entry)
	resp.Body.Close()
	if resp.StatusCode != 201 || entry.Interval != "90m" || entry.ExpiresAt <= entry.NextFireAt {
		t.Fatalf("create %d %+v", resp.StatusCode, entry)
	}
	runs := waitCronRuns(t, base, entry.ID, func(r []CronRunRecord) bool { return len(r) == 1 && r[0].FinishedAt != nil })
	if !runs[0].Success || runs[0].SessionID == sess.ID {
		t.Fatalf("worker: %+v", runs)
	}
	turns := client.userTurns()
	if len(turns) != 2 || strings.Contains(turns[1], "PRIVATE_PARENT_CONTEXT") || turns[1] != "independent check" {
		t.Fatalf("context: %q", turns)
	}
	client.mu.Lock()
	for _, message := range client.requests[1] {
		if strings.Contains(message.Content, "PRIVATE_PARENT_CONTEXT") || strings.Contains(message.Content, "parent secret") {
			t.Error("loop inherited parent history")
		}
	}
	client.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, msgs, err := store.Load(sess.ID)
		if err != nil {
			t.Fatal(err)
		}
		found := 0
		for _, m := range msgs {
			if strings.Contains(m.Content, "[Loop "+entry.ID) {
				found++
			}
		}
		if found == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("result not delivered")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for i := 0; i < 2; i++ {
		server.deliverLoopResults(server.cron.store.snapshot().Tasks[0])
	}
	_, msgs, _ := store.Load(sess.ID)
	found := 0
	for _, m := range msgs {
		if strings.Contains(m.Content, "[Loop "+entry.ID) {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("duplicate results: %d", found)
	}
	listed := listLoops(t, base, sess.ID)
	if len(listed) != 1 || listed[0].Interval != "90m" || listed[0].NextFireAt <= entry.NextFireAt {
		t.Fatalf("list: %+v", listed)
	}
	reloaded, err := newCronStore(store)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.snapshot().Tasks[0].Loop.NextFireAt != listed[0].NextFireAt {
		t.Fatal("restart lost next fire")
	}
}

func TestLoopExpiryLimitAndAtomicCancellation(t *testing.T) {
	client := &replyQueue{block: make(chan struct{})}
	server, _, base := newGoalTestServer(t, client)
	sess := createTestSession(t, base)
	var entry loopEntry
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/loops", loopCreateInput{Interval: "30s", Prompt: "blocked"}, &entry)
	resp.Body.Close()
	if entry.Interval != "1m" {
		t.Fatalf("minimum: %+v", entry)
	}
	waitTurns(t, client, 1)
	// A missed deadline while the worker is blocked must not overlap or queue runs.
	for i := 0; i < 3; i++ {
		server.cron.tickLoop(server.cron.store.snapshot().Tasks[0], time.Now().Add(2*time.Minute))
	}
	if len(client.userTurns()) != 1 {
		t.Fatal("loop overlapped its active occurrence")
	}
	resp = postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/loops/"+entry.ID+"/cancel", map[string]any{}, nil)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		server.cron.mu.Lock()
		active := server.cron.active[entry.ID] != nil
		server.cron.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancel left worker active")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for i := 0; i < maxSessionLoops; i++ {
		task := &CronTask{ID: newLoopID(), Name: "loop", Type: "prompt", Cron: "0 * * * * *", Enabled: true, Prompt: "p", TimeoutSeconds: 60, Workdir: sess.CWD, SelectedModel: &CronModelRef{CustomProviderID: "fixture", Model: "fixture-model"}, Loop: &loopSchedule{IntervalSeconds: 60, NextFireAt: time.Now().Add(time.Hour).UnixMilli(), ExpiresAt: time.Now().Add(loopLifetime).UnixMilli()}}
		if err := server.cron.store.addSessionLoop(task, sess.ID); err != nil {
			t.Fatal(err)
		}
	}
	resp = postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/loops", loopCreateInput{Interval: "1m", Prompt: "overflow"}, nil)
	resp.Body.Close()
	if resp.StatusCode != 422 {
		t.Fatalf("limit: %d", resp.StatusCode)
	}
	task := server.cron.store.snapshot().Tasks[0]
	server.cron.tickLoop(task, time.UnixMilli(task.Loop.ExpiresAt))
	if server.cron.store.snapshot().Tasks[0].Enabled {
		t.Fatal("expired loop enabled")
	}
	server.cron.store.mu.Lock()
	server.cron.store.task(task.ID).Loop.ExpiresAt = time.Now().Add(-time.Second).UnixMilli()
	server.cron.store.mu.Unlock()
	if _, _, err := server.cron.store.reserve(task.ID, false); err == nil {
		t.Fatal("expired loop accepted manual execution")
	}
}

func TestLoopDeferredResultDelivery(t *testing.T) {
	client := &replyQueue{block: make(chan struct{})}
	server, store, base := newGoalTestServer(t, client)
	sess := createTestSession(t, base)
	var accepted protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ClientRequestID: "busy-parent", Prompt: "busy"}, &accepted)
	resp.Body.Close()
	waitTurns(t, client, 1)
	task := &CronTask{ID: newLoopID(), Name: "deferred", Type: "prompt", Cron: "0 * * * * *", Enabled: true, Prompt: "check", TimeoutSeconds: 60, Workdir: sess.CWD, SelectedModel: &CronModelRef{CustomProviderID: "fixture", Model: "fixture-model"}, Loop: &loopSchedule{IntervalSeconds: 60, NextFireAt: time.Now().Add(time.Hour).UnixMilli(), ExpiresAt: time.Now().Add(loopLifetime).UnixMilli()}}
	if err := server.cron.store.addSessionLoop(task, sess.ID); err != nil {
		t.Fatal(err)
	}
	finished := time.Now().UnixMilli()
	server.cron.store.mu.Lock()
	server.cron.store.disk.Runs = append(server.cron.store.disk.Runs, CronRunRecord{ID: "finished-loop", TaskID: task.ID, FinishedAt: &finished, Success: true, State: "completed", Output: "deferred result"})
	server.cron.store.mu.Unlock()
	server.deliverLoopResults(task)
	runs, _ := server.cron.store.runs(task.ID, 10)
	if runs[0].Delivered {
		t.Fatal("delivered into an active parent")
	}
	close(client.block)
	rt, _ := server.loadRuntimeByID(sess.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitForCanonicalRun(rt, accepted.RunID, ctx); err != nil {
		t.Fatal(err)
	}
	server.releaseRuntime(rt)
	server.deliverLoopResults(task)
	// Simulate a crash after history save but before the delivery acknowledgement.
	server.cron.store.mu.Lock()
	server.cron.store.disk.Runs[0].Delivered = false
	server.cron.store.mu.Unlock()
	server.deliverLoopResults(task)
	_, messages, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, message := range messages {
		if message.ID == "loop-result-finished-loop" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("delivered %d copies", found)
	}
}
