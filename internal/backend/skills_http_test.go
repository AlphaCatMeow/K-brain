package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/datapath"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestSkillsHTTPFixture(t *testing.T) {
	t.Setenv(datapath.LiveAgentHomeEnv, t.TempDir())
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	client := &scriptedClient{response: "ok"}
	s, err := New(Options{Store: store, Factory: func(_ context.Context, _ string, m protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(client, m.Model, 100, "system"), nil
	}, EventDir: t.TempDir(), Token: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	r := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/skills", nil)
	req.Header.Set("Authorization", "Bearer secret")
	s.ServeHTTP(r, req)
	if r.Code != 200 {
		t.Fatalf("list status %d: %s", r.Code, r.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["rootDir"] == nil {
		t.Fatal("missing root")
	}
	payload := `{"action":"create","name":"http-fixture","description":"fixture","body":"workflow"}`
	r = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/skills/manage", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	s.ServeHTTP(r, req)
	if r.Code != 200 {
		t.Fatalf("create status %d: %s", r.Code, r.Body.String())
	}
	r = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v1/skills/file?path=http-fixture/SKILL.md", nil)
	req.Header.Set("Authorization", "Bearer secret")
	s.ServeHTTP(r, req)
	if r.Code != 200 {
		t.Fatalf("read status %d: %s", r.Code, r.Body.String())
	}
}
