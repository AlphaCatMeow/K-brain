package backend

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

const questionArgs = `{"questions":[{"prompt":"Which strategy?","options":[{"label":"Slow"},{"label":"Fast","recommended":true}]},{"id":"custom","header":"Detail","prompt":"Which detail?","options":[{"label":"Default"},{"label":"Alternate"}]}]}`

type questionClient struct{ scriptedClient }

func (c *questionClient) Clone() ai.Client { return c }
func (c *questionClient) Stream(_ context.Context, request ai.Request, text, think func(string), tool func(string, string, string)) (ai.Message, ai.Usage, error) {
	for _, message := range request.Messages {
		if message.Role == "tool" && message.Name == "AskUserQuestion" {
			if !strings.Contains(message.Content, "ask_user_question") {
				return ai.Message{}, ai.Usage{}, fmt.Errorf("missing structured answer: %s", message.Content)
			}
			text("Answer received")
			return ai.Message{Role: "assistant", Content: "Answer received", StopReason: ai.StopReasonStop}, ai.Usage{}, nil
		}
	}
	call := ai.ToolCall{ID: "provider-call", Type: "function"}
	call.Function.Name, call.Function.Arguments = "AskUserQuestion", questionArgs
	return ai.Message{Role: "assistant", ToolCalls: []ai.ToolCall{call}, StopReason: ai.StopReasonToolUse}, ai.Usage{}, nil
}

func questionFixture(t *testing.T, timeout time.Duration) (*Server, *httptest.Server, *session.Store, string) {
	t.Helper()
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	backend, err := New(Options{Store: store, EventDir: dir, MemoryRoot: t.TempDir(), QuestionTimeout: timeout, Factory: func(_ context.Context, _ string, m protocol.ModelRef) (*agent.Agent, error) {
		return agent.New(&questionClient{}, m.Model, 1024, "system"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(backend)
	t.Cleanup(func() { backend.Close(); server.Close(); store.Close() })
	return backend, server, store, dir
}

func nextQuestion(t *testing.T, stream *bufio.Scanner) protocol.QuestionRequest {
	t.Helper()
	for stream.Scan() {
		line := stream.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event protocol.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == protocol.EventQuestionRequested {
			var q protocol.QuestionRequest
			json.Unmarshal(event.Payload, &q)
			return q
		}
	}
	t.Fatal("stream ended without question")
	return protocol.QuestionRequest{}
}

func TestQuestionProviderSSEAnswerReplayAndIdempotency(t *testing.T) {
	_, server, store, dir := questionFixture(t, time.Second*5)
	sess := createTestSession(t, server.URL)
	run := runRequest(t, server.URL, sess.ID, "question-run", "ask me")
	response, err := http.Get(server.URL + "/v1/sessions/" + sess.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	q := nextQuestion(t, scanner)
	replay, err := http.Get(server.URL + "/v1/sessions/" + sess.ID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	replayed := nextQuestion(t, bufio.NewScanner(replay.Body))
	replay.Body.Close()
	if replayed.QuestionID != q.QuestionID || replayed.DeadlineAt != q.DeadlineAt {
		t.Fatalf("replayed question changed identity or deadline: %+v", replayed)
	}
	if q.RunID != run.RunID || q.ToolCallID != "provider-call" || q.Questions[0].Options[0].Label != "Fast" {
		t.Fatalf("request: %+v", q)
	}
	answers := protocol.QuestionAnswerRequest{ConversationID: sess.ID, RunID: run.RunID, QuestionID: q.QuestionID, Answers: []protocol.QuestionAnswer{{QuestionID: "custom", SelectedLabel: "my own words", Custom: true}, {QuestionID: "q1", SelectedLabel: "Fast"}}}
	path := server.URL + "/v1/sessions/" + sess.ID + "/questions/" + q.QuestionID
	for i := 0; i < 2; i++ {
		r := postJSON(t, http.DefaultClient, path, answers, nil)
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatalf("answer %d status %d", i, r.StatusCode)
		}
	}
	var terminal, result, final bool
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var e protocol.Event
		json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e)
		if e.Type == protocol.EventRunCompleted {
			terminal = true
		}
		if e.Type == protocol.EventTextDelta && strings.Contains(string(e.Payload), "Answer received") {
			final = true
		}
		if e.Type == protocol.EventToolResult && strings.Contains(string(e.Payload), "my own words") {
			result = true
		}
	}
	if !terminal || !result || !final {
		t.Fatalf("terminal=%v tool result=%v final=%v", terminal, result, final)
	}
	answers.Answers[1].SelectedLabel = "Slow"
	r := postJSON(t, http.DefaultClient, path, answers, nil)
	r.Body.Close()
	if r.StatusCode != 409 {
		t.Fatalf("conflicting duplicate status=%d", r.StatusCode)
	}
	recovered, httpRecovered := newTestServer(t, store, dir, &scriptedClient{response: "unused"})
	defer recovered.Close()
	answers.Answers[1].SelectedLabel = "Fast"
	r = postJSON(t, http.DefaultClient, httpRecovered.URL+"/v1/sessions/"+sess.ID+"/questions/"+q.QuestionID, answers, nil)
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("recovered duplicate status=%d", r.StatusCode)
	}
}

func TestQuestionTimeoutCancellationAndIdentity(t *testing.T) {
	for _, mode := range []string{"timeout", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			_, server, _, _ := questionFixture(t, 100*time.Millisecond)
			sess := createTestSession(t, server.URL)
			other := createTestSession(t, server.URL)
			run := runRequest(t, server.URL, sess.ID, "run", "ask")
			response, err := http.Get(server.URL + "/v1/sessions/" + sess.ID + "/events")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			scanner := bufio.NewScanner(response.Body)
			q := nextQuestion(t, scanner)
			answer := protocol.QuestionAnswerRequest{ConversationID: sess.ID, RunID: run.RunID, QuestionID: q.QuestionID, Answers: questionDefaults(q.Questions)}
			path := server.URL + "/v1/sessions/" + sess.ID + "/questions/" + q.QuestionID
			wrong := answer
			wrong.ConversationID = other.ID
			r := postJSON(t, http.DefaultClient, path, wrong, nil)
			r.Body.Close()
			if r.StatusCode != 409 {
				t.Fatal(r.StatusCode)
			}
			wrong = answer
			wrong.RunID = "wrong"
			r = postJSON(t, http.DefaultClient, path, wrong, nil)
			r.Body.Close()
			if r.StatusCode != 404 {
				t.Fatal(r.StatusCode)
			}
			if mode == "cancel" {
				r = postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+sess.ID+"/runs/"+run.RunID+"/cancel", map[string]any{}, nil)
				r.Body.Close()
			}
			var resolution protocol.QuestionResolution
			for scanner.Scan() {
				line := scanner.Text()
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var e protocol.Event
				json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e)
				if e.Type == protocol.EventQuestionResolved {
					json.Unmarshal(e.Payload, &resolution)
				}
			}
			if mode == "timeout" && (!resolution.TimedOut || resolution.Answers[0].SelectedLabel != "Fast") {
				t.Fatalf("timeout: %+v", resolution)
			}
			if mode == "cancel" && (!resolution.Cancelled || len(resolution.Answers) != 0) {
				t.Fatalf("cancel: %+v", resolution)
			}
			r = postJSON(t, http.DefaultClient, path, answer, nil)
			r.Body.Close()
			if r.StatusCode != 409 {
				t.Fatalf("stale answer: %d", r.StatusCode)
			}
		})
	}
}

func TestQuestionValidation(t *testing.T) {
	var input struct{ Questions []protocol.Question }
	json.Unmarshal([]byte(questionArgs), &input)
	q, err := normalizeQuestions(input.Questions)
	if err != nil {
		t.Fatal(err)
	}
	if q[0].ID != "q1" || q[0].Options[0].Label != "Fast" {
		t.Fatal(q)
	}
	if defaultQuestionTimeout != 3*time.Minute {
		t.Fatal(defaultQuestionTimeout)
	}
	for _, count := range []int{0, 5} {
		if _, err := normalizeQuestions(make([]protocol.Question, count)); err == nil {
			t.Fatal("accepted count", count)
		}
	}
	for _, count := range []int{1, 7} {
		bad := input.Questions[0]
		bad.Options = make([]protocol.QuestionOption, count)
		if _, err := normalizeQuestions([]protocol.Question{bad}); err == nil {
			t.Fatal("accepted options", count)
		}
	}
	bad := input.Questions[0]
	bad.Options = []protocol.QuestionOption{{Label: "a", Recommended: true}, {Label: "b", Recommended: true}}
	if _, err := normalizeQuestions([]protocol.Question{bad}); err == nil {
		t.Fatal("accepted duplicate recommendation")
	}
	if _, err := validateQuestionAnswers(q, []protocol.QuestionAnswer{{QuestionID: "q1", SelectedLabel: "fake"}, {QuestionID: "custom", SelectedLabel: "Default"}}); err == nil {
		t.Fatal("accepted unknown label")
	}
}

func TestQuestionConcurrentSessionsRemainIsolated(t *testing.T) {
	_, server, _, _ := questionFixture(t, 5*time.Second)
	type pending struct {
		id       string
		question protocol.QuestionRequest
		scanner  *bufio.Scanner
	}
	var requests []pending
	for i := 0; i < 2; i++ {
		sess := createTestSession(t, server.URL)
		runRequest(t, server.URL, sess.ID, "run", "ask")
		response, err := http.Get(server.URL + "/v1/sessions/" + sess.ID + "/events")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		scanner := bufio.NewScanner(response.Body)
		requests = append(requests, pending{sess.ID, nextQuestion(t, scanner), scanner})
	}
	for i, request := range requests {
		answer := protocol.QuestionAnswerRequest{ConversationID: request.id, RunID: request.question.RunID, QuestionID: request.question.QuestionID, Answers: questionDefaults(request.question.Questions)}
		other := requests[1-i]
		wrong := answer
		wrong.ConversationID = other.id
		r := postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+other.id+"/questions/"+request.question.QuestionID, wrong, nil)
		r.Body.Close()
		if r.StatusCode != 404 {
			t.Fatalf("cross-session answer: %d", r.StatusCode)
		}
		answer.Answers[1].SelectedLabel = fmt.Sprintf("session-%d", i)
		answer.Answers[1].Custom = true
		r = postJSON(t, http.DefaultClient, server.URL+"/v1/sessions/"+request.id+"/questions/"+request.question.QuestionID, answer, nil)
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatal(r.StatusCode)
		}
	}
	for i, request := range requests {
		found := false
		for request.scanner.Scan() {
			line := request.scanner.Text()
			if strings.Contains(line, `"type":"tool.result"`) && strings.Contains(line, fmt.Sprintf("session-%d", i)) {
				found = true
			}
		}
		if !found {
			t.Fatalf("session %d did not receive its own answer", i)
		}
	}
}
