package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

// readRunAnsweringRootRequests streams a run's events until it ends, answering
// every root.requested with answer(request) and recording what it saw.
func readRunAnsweringRootRequests(t *testing.T, base, sessionID string, answer func(protocol.RootRequest) protocol.RootResolution) ([]protocol.Event, []protocol.RootRequest) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/sessions/"+sessionID+"/events?after_seq=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var events []protocol.Event
	var requests []protocol.RootRequest
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event protocol.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
		switch event.Type {
		case protocol.EventRootRequested:
			var request protocol.RootRequest
			if err := json.Unmarshal(event.Payload, &request); err != nil {
				t.Fatal(err)
			}
			requests = append(requests, request)
			if r := postJSON(t, http.DefaultClient, base+"/v1/sessions/"+sessionID+"/root-requests/"+request.RequestID, answer(request), nil); r.StatusCode != http.StatusOK {
				t.Fatalf("resolve status=%d", r.StatusCode)
			}
		case protocol.EventRunCompleted, protocol.EventRunFailed, protocol.EventRunCancelled:
			return events, requests
		}
	}
	t.Fatalf("event stream ended early: %v", scanner.Err())
	return nil, nil
}

func TestRootRequestsPauseDeniedCallUntilClientAnswers(t *testing.T) {
	for _, grant := range []bool{true, false} {
		name := "deny"
		if grant {
			name = "grant"
		}
		t.Run(name, func(t *testing.T) {
			external := t.TempDir()
			target := filepath.Join(external, "notes.txt")
			if err := os.WriteFile(target, []byte("granted while waiting"), 0600); err != nil {
				t.Fatal(err)
			}
			client := &pausingReadClient{path: target, entered: make(chan struct{}), release: make(chan struct{})}
			close(client.release)
			store, err := session.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			_, server := newToolHistoryServer(t, store, t.TempDir(), client)
			sess := createTestSession(t, server.URL)
			options := protocol.RunOptions{Mode: "agent", ApprovalPolicy: "auto", RootRequests: true, WorkspaceRoots: []protocol.WorkspaceRoot{{Path: sess.CWD, Access: "write"}}, Tools: &protocol.ToolSelection{Policies: map[string]string{"Read": "allow"}}}
			var accepted protocol.RunAccepted
			if resp := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: name, Prompt: "read it", Options: &options}, &accepted); resp.StatusCode != http.StatusAccepted {
				t.Fatalf("accept status=%d", resp.StatusCode)
			}
			events, requests := readRunAnsweringRootRequests(t, server.URL, sess.ID, func(request protocol.RootRequest) protocol.RootResolution {
				if !grant {
					return protocol.RootResolution{Decision: "deny"}
				}
				return protocol.RootResolution{Decision: "grant", Roots: []protocol.WorkspaceRoot{{Path: external, Access: "read"}}}
			})
			if len(requests) != 1 || requests[0].Path != target || requests[0].Code != "path_outside_roots" || requests[0].Tool != "Read" || requests[0].ToolCallID != "roots-call" {
				t.Fatalf("requests=%+v", requests)
			}
			if !hasToolHistoryEvent(events, protocol.EventRootResolved) {
				t.Fatal("missing root.resolved")
			}
			result := readToolResult(t, events)
			if grant {
				if result.Failed || !strings.Contains(result.Output, "granted while waiting") {
					t.Fatalf("granted read=%+v", result)
				}
				return
			}
			if !result.Failed || result.Error == nil || result.Error.Code != "path_outside_roots" || !strings.Contains(result.Output, "the user denied access to this path") {
				t.Fatalf("denied read=%+v", result)
			}
		})
	}
}

func TestRootRequestsAreOptIn(t *testing.T) {
	external := t.TempDir()
	target := filepath.Join(external, "notes.txt")
	if err := os.WriteFile(target, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &pausingReadClient{path: target, entered: make(chan struct{}), release: make(chan struct{})}
	close(client.release)
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newToolHistoryServer(t, store, t.TempDir(), client)
	sess := createTestSession(t, server.URL)
	options := protocol.RunOptions{Mode: "agent", ApprovalPolicy: "auto", WorkspaceRoots: []protocol.WorkspaceRoot{{Path: sess.CWD, Access: "write"}}, Tools: &protocol.ToolSelection{Policies: map[string]string{"Read": "allow"}}}
	var accepted protocol.RunAccepted
	postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs", protocol.PromptRequest{ConversationID: sess.ID, ClientRequestID: "opt-in", Prompt: "read it", Options: &options}, &accepted)
	events, requests := readRunAnsweringRootRequests(t, server.URL, sess.ID, func(protocol.RootRequest) protocol.RootResolution {
		return protocol.RootResolution{Decision: "deny"}
	})
	if len(requests) != 0 || !readToolResult(t, events).Failed {
		t.Fatalf("requests=%+v", requests)
	}
}
