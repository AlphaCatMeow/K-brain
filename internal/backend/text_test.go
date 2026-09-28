package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestGenerateTextUsesBackendClientWithoutTools(t *testing.T) {
	store, err := openTestStore(t)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := &scriptedClient{response: "generated"}
	_, server := newTestServer(t, store, t.TempDir(), fixture)
	body := `{"model":{"provider":"fixture","model":"fixture-model"},"messages":[{"role":"system","content":[{"type":"text","text":"be concise"}]},{"role":"user","content":[{"type":"text","text":"hello"}]}],"output":"text"}`
	resp, err := http.Post(server.URL+"/v1/text/generate", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	var out protocol.TextGenerateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Version != protocol.Version || out.Text != "generated" {
		t.Fatalf("unexpected response: %+v", out)
	}
	if fixture.calls != 1 {
		t.Fatalf("expected one provider call, got %d", fixture.calls)
	}
}

func TestGenerateTextRejectsToolsAndDoesNotLeakProviderError(t *testing.T) {
	store, err := openTestStore(t)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fixture := &scriptedClient{response: "unused"}
	_, server := newTestServer(t, store, t.TempDir(), fixture)
	for _, body := range []string{
		`{"model":{"provider":"fixture","model":"fixture-model"},"messages":[{"role":"tool","tool_call_id":"secret","content":[{"type":"text","text":"no"}]}]}`,
		`{"model":{"provider":"fixture","model":"fixture-model"},"messages":[{"role":"user","content":[{"type":"image","image_url":"file:///secret"}]}]}`,
	} {
		resp, requestErr := http.Post(server.URL+"/v1/text/generate", "application/json", strings.NewReader(body))
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || strings.Contains(string(b), "secret") {
			t.Fatalf("unexpected validation response: %d %s", resp.StatusCode, b)
		}
	}
}

func TestWriteTextErrorHidesProviderDetails(t *testing.T) {
	recorder := &responseRecorder{header: make(http.Header)}
	writeTextError(recorder, context.Canceled)
	if recorder.status != http.StatusRequestTimeout || !strings.Contains(recorder.body, "cancelled") {
		t.Fatalf("unexpected cancellation: %d %s", recorder.status, recorder.body)
	}
	recorder = &responseRecorder{header: make(http.Header)}
	writeTextError(recorder, &ai.HTTPError{Status: "401 Unauthorized", Body: "Bearer provider-secret"})
	if recorder.status != http.StatusBadGateway || strings.Contains(recorder.body, "provider-secret") {
		t.Fatalf("provider detail leaked: %d %s", recorder.status, recorder.body)
	}
}

type responseRecorder struct {
	header http.Header
	status int
	body   string
}

func (r *responseRecorder) Header() http.Header    { return r.header }
func (r *responseRecorder) WriteHeader(status int) { r.status = status }
func (r *responseRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.body += string(p)
	return len(p), nil
}

func openTestStore(t *testing.T) (*session.Store, error) {
	t.Helper()
	return session.Open(t.TempDir())
}
