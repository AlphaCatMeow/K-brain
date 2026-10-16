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
