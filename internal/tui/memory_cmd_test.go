package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/memory"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func memoryTurnRequest(t *testing.T, m *model, input string) string {
	t.Helper()
	var request ai.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	m.agent.Client = ai.New(server.URL, "fixture")
	if _, err := m.agent.TurnAuthored(t.Context(), input, agent.Events{}); err != nil {
		t.Fatal(err)
	}
	return request.Messages[len(request.Messages)-1].Content
}

func TestMemoryEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)

	m := compactCmdModel()

	callTool := func(name, args string) string {
		t.Helper()
		for _, tool := range m.agent.Tools {
			if tool.Def.Function.Name == name {
				out, err := tool.Run(t.Context(), []byte(args))
				if err != nil {
					return "Error: " + err.Error()
				}
				return out
			}
		}
		t.Fatal(name + " not registered")
		return ""
	}
	if out := callTool("remember", `{"text":"user prefers pnpm over npm"}`); strings.HasPrefix(out, "Error:") {
		t.Fatal(out)
	}

	data, err := os.ReadFile(filepath.Join(home, "memory.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "- [ ] user prefers pnpm over npm\n" {
		t.Fatalf("memory.md:\n%s", data)
	}

	m.prepareTurn("hello")
	context := memoryTurnRequest(t, m, "hello")
	if !strings.Contains(context, "user prefers pnpm over npm") || !strings.Contains(context, "<memory>") {
		t.Fatalf("memory not injected into request context:\n%s", context)
	}

	m.memoryCommand(nil)
	var listed bool
	for _, b := range m.blocks {
		r := ansi.Strip(b.render(m.width))
		if strings.Contains(r, "installation") && strings.Contains(r, "user prefers pnpm") {
			listed = true
		}
	}
	if !listed {
		t.Fatal("/memory should list the installation entry")
	}

	m.memoryCommand([]string{"1"})
	data, _ = os.ReadFile(filepath.Join(home, "memory.md"))
	if !strings.Contains(string(data), "- [x] user prefers pnpm") {
		t.Fatalf("entry should be struck, not deleted:\n%s", data)
	}
	m.prepareTurn("hello again")
	if context := memoryTurnRequest(t, m, "hello again"); !strings.Contains(context, "Earlier saved-memory snapshots no longer apply") {
		t.Fatal("forgotten memory was not explicitly cleared in the request")
	}
	if strings.Contains(m.agent.Messages[0].Content, "pnpm") {
		t.Fatal("struck entries must stop being injected")
	}
}

func TestSessionMemoryScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("K_BRAIN_HOME", home)

	m := compactCmdModel()
	store, err := session.OpenProjectHome(home)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id, err := store.Create(t.TempDir(), "model", "provider")
	if err != nil {
		t.Fatal(err)
	}
	m.agent.SetSessionID(id)
	if err := memory.Session(id).Remember("this repo uses ./scripts/ship.sh to deploy"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(store.TranscriptPath(id)), "memory.md")); err != nil {
		t.Fatal("session memory should live under sessions/")
	}
	m.sessionID = id
	m.prepareTurn("hi")
	if !strings.Contains(memoryTurnRequest(t, m, "hi"), "ship.sh") {
		t.Fatal("session memory should inject while the session is active")
	}
}
