package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/ai"
)

func TestSubagentControlFollowupRetainsContextAndIdentity(t *testing.T) {
	srv, requests := modelRecorder(t, "reply")
	defer srv.Close()
	a := New(ai.New(srv.URL, "k"), "m", 100, "sys")
	task := a.StartBackground("control test", "original prompt", SubModel{})
	select {
	case <-task.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("task did not finish")
	}
	args, _ := json.Marshal(map[string]string{"id": task.ID, "message": "continue original"})
	result, err := findTool(t, a, "subagent_followup").Run(t.Context(), args)
	if err != nil || result != "reply" {
		t.Fatalf("followup: %q %v", result, err)
	}
	if len(requests()) != 2 {
		t.Fatalf("requests=%d", len(requests()))
	}
	snap, ok := a.Tasks().Get(task.ID)
	if !ok || snap.FollowingUp || len(a.Tasks().List()) != 1 {
		t.Fatal("follow-up changed identity")
	}
	found := false
	for _, m := range snap.SubMessages {
		if m.Content == "original prompt" {
			found = true
		}
	}
	if !found {
		t.Fatal("original context lost")
	}
	status, err := findTool(t, a, "subagent_status").Run(t.Context(), json.RawMessage(fmt.Sprintf(`{"id":%q}`, task.ID)))
	if err != nil || !strings.Contains(status, `"can_followup":true`) {
		t.Fatalf("status=%s %v", status, err)
	}
}

func TestSubagentControlOwnershipAndCancellation(t *testing.T) {
	a := New(ai.New("http://unused", "k"), "m", 100, "sys")
	b := New(ai.New("http://unused", "k"), "m", 100, "sys")
	task := a.RegisterBackground("owned", "task", SubModel{})
	defer task.cancel()
	arg := json.RawMessage(fmt.Sprintf(`{"id":%q,"message":"hello"}`, task.ID))
	for _, name := range []string{"subagent_status", "subagent_cancel", "subagent_followup"} {
		if _, err := findTool(t, b, name).Run(t.Context(), arg); err == nil {
			t.Fatalf("%s accepted foreign task", name)
		}
	}
	out, err := findTool(t, a, "subagent_cancel").Run(t.Context(), arg)
	if err != nil || !strings.Contains(out, "Cancellation requested") {
		t.Fatalf("cancel=%s %v", out, err)
	}
	if task.ctx.Err() != context.Canceled {
		t.Fatal("task not cancelled")
	}
	if err := a.SteerTask(task.ID, "late guidance"); err == nil {
		t.Fatal("cancelling task accepted guidance")
	}
	a.Tasks().settle(task.ID, TaskCancelled, "cancelled")
	out, err = findTool(t, a, "subagent_cancel").Run(t.Context(), arg)
	if err != nil || !strings.Contains(out, "inactive") {
		t.Fatalf("repeated cancel=%s %v", out, err)
	}
}

func TestSubagentSteeringLimitsAndConcurrentAdmission(t *testing.T) {
	a := New(ai.New("http://unused", "k"), "m", 100, "sys")
	task := a.RegisterBackground("owned", "task", SubModel{})
	defer task.cancel()
	for _, text := range []string{"  ", strings.Repeat("x", maxSubagentMessageBytes+1)} {
		if err := a.SteerTask(task.ID, text); err == nil {
			t.Fatal("invalid steer accepted")
		}
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for range 64 {
		wg.Go(func() {
			if a.SteerTask(task.ID, "message") == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if accepted != maxPendingSubagentMessages {
		t.Fatalf("accepted=%d", accepted)
	}
	if len(task.sub.drainPending()) != maxPendingSubagentMessages {
		t.Fatal("queue limit violated")
	}
	if err := a.SteerTask(task.ID, "after drain"); err != nil {
		t.Fatal(err)
	}
}

func TestFollowupRejectsFinalizingTask(t *testing.T) {
	a := New(ai.New("http://unused", "k"), "m", 100, "sys")
	task := a.RegisterBackground("owned", "task", SubModel{})
	defer task.cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	a.Tasks().OnRecord = func(_ string, _ *BackgroundTask) { close(entered); <-release }
	finished := make(chan struct{})
	go func() { a.Tasks().settle(task.ID, TaskDone, "original"); close(finished) }()
	<-entered
	cleared := a.Tasks().ClearSettled()
	_, err := a.FollowupTask(t.Context(), task.ID, "followup", Events{})
	close(release)
	<-finished
	if cleared != 0 {
		t.Fatal("finalizing task removed before persistence")
	}
	if err == nil || !strings.Contains(err.Error(), "finalizing") {
		t.Fatalf("followup=%v", err)
	}
}

func TestSubagentStatusBoundedAndRestoredNotResumable(t *testing.T) {
	a := New(ai.New("http://unused", "k"), "m", 100, "sys")
	for i := 0; i < 120; i++ {
		a.RestoreTask(BackgroundTask{ID: fmt.Sprintf("task-%03d", i), Status: TaskDone, Description: strings.Repeat("文", 1000), Report: strings.Repeat("r", 10000)})
	}
	tool := findTool(t, a, "subagent_status")
	out, err := tool.Run(t.Context(), json.RawMessage(`{}`))
	if err != nil || len(out) > 50000 || !strings.Contains(out, `"truncated":true`) || !strings.Contains(out, `"total":120`) {
		t.Fatalf("invalid bounded status: bytes=%d err=%v", len(out), err)
	}
	again, _ := tool.Run(t.Context(), json.RawMessage(`{}`))
	if out != again {
		t.Fatal("unstable ordering")
	}
	out, err = tool.Run(t.Context(), json.RawMessage(`{"id":"task-000"}`))
	if err != nil || !strings.Contains(out, `"can_followup":false`) {
		t.Fatalf("restored status=%s %v", out, err)
	}
	if _, err = findTool(t, a, "subagent_followup").Run(t.Context(), json.RawMessage(`{"id":"task-000","message":"resume"}`)); err == nil {
		t.Fatal("restored summary resumed without context")
	}
}

func TestSubagentControlRejectsCancelledCaller(t *testing.T) {
	a := New(ai.New("http://unused", "k"), "m", 100, "sys")
	task := a.RegisterBackground("owned", "task", SubModel{})
	defer task.cancel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	arg := json.RawMessage(fmt.Sprintf(`{"id":%q,"message":"do not deliver"}`, task.ID))
	for _, name := range []string{"subagent_status", "subagent_cancel", "subagent_followup", "subagent_steer"} {
		if _, err := findTool(t, a, name).Run(ctx, arg); err != context.Canceled {
			t.Fatalf("%s err=%v", name, err)
		}
	}
	if task.ctx.Err() != nil || len(task.sub.drainPending()) != 0 {
		t.Fatal("cancelled caller mutated child")
	}
}
