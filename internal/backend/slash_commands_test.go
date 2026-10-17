package backend

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

func writePromptTemplate(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func listSlashCommands(t *testing.T, base, workdir string) []protocol.SlashCommand {
	t.Helper()
	resp, err := http.Get(base + "/v1/commands?workdir=" + url.QueryEscape(workdir))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("commands status %d", resp.StatusCode)
	}
	var out protocol.SlashCommandList
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.Commands
}

func TestSlashCommandCatalogListsDesktopBuiltinsAndTemplates(t *testing.T) {
	_, base := newGoalServer(t, &replyQueue{})
	root, _ := config.Dir()
	writePromptTemplate(t, filepath.Join(root, "prompts"), "deploy", "---\ndescription: Deploy a service\nargument-hint: <env>\n---\nDeploy to $1")
	// Templates cannot shadow desktop built-ins.
	writePromptTemplate(t, filepath.Join(root, "prompts"), "goal", "shadow")
	writePromptTemplate(t, filepath.Join(root, "prompts"), "init", "shadow init")

	var names []string
	byName := map[string]protocol.SlashCommand{}
	for _, command := range listSlashCommands(t, base, "") {
		names = append(names, command.Kind+":"+command.Name)
		byName[command.Name] = command
	}
	want := "action:goal,action:loop,action:plan,action:compact,action:model,action:rewind,prompt:init,prompt:review,prompt:deploy"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("catalog:\n got %s\nwant %s", got, want)
	}
	if deploy := byName["deploy"]; deploy.Source != "user" || deploy.ArgumentHint != "<env>" || deploy.Description != "Deploy a service" {
		t.Fatalf("deploy: %+v", deploy)
	}
	for _, terminalOnly := range []string{"new", "clear", "stop", "help", "status", "resume", "permissions", "mcp", "memory", "agents"} {
		if _, ok := byName[terminalOnly]; ok {
			t.Fatalf("/%s is terminal-only and must not reach the desktop catalog", terminalOnly)
		}
	}
}

func TestPromptCommandRunExpandsForModelAndKeepsTypedText(t *testing.T) {
	client := &replyQueue{replies: []string{"deployed"}}
	store, base := newGoalServer(t, client)
	root, _ := config.Dir()
	writePromptTemplate(t, filepath.Join(root, "prompts"), "deploy", "Deploy the service to $1 and report back.")
	sess := createTestSession(t, base)

	var accepted protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{
		ConversationID: sess.ID, ClientRequestID: "cmd-1", Prompt: "/deploy staging", PromptCommand: true,
	}, &accepted)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("run status %d", resp.StatusCode)
	}
	waitForRunTerminal(t, base, sess.ID, accepted.RunID)
	turns := client.userTurns()
	if len(turns) != 1 || !strings.HasPrefix(turns[0], "/deploy staging") || !strings.Contains(turns[0], "Deploy the service to staging and report back.") {
		t.Fatalf("model must see the typed command and its expansion: %q", turns)
	}
	_, msgs, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range msgs {
		if message.Role == "user" && message.Content != "/deploy staging" {
			t.Fatalf("history must keep the typed command, got %q", message.Content)
		}
	}

	var second protocol.RunAccepted
	builtin := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{
		ConversationID: sess.ID, ClientRequestID: "cmd-2", Prompt: "/init keep it short", PromptCommand: true,
	}, &second)
	if builtin.StatusCode != http.StatusAccepted {
		t.Fatalf("builtin status %d", builtin.StatusCode)
	}
	waitForRunTerminal(t, base, sess.ID, second.RunID)
	if last := client.userTurns()[1]; !strings.Contains(last, "AGENTS.md") || !strings.Contains(last, "keep it short") {
		t.Fatalf("/init expansion: %q", last)
	}

	missing := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{
		ConversationID: sess.ID, ClientRequestID: "cmd-3", Prompt: "/nope", PromptCommand: true,
	}, nil)
	missing.Body.Close()
	if missing.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("unknown command status %d", missing.StatusCode)
	}
}

// waitForRunTerminal blocks until the run's terminal event is in the session log.
func waitForRunTerminal(t *testing.T, base, id, runID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	after := int64(0)
	for time.Now().Before(deadline) {
		// The stream closes after each terminal event, so resume from the last sequence.
		resp, err := http.Get(base + "/v1/sessions/" + id + "/events?after_seq=" + strconv.FormatInt(after, 10))
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range readSSE(t, resp) {
			after = event.Seq
			if event.RunID == runID && (event.Type == protocol.EventRunCompleted || event.Type == protocol.EventRunFailed || event.Type == protocol.EventRunCancelled) {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("run %s did not finish", runID)
}

func TestPromptCommandArgumentsToleratePlainApostrophes(t *testing.T) {
	isolateUserDir(t)
	s := &Server{}
	out, err := s.expandPromptCommand("/review main don't touch tests", "")
	if err != nil || !strings.Contains(out, "against main") || !strings.Contains(out, "don't touch tests") {
		t.Fatalf("expansion: %v\n%s", err, out)
	}
	out, err = s.expandPromptCommand(`/review main "keep it short"`, "")
	if err != nil || !strings.Contains(out, "\n\nkeep it short\n") {
		t.Fatalf("balanced quotes still group arguments: %v\n%s", err, out)
	}
}

func TestPromptCommandRetryReplaysAfterTemplateRemoval(t *testing.T) {
	client := &replyQueue{replies: []string{"done"}}
	_, base := newGoalServer(t, client)
	root, _ := config.Dir()
	dir := filepath.Join(root, "prompts")
	writePromptTemplate(t, dir, "deploy", "Deploy to $1.")
	sess := createTestSession(t, base)
	in := protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "cmd-r", Prompt: "/deploy staging", PromptCommand: true}
	var first protocol.RunAccepted
	postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/runs", in, &first)
	waitForRunTerminal(t, base, sess.ID, first.RunID)
	if err := os.Remove(filepath.Join(dir, "deploy.md")); err != nil {
		t.Fatal(err)
	}
	var retry protocol.RunAccepted
	resp := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sess.ID+"/runs", in, &retry)
	if resp.StatusCode != http.StatusOK || retry.RunID != first.RunID {
		t.Fatalf("retry must replay the accepted run: %d %+v", resp.StatusCode, retry)
	}
}
