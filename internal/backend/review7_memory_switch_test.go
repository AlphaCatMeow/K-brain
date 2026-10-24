package backend

// Seventh independent review of the memory master switch (through 07e1902). Reuses
// newReviewMemoryFixture and the review2/review3/review6 helpers (real backend, real
// memoryruntime, real session and cron stores); LIVEAGENT_HOME is isolated.

import (
	"net/http"
	"strings"
	"testing"
)

// 关闭期间用户在会话里用 /loop 创建循环任务（POST /v1/sessions/{id}/loops，原文是用户亲手写的）。
// be80155 只在 PUT /v1/cron 上记录 memoryOff，这条路由没有；重新打开后循环触发，
// 循环 prompt 作为用户消息进入隐藏提取。
func TestReview7LoopPromptWrittenWhileOffIsExtractedAfterReenable(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	root := t.TempDir()
	f := newReviewMemoryFixture(t, root, reviewFixtureConfig())
	const secret = "OFFSECRET-LOOP7-6161 I always deploy on Fridays"
	sess := createTestSession(t, f.url)
	f.turn(t, sess.ID, "on-1", "hello there friend", 0)

	f.setEnabled(t, false)
	var loop loopEntry
	// The legacy cron form (still accepted by this route) on a fixed yearly schedule, so
	// the scheduler does not fire it on its own; the interval form shares the create path.
	if resp := postJSON(t, http.DefaultClient, f.url+"/v1/sessions/"+sess.ID+"/loops", loopCreateInput{Cron: "0 0 0 1 1 *", Prompt: secret}, &loop); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create loop status %d", resp.StatusCode)
	}
	f.setEnabled(t, true)
	var accepted CronRunNowResponse
	if status := doCronJSON(t, f.url+"/v1/cron/"+loop.ID+"/run-now", http.MethodPost, nil, &accepted); status != http.StatusAccepted {
		t.Fatalf("run-now status %d", status)
	}
	runs := waitCronRuns(t, f.url, loop.ID, func(runs []CronRunRecord) bool { return len(runs) == 1 && runs[0].State == "done" })
	if !runs[0].Success {
		t.Fatalf("fixture: loop run %+v", runs[0])
	}
	if runs[0].SessionID != sess.ID {
		t.Fatalf("fixture: a cron-form loop runs in its conversation, got session %q want %q", runs[0].SessionID, sess.ID)
	}
	// A later turn written while on is judged after the loop turn, so the loop turn has
	// been judged or dropped by then.
	f.review6Sentinel(t, sess.ID, "s1")
	for _, request := range f.extractionRequests() {
		for _, m := range request {
			if strings.Contains(m.TextContent(), "OFFSECRET-LOOP7-6161") {
				t.Fatalf("a /loop prompt the user wrote while memory was off reached the memory extractor after re-enabling:\n%s", m.TextContent())
			}
		}
	}
}

// 同上，桌面端输入框实际使用的 interval 形式（LA client.createLoop 传 interval）：循环在独立
// worker 会话里执行，worker 的这条用户消息同样进入提取。
func TestReview7IntervalLoopPromptWrittenWhileOffIsExtractedAfterReenable(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	root := t.TempDir()
	f := newReviewMemoryFixture(t, root, reviewFixtureConfig())
	const secret = "OFFSECRET-LOOP7I-7171 I always deploy on Fridays"
	sess := createTestSession(t, f.url)

	f.setEnabled(t, false)
	var loop loopEntry
	if resp := postJSON(t, http.DefaultClient, f.url+"/v1/sessions/"+sess.ID+"/loops", loopCreateInput{Interval: "1d", Prompt: secret}, &loop); resp.StatusCode != http.StatusCreated {
		t.Fatalf("create loop status %d", resp.StatusCode)
	}
	f.setEnabled(t, true)
	var accepted CronRunNowResponse
	if status := doCronJSON(t, f.url+"/v1/cron/"+loop.ID+"/run-now", http.MethodPost, nil, &accepted); status != http.StatusAccepted {
		t.Fatalf("run-now status %d", status)
	}
	runs := waitCronRuns(t, f.url, loop.ID, func(runs []CronRunRecord) bool {
		return len(runs) > 0 && runs[0].State == "done"
	})
	worker := runs[0].SessionID
	if worker == "" || worker == sess.ID {
		t.Fatalf("fixture: an interval loop runs in its own worker session, got %q", worker)
	}
	// A later turn written while on in the worker session is judged after the loop turn.
	f.review6Sentinel(t, worker, "s1")
	for _, request := range f.extractionRequests() {
		for _, m := range request {
			if strings.Contains(m.TextContent(), "OFFSECRET-LOOP7I-7171") {
				t.Fatalf("an interval /loop prompt the user wrote while memory was off reached the memory extractor after re-enabling:\n%s", m.TextContent())
			}
		}
	}
}

const review7Notice = "Memory is turned off by the user"

// 验证（预期通过）：会话从关闭状态开始（从未注入过记忆），关→关→开→关→关。每段关闭期恰好
// 一次说明；开启那一轮在说明之后恢复记忆索引；第二段关闭再说明一次。
func TestReview7NoticeOncePerOffPeriodInFreshOffSession(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	f.setEnabled(t, false)
	sess := createTestSession(t, f.url)

	seq := f.turn(t, sess.ID, "off-1", "first off message", 0)
	seq = f.turn(t, sess.ID, "off-2", "second off message", seq)
	if n := strings.Count(f.review3LastRequestText(), review7Notice); n != 1 {
		t.Fatalf("first off period: want one notice, got %d", n)
	}
	f.setEnabled(t, true)
	seq = f.turn(t, sess.ID, "on-1", "back on", seq)
	text := f.review3LastRequestText()
	if strings.LastIndex(text, "prefers-go") < strings.LastIndex(text, review7Notice) {
		t.Fatalf("re-enabled turn must carry the memory index after the off notice:\n%s", text)
	}
	if tools, _ := f.client.lastRequest(t); !hasMemoryTool(tools) {
		t.Fatalf("re-enabled turn lost memory tools: %v", tools)
	}
	f.setEnabled(t, false)
	seq = f.turn(t, sess.ID, "off-3", "third off message", seq)
	f.turn(t, sess.ID, "off-4", "fourth off message", seq)
	text = f.review3LastRequestText()
	if n := strings.Count(text, review7Notice); n != 2 {
		t.Fatalf("two off periods: want two notices, got %d:\n%s", n, text)
	}
	if strings.LastIndex(text, review7Notice) < strings.LastIndex(text, "prefers-go") {
		t.Fatalf("second off period must end on the notice:\n%s", text)
	}
}

// 验证（预期通过）：会话从关闭状态开始，首条消息停止后恢复两次、再编辑重发（/edit 丢弃消息上的
// 快照）。说明始终恰好一次，编辑后由 markResumedMessage 补回。
func TestReview7NoticeOnceAcrossResumeAndEditInFreshOffSession(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	f := newReviewMemoryFixture(t, t.TempDir(), reviewFixtureConfig())
	f.setEnabled(t, false)
	sess := createTestSession(t, f.url)

	seq := f.review3CancelledTurn(t, sess.ID, "off-1", "please look at the build script", 0)
	seq = f.review3CancelledTurnResume(t, sess.ID, "off-resume-1", seq)
	seq = f.review3Resume(t, sess.ID, "off-resume-2", seq)
	if n := strings.Count(f.review3LastRequestText(), review7Notice); n != 1 {
		t.Fatalf("after two resumes: want one notice, got %d:\n%s", n, f.review3LastRequestText())
	}
	seq = f.review2EditAndResend(t, sess.ID, "please look at the deploy script instead", "off-edit", seq)
	if n := strings.Count(f.review3LastRequestText(), review7Notice); n != 1 {
		t.Fatalf("after edit-resend: want one notice, got %d:\n%s", n, f.review3LastRequestText())
	}
	f.turn(t, sess.ID, "off-2", "and the readme", seq)
	if n := strings.Count(f.review3LastRequestText(), review7Notice); n != 1 {
		t.Fatalf("next off turn: want one notice, got %d", n)
	}
}

func review7CronState(t *testing.T, base string) (uint64, map[string]bool) {
	t.Helper()
	var snapshot CronSnapshot
	if code := doCronJSON(t, base+"/v1/cron", http.MethodGet, nil, &snapshot); code != http.StatusOK {
		t.Fatalf("cron snapshot status %d", code)
	}
	marks := map[string]bool{}
	for _, task := range snapshot.Tasks {
		marks[task.ID] = task.MemoryOff
	}
	return snapshot.Revision, marks
}

func review7CronApply(t *testing.T, base string, revision uint64, ops ...CronOperation) {
	t.Helper()
	var out CronApplyResponse
	if code := doCronJSON(t, base+"/v1/cron", http.MethodPut, CronApplyInput{BaseRevision: revision, Ops: ops}, &out); code != http.StatusOK || out.Status != "ok" {
		t.Fatalf("cron apply status %d %q", code, out.Status)
	}
}

// 验证（预期通过）：cron memoryOff 由后端掌控。开启时客户端传 memoryOff:true 被覆盖；关闭时
// 创建的任务开启后客户端 patch memoryOff:false 清不掉；关闭期间只改 enabled、或 prompt 原样
// 回传不加标记；同一批里先改写再改回仍带标记（过度排除，可接受）。
func TestReview7CronMemoryOffIsBackendOwned(t *testing.T) {
	t.Setenv("LIVEAGENT_HOME", t.TempDir())
	root := t.TempDir()
	f := newReviewMemoryFixture(t, root, reviewFixtureConfig())
	item := func(id, prompt string, extra map[string]any) map[string]any {
		out := map[string]any{"id": id, "name": id, "cron": "0 0 0 1 1 0", "enabled": false, "type": "prompt", "prompt": prompt,
			"selectedModel": CronModelRef{CustomProviderID: "fixture", Model: "fixture-model"}, "workdir": root, "timeoutSeconds": 30}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	revision, _ := review7CronState(t, f.url)
	review7CronApply(t, f.url, revision,
		CronOperation{Op: "create", Item: item("on-forged", "written while on", map[string]any{"memoryOff": true})},
		CronOperation{Op: "create", Item: item("on-plain", "plain prompt written while on", nil)})
	revision, marks := review7CronState(t, f.url)
	if marks["on-forged"] {
		t.Fatal("a client-supplied memoryOff on create while on was kept")
	}
	f.setEnabled(t, false)
	review7CronApply(t, f.url, revision,
		CronOperation{Op: "create", Item: item("off-made", "written while off", map[string]any{"memoryOff": false})},
		CronOperation{Op: "update", ID: "on-plain", Patch: map[string]any{"enabled": true, "prompt": "plain prompt written while on", "memoryOff": true}},
		CronOperation{Op: "update", ID: "on-forged", Patch: map[string]any{"prompt": "rewritten while off"}},
		CronOperation{Op: "update", ID: "on-forged", Patch: map[string]any{"prompt": "written while on"}})
	f.setEnabled(t, true)
	revision, _ = review7CronState(t, f.url)
	review7CronApply(t, f.url, revision, CronOperation{Op: "update", ID: "off-made", Patch: map[string]any{"memoryOff": false, "enabled": true}})
	_, marks = review7CronState(t, f.url)
	want := map[string]bool{"on-plain": false, "off-made": true, "on-forged": true}
	for id, mark := range want {
		if marks[id] != mark {
			t.Fatalf("task %s memoryOff = %v, want %v (all: %v)", id, marks[id], mark, marks)
		}
	}
}
