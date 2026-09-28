package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func doJSON(t *testing.T, method, url string, input, output any) int {
	t.Helper()
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		body = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if output != nil && resp.ContentLength != 0 {
		if err := json.NewDecoder(resp.Body).Decode(output); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func TestHistoryMutationContract(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := &scriptedClient{response: "continued"}
	_, server := newTestServer(t, store, t.TempDir(), fixture)
	created := createTestSession(t, server.URL)
	if err := store.Save(created.ID, 0, []ai.Message{{Role: "user", Content: "first"}, {Role: "assistant", Content: "answer"}, {Role: "user", Content: "pending"}}, "fixture-model", "fixture"); err != nil {
		t.Fatal(err)
	}
	got := getSession(t, server.URL, created.ID)
	if got.Revision == "" || len(got.Messages) != 3 || got.Messages[2].ID == "" {
		t.Fatalf("canonical session = %+v", got)
	}
	var page historyResponse
	if code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions/"+created.ID+"/history?max_messages=2&before_offset=3", nil, &page); code != http.StatusOK || len(page.Session.Messages) != 2 || page.OldestOffset != 1 {
		t.Fatalf("history page code=%d response=%+v", code, page)
	}
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/"+created.ID+"/edit", protocol.EditSessionRequest{ExpectedRevision: got.Revision, MessageRef: protocol.HistoryMessageRef{MessageID: got.Messages[2].ID, Role: protocol.RoleUser}, Replacement: protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "edited"}}}}, &got); code != http.StatusOK {
		t.Fatalf("edit status=%d", code)
	}
	if got.Messages[2].Content[0].Text != "edited" {
		t.Fatalf("edited history=%+v", got.Messages)
	}
	stale := protocol.EditSessionRequest{ExpectedRevision: "stale", MessageRef: protocol.HistoryMessageRef{MessageID: got.Messages[2].ID}, Replacement: protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "bad"}}}}
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/"+created.ID+"/edit", stale, nil); code != http.StatusConflict {
		t.Fatalf("stale edit status=%d", code)
	}
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/"+created.ID+"/runs", protocol.PromptRequest{ConversationID: created.ID, ClientRequestID: "resume", ResumeMessageID: got.Messages[2].ID, Prompt: "edited"}, nil); code != http.StatusAccepted {
		t.Fatalf("resume status=%d", code)
	}
	_ = waitForRun(t, server.URL, created.ID, 0)
	after := getSession(t, server.URL, created.ID)
	if after.MessageCount != 4 {
		t.Fatalf("resume duplicated user: %+v", after.Messages)
	}
	var share protocol.ShareStatus
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/"+created.ID+"/share", protocol.ShareUpdateRequest{Enabled: true}, &share); code != http.StatusOK || !share.Enabled || share.Token == "" {
		t.Fatalf("share=%d %+v", code, share)
	}
	var public struct {
		Messages []protocol.Message `json:"messages"`
	}
	if code := doJSON(t, http.MethodGet, server.URL+"/v1/shares/"+share.Token, nil, &public); code != http.StatusOK || len(public.Messages) == 0 {
		t.Fatalf("public share=%d %+v", code, public)
	}
	revokedToken := share.Token
	if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/"+created.ID+"/share", protocol.ShareUpdateRequest{Enabled: false}, &share); code != http.StatusOK {
		t.Fatalf("revoke=%d %+v", code, share)
	}
	if code := doJSON(t, http.MethodGet, server.URL+"/v1/shares/"+revokedToken, nil, nil); code != http.StatusNotFound {
		t.Fatalf("revoked share status=%d", code)
	}
	if code := doJSON(t, http.MethodDelete, server.URL+"/v1/sessions/"+created.ID, nil, nil); code != http.StatusOK {
		t.Fatalf("delete=%d", code)
	}
	if code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions/"+created.ID, nil, nil); code != http.StatusNotFound {
		t.Fatalf("deleted get=%d", code)
	}
}

func TestHistoryMessageOffsetsSparseSystemRows(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id, err := store.Create("", "fixture-model", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]ai.Message, 12)
	raw[0] = ai.Message{Role: "system", Content: "saved system"}
	raw[3] = ai.Message{Role: "user", Content: "first"}
	raw[7] = ai.Message{Role: "assistant", Content: "answer"}
	raw[11] = ai.Message{Role: "user", Content: "pending"}
	if err := store.Save(id, 0, raw, "fixture-model", "fixture"); err != nil {
		t.Fatal(err)
	}
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "reply"})
	full := getSession(t, server.URL, id)
	cases := []struct {
		name, query string
		offsets     []int
		roles       []string
		more        bool
	}{
		{"latest", "?max_messages=2", []int{7, 11}, []string{"assistant", "user"}, true},
		{"older with system", "?max_messages=2&before_offset=7", []int{0, 3}, []string{"system", "user"}, false},
		{"boundary in hole", "?max_messages=2&before_offset=6", []int{0, 3}, []string{"system", "user"}, false},
		{"system only", "?max_messages=2&before_offset=3", []int{0}, []string{"system"}, false},
		{"empty", "?max_messages=2&before_offset=0", []int{}, []string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var page historyResponse
			code := doJSON(t, http.MethodGet, server.URL+"/v1/sessions/"+id+"/history"+tc.query+"&expected_revision="+full.Revision, nil, &page)
			if code != http.StatusOK {
				t.Fatalf("history status=%d", code)
			}
			if !reflect.DeepEqual(page.MessageOffsets, tc.offsets) || len(page.MessageOffsets) != len(page.Session.Messages) {
				t.Fatalf("offsets=%v messages=%+v want=%v", page.MessageOffsets, page.Session.Messages, tc.offsets)
			}
			if page.Revision != full.Revision || page.TotalMessageCount != 4 || page.HasMoreBefore != tc.more {
				t.Fatalf("page metadata=%+v", page)
			}
			for i, offset := range page.MessageOffsets {
				if page.Session.Messages[i].Role != tc.roles[i] || page.Session.Messages[i].Content[0].Text != raw[offset].Content {
					t.Fatalf("offset %d maps to wrong message: %+v", offset, page.Session.Messages[i])
				}
			}
			if len(tc.offsets) > 0 && page.OldestOffset != tc.offsets[0] {
				t.Fatalf("oldest=%d want=%d", page.OldestOffset, tc.offsets[0])
			}
		})
	}
}

type editResumeClient struct {
	scriptedClient
	requests chan []ai.Message
}

func (c *editResumeClient) Stream(ctx context.Context, request ai.Request, onText, onThink func(string), onTool func(string, string, string)) (ai.Message, ai.Usage, error) {
	c.requests <- append([]ai.Message(nil), request.Messages...)
	return c.scriptedClient.Stream(ctx, request, onText, onThink, onTool)
}

func TestEditResumeWithFactorySystemPrompt(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart=%v", restart), func(t *testing.T) {
			dir, events := t.TempDir(), t.TempDir()
			store, err := session.Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = store.Close() }()
			fixture := &editResumeClient{scriptedClient: scriptedClient{response: "resumed answer"}, requests: make(chan []ai.Message, 2)}
			const systemPrompt = "You are the actual factory system prompt. Preserve user history."
			factory := func(_ context.Context, _ string, selected protocol.ModelRef) (*agent.Agent, error) {
				return agent.New(fixture, selected.Model, 1024, systemPrompt), nil
			}
			start := func() (*Server, *httptest.Server) {
				backend, err := New(Options{Store: store, Factory: factory, EventDir: events})
				if err != nil {
					t.Fatal(err)
				}
				server := httptest.NewServer(backend)
				t.Cleanup(func() { server.Close(); _ = backend.Close() })
				return backend, server
			}
			backend, server := start()
			var created protocol.Session
			input := protocol.CreateSessionRequest{Model: protocol.ModelRef{Model: "fixture-model", Provider: "fixture"}, Messages: []protocol.Message{
				{Role: protocol.RoleSystem, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "old stored system"}}},
				{Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "old question"}}},
				{Role: protocol.RoleAssistant, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "discarded answer"}}},
				{Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "discarded suffix"}}},
			}}
			if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions", input, &created); code != http.StatusCreated {
				t.Fatalf("create status=%d", code)
			}
			var edited protocol.Session
			edit := protocol.EditSessionRequest{MessageRef: protocol.HistoryMessageRef{MessageID: created.Messages[1].ID}, ExpectedRevision: created.Revision, Replacement: protocol.Message{Role: protocol.RoleUser, Content: []protocol.ContentBlock{{Type: protocol.ContentText, Text: "replacement question"}}}}
			if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/"+created.ID+"/edit", edit, &edited); code != http.StatusOK {
				t.Fatalf("edit status=%d", code)
			}
			if len(edited.Messages) != 2 || edited.Messages[1].ID == "" {
				t.Fatalf("edited=%+v", edited)
			}
			tailID := edited.Messages[1].ID
			if restart {
				server.Close()
				if err := backend.Close(); err != nil {
					t.Fatal(err)
				}
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				store, err = session.Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				backend, server = start()
			}
			loaded := getSession(t, server.URL, created.ID)
			if loaded.Revision != edited.Revision || loaded.Messages[1].ID != tailID {
				t.Fatalf("edit identity changed: edited=%+v loaded=%+v", edited, loaded)
			}
			rt, err := backend.loadRuntimeByID(created.ID)
			if err != nil {
				t.Fatal(err)
			}
			restored := rt.agent.MessagesSnapshot()
			if len(restored) != 2 || restored[0].Content != systemPrompt || restored[1].ID != tailID {
				t.Fatalf("restored runtime=%+v", restored)
			}
			prompt := protocol.PromptRequest{ClientRequestID: "resume-edited", ResumeMessageID: loaded.Messages[1].ID, Prompt: loaded.Messages[1].Content[0].Text}
			wrong := prompt
			wrong.ClientRequestID = "wrong-content"
			wrong.Content = []protocol.ContentBlock{{Type: protocol.ContentText, Text: "not the replacement"}}
			if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/"+created.ID+"/runs", wrong, nil); code != http.StatusConflict {
				t.Fatalf("mismatched content status=%d", code)
			}
			if code := doJSON(t, http.MethodPost, server.URL+"/v1/sessions/"+created.ID+"/runs", prompt, nil); code != http.StatusAccepted {
				t.Fatalf("matching resume status=%d", code)
			}
			runEvents := waitForRun(t, server.URL, created.ID, loaded.LastSeq)
			if len(runEvents) == 0 || runEvents[len(runEvents)-1].Type != protocol.EventRunCompleted {
				t.Fatalf("run events=%+v", runEvents)
			}
			select {
			case messages := <-fixture.requests:
				if len(messages) != 2 || messages[0].Role != "system" || messages[0].Content != systemPrompt || messages[1].Role != "user" || messages[1].ID != tailID || messages[1].Content != "replacement question" {
					t.Fatalf("model input duplicated or stale: %+v", messages)
				}
			default:
				t.Fatal("model was not called")
			}
			after := getSession(t, server.URL, created.ID)
			if len(after.Messages) != 3 || after.Messages[1].ID != tailID || after.Messages[2].Role != "assistant" {
				t.Fatalf("persisted history after resume=%+v", after.Messages)
			}
		})
	}
}
