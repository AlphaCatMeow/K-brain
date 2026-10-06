package backend

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestHistoryReadsWithoutAnyConfiguredModel(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id, err := store.Create(t.TempDir(), "removed", "removed")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Save(id, 0, []ai.Message{{Role: "user", Content: "retained history"}}, "removed", "removed"); err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Store: store, EventDir: t.TempDir(), Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
		t.Error("history attempted model initialization")
		return nil, fmt.Errorf("no models")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	host := httptest.NewServer(s)
	defer host.Close()
	view := getSession(t, host.URL, id)
	if len(view.Messages) != 1 {
		t.Fatalf("history=%+v", view)
	}
	var history historyResponse
	if status := doJSON(t, http.MethodGet, host.URL+"/v1/sessions/"+id+"/history?include_active=true", nil, &history); status != 200 || len(history.Session.Messages) != 1 || history.ActiveMessages == nil {
		t.Fatalf("history status=%d body=%+v", status, history)
	}
	if len(s.sessions) != 0 {
		t.Fatal("read-only history installed incomplete runtime")
	}
}

func TestExplicitReasoningOffReachesProvider(t *testing.T) {
	a := agent.New(nil, "m", 100, "system")
	a.Effort = "high"
	restore := applyRunOptions(a, protocol.RunOptions{Reasoning: "off"})
	if a.Effort != "off" {
		t.Fatalf("off was lost: %q", a.Effort)
	}
	restore()
	if a.Effort != "high" {
		t.Fatal("effort did not restore")
	}
}
