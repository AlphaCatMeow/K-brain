package backend

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func listLoops(t *testing.T, base, id string) []loopEntry {
	t.Helper()
	resp, err := http.Get(base + "/v1/sessions/" + id + "/loops")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("loops status %d", resp.StatusCode)
	}
	var out loopList
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Loops
}

func TestSessionLoopLifecycleAndIsolation(t *testing.T) {
	_, base := newGoalServer(t, &replyQueue{})
	first := createTestSession(t, base)
	second := createTestSession(t, base)

	var created loopEntry
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+first.ID+"/loops", loopCreateInput{
		Cron: "0 * * * * *", Interval: "10m", Prompt: "report the build status",
	}, &created)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d", resp.StatusCode)
	}
	if !strings.HasPrefix(created.ID, "loop-") || len(created.ID) != len("loop-")+6 || created.Interval != "10m" || !created.Enabled {
		t.Fatalf("created: %+v", created)
	}
	if got := listLoops(t, base, first.ID); len(got) != 1 || got[0].ID != created.ID {
		t.Fatalf("list: %+v", got)
	}
	if got := listLoops(t, base, second.ID); len(got) != 0 {
		t.Fatalf("loops must stay bound to their session: %+v", got)
	}

	// /loop list on another session must not be able to cancel this one.
	foreign := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+second.ID+"/loops/"+created.ID+"/cancel", map[string]any{}, nil)
	foreign.Body.Close()
	if foreign.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-session cancel status %d", foreign.StatusCode)
	}
	cancel := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+first.ID+"/loops/"+created.ID+"/cancel", map[string]any{}, nil)
	cancel.Body.Close()
	if cancel.StatusCode != http.StatusOK {
		t.Fatalf("cancel status %d", cancel.StatusCode)
	}
	if got := listLoops(t, base, first.ID); len(got) != 0 {
		t.Fatalf("after cancel: %+v", got)
	}
}

func TestSessionLoopValidatesAndReadsBackCronInterval(t *testing.T) {
	_, base := newGoalServer(t, &replyQueue{})
	sess := createTestSession(t, base)
	for _, in := range []loopCreateInput{
		{Prompt: "no schedule"},
		{Cron: "0 * * * * *"},
		{Cron: "not a cron", Prompt: "x"},
	} {
		resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/loops", in, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%+v: status %d", in, resp.StatusCode)
		}
	}
	var hourly loopEntry
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/loops", loopCreateInput{
		Cron: "0 0 */2 * * *", Prompt: "check the deploy",
	}, &hourly)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status %d", resp.StatusCode)
	}
	// Without an explicit interval the composer reads the schedule back.
	if hourly.Interval != "2h" {
		t.Fatalf("interval: %q", hourly.Interval)
	}
	if hourly.Cron != "0 0 */2 * * *" {
		t.Fatalf("cron: %q", hourly.Cron)
	}
}

func TestLoopIntervalReadsBackGeneratedSchedules(t *testing.T) {
	for expression, want := range map[string]string{
		"0 */10 * * * *": "10m",
		"0 0 */2 * * *":  "2h",
		"0 0 0 */3 * *":  "3d",
		"0 0 9 * * 1":    "0 0 9 * * 1",
		"0 */99 * * * *": "0 */99 * * * *",
		"@every 10m":     "@every 10m",
	} {
		if got := loopInterval(expression); got != want {
			t.Fatalf("loopInterval(%q) = %q, want %q", expression, got, want)
		}
	}
}

func TestSessionLoopRejectsSubMinuteSchedules(t *testing.T) {
	_, base := newGoalServer(t, &replyQueue{})
	sess := createTestSession(t, base)
	for _, cron := range []string{"* * * * * *", "*/5 * * * * *", "0,30 * * * * *", "0-59 * * * * *"} {
		resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/loops", loopCreateInput{Cron: cron, Prompt: "ping"}, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("%q: status %d", cron, resp.StatusCode)
		}
	}
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/loops", loopCreateInput{Cron: "30 * * * * *", Prompt: "ping"}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("once a minute: status %d", resp.StatusCode)
	}
}

func TestDeletingSessionRemovesItsLoops(t *testing.T) {
	server, _, base := newGoalTestServer(t, &replyQueue{})
	doomed := createTestSession(t, base)
	kept := createTestSession(t, base)
	for _, id := range []string{doomed.ID, kept.ID} {
		resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+id+"/loops", loopCreateInput{Cron: "0 */10 * * * *", Prompt: "ping"}, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create status %d", resp.StatusCode)
		}
	}
	req, _ := http.NewRequest(http.MethodDelete, base+"/v1/sessions/"+doomed.ID, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status %d", resp.StatusCode)
	}
	tasks := server.cron.store.snapshot().Tasks
	if len(tasks) != 1 || tasks[0].SessionID != kept.ID {
		t.Fatalf("only the surviving conversation's loop may remain: %+v", tasks)
	}
}

func TestSessionLoopCancelRetriesCronConflicts(t *testing.T) {
	_, base := newGoalServer(t, &replyQueue{})
	sess := createTestSession(t, base)
	other := createTestSession(t, base)
	create := func(id string) loopEntry {
		var out loopEntry
		resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+id+"/loops", loopCreateInput{Cron: "0 */10 * * * *", Prompt: "ping"}, &out)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create status %d", resp.StatusCode)
		}
		return out
	}
	cancel := func(id string) int {
		resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/loops/"+id+"/cancel", map[string]any{}, nil)
		resp.Body.Close()
		return resp.StatusCode
	}
	// Another writer changes the store between snapshot and apply.
	bumps := 0
	t.Cleanup(func() { loopCancelBeforeApply = nil })
	loopCancelBeforeApply = func() {
		if bumps < 1 {
			bumps++
			create(other.ID)
		}
	}
	first := create(sess.ID)
	if status := cancel(first.ID); status != http.StatusOK || len(listLoops(t, base, sess.ID)) != 0 {
		t.Fatalf("one conflict must be retried: status %d", status)
	}
	loopCancelBeforeApply = func() { create(other.ID) }
	second := create(sess.ID)
	if status := cancel(second.ID); status != http.StatusConflict {
		t.Fatalf("persistent conflicts must not report success: status %d", status)
	}
	if got := listLoops(t, base, sess.ID); len(got) != 1 || got[0].ID != second.ID {
		t.Fatalf("loop must still exist after a failed cancel: %+v", got)
	}
}
