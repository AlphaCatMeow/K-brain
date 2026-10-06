package backend

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestRunAcceptanceLookupWithoutModelInitialization(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	dir := t.TempDir()
	service, host := newTestServer(t, store, dir, &scriptedClient{wait: true})
	sess := createTestSession(t, host.URL)
	path := "/v1/sessions/" + sess.ID + "/runs"
	for _, query := range []struct {
		suffix string
		status int
	}{{"", 400}, {"?client_request_id=unknown", 404}} {
		if code := doJSON(t, http.MethodGet, host.URL+path+query.suffix, nil, nil); code != query.status {
			t.Fatalf("lookup status=%d", code)
		}
	}
	var accepted, found protocol.RunAccepted
	input := protocol.PromptRequest{ClientRequestID: "lookup request/1", Prompt: "hello"}
	if code := doJSON(t, http.MethodPost, host.URL+path, input, &accepted); code != 202 {
		t.Fatalf("start=%d", code)
	}
	query := "?client_request_id=lookup%20request%2F1"
	if code := doJSON(t, http.MethodGet, host.URL+path+query, nil, &found); code != 200 || !reflect.DeepEqual(accepted, found) {
		t.Fatalf("lookup=%d %+v", code, found)
	}
	rt, err := service.loadRuntimeByID(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	rt.mu.Lock()
	rt.runtimeErr = fmt.Errorf("persistence unavailable")
	rt.mu.Unlock()
	if code := doJSON(t, http.MethodGet, host.URL+path+query, nil, nil); code != 500 {
		t.Fatalf("quarantined lookup=%d", code)
	}
	rt.mu.Lock()
	rt.runtimeErr = nil
	rt.mu.Unlock()
	service.Close()
	restored, err := New(Options{Store: store, EventDir: dir, Factory: func(context.Context, string, protocol.ModelRef) (*agent.Agent, error) {
		t.Error("lookup initialized model")
		return nil, fmt.Errorf("no models")
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restarted := httptest.NewServer(restored)
	defer restarted.Close()
	if code := doJSON(t, http.MethodGet, restarted.URL+path+query, nil, &found); code != 200 || !reflect.DeepEqual(accepted, found) {
		t.Fatalf("restored=%d %+v", code, found)
	}
	if len(restored.sessions) != 0 {
		t.Fatal("lookup installed a runtime")
	}
}
