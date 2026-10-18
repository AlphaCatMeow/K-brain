package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// pausingReadClient asks for one Read of path, but only after release is
// closed, so a test can act on the run while it is provably in flight.
type pausingReadClient struct {
	scriptedClient
	lock    sync.Mutex
	calls   int
	path    string
	entered chan struct{}
	release chan struct{}
}

func (c *pausingReadClient) Stream(ctx context.Context, _ ai.Request, text, _ func(string), _ func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.lock.Lock()
	c.calls++
	first := c.calls == 1
	c.lock.Unlock()
	if first {
		close(c.entered)
		select {
		case <-c.release:
		case <-ctx.Done():
			return ai.Message{}, ai.Usage{}, ctx.Err()
		}
		args, _ := json.Marshal(map[string]string{"path": c.path})
		call := ai.ToolCall{ID: "roots-call", Type: "function"}
		call.Function.Name, call.Function.Arguments = "Read", string(args)
		return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}}, ai.Usage{}, nil
	}
	if text != nil {
		text("done")
	}
	return ai.Message{Role: "assistant", Content: "done", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
}

var _ ai.Client = (*pausingReadClient)(nil)

func readToolResult(t *testing.T, events []protocol.Event) protocol.ToolResult {
	t.Helper()
	for _, event := range events {
		if event.Type != protocol.EventToolResult {
			continue
		}
		var result protocol.ToolResultEvent
		if err := json.Unmarshal(event.Payload, &result); err != nil {
			t.Fatal(err)
		}
		if result.ToolResult.Name == "Read" {
			return result.ToolResult
		}
	}
	t.Fatalf("missing Read result in %+v", events)
	return protocol.ToolResult{}
}

func TestRunRootsHTTPExtendMidRunAndDenialCodes(t *testing.T) {
	for _, extend := range []bool{false, true} {
		name := "denied-with-code"
		if extend {
			name = "extended-mid-run"
		}
		t.Run(name, func(t *testing.T) {
			external := t.TempDir()
			target := filepath.Join(external, "notes.txt")
			if err := os.WriteFile(target, []byte("granted mid-run"), 0600); err != nil {
				t.Fatal(err)
			}
			client := &pausingReadClient{path: target, entered: make(chan struct{}), release: make(chan struct{})}
			store, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			_, server := newToolHistoryServer(t, store, t.TempDir(), client)
			sess := createTestSession(t, server.URL)
			options := protocol.RunOptions{Mode: "agent", ApprovalPolicy: "auto", WorkspaceRoots: []protocol.WorkspaceRoot{{Path: sess.CWD, Access: "write"}}, Tools: &protocol.ToolSelection{Policies: map[string]string{"Read": "allow"}}}
			var accepted protocol.RunAccepted
			resp := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: name, Prompt: "read it", Options: &options}, &accepted)
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("accept status=%d", resp.StatusCode)
			}
			<-client.entered

			rootsURL := server.URL + "/v1/sessions/" + sess.ID + "/runs/" + accepted.RunID + "/roots"
			if extend {
				var granted runRootsResponse
				resp = postJSON(t, http.DefaultClient, rootsURL, extendRunRootsRequest{Roots: []protocol.WorkspaceRoot{{Path: external, Access: "read"}}}, &granted)
				if resp.StatusCode != http.StatusOK || len(granted.Roots) != 2 || granted.Roots[1].Path != filepath.Clean(external) {
					t.Fatalf("extend status=%d roots=%+v", resp.StatusCode, granted.Roots)
				}
			}
			resp = postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs/other-run/roots", extendRunRootsRequest{Roots: []protocol.WorkspaceRoot{{Path: external, Access: "read"}}}, nil)
			if resp.StatusCode != http.StatusConflict {
				t.Fatalf("extend of inactive run status=%d", resp.StatusCode)
			}
			close(client.release)

			result := readToolResult(t, readToolHistoryRun(t, server.URL, sess.ID, accepted.RunID, "reject"))
			if extend {
				if result.Failed || result.Error != nil {
					t.Fatalf("extended read failed: %+v", result)
				}
				return
			}
			if !result.Failed || result.Error == nil || result.Error.Code != "path_outside_roots" || result.Error.Path != target || result.Error.Access != "read" {
				t.Fatalf("denial=%+v", result)
			}
			if len(result.Error.Roots) == 0 || result.Error.Roots[0].Path != sess.CWD {
				t.Fatalf("denial roots=%+v", result.Error.Roots)
			}
		})
	}
}

func TestRunRootsHTTPRejectsInvalidGrants(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newToolHistoryServer(t, store, t.TempDir(), &pausingReadClient{})
	sess := createTestSession(t, server.URL)
	url := server.URL + "/v1/sessions/" + sess.ID + "/runs/any/roots"
	for _, body := range []extendRunRootsRequest{
		{},
		{Roots: []protocol.WorkspaceRoot{{Path: "relative", Access: "read"}}},
		{Roots: []protocol.WorkspaceRoot{{Path: t.TempDir(), Access: "admin"}}},
		{Roots: []protocol.WorkspaceRoot{{Path: filepath.Join(t.TempDir(), "missing"), Access: "read"}}},
	} {
		if resp := postJSON(t, http.DefaultClient, url, body, nil); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("body=%+v status=%d", body, resp.StatusCode)
		}
	}
}
