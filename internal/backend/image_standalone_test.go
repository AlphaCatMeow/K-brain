package backend

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/session"
)

// An unavailable optional image module must leave the standalone chat API usable.
func TestStandaloneChatWithoutImageModule(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "standalone reply"})
	response, err := http.Get(server.URL + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	var health struct {
		Status       string `json:"status"`
		Capabilities struct {
			Image struct {
				Supported bool `json:"supported"`
			} `json:"image"`
		} `json:"capabilities"`
	}
	err = json.NewDecoder(response.Body).Decode(&health)
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || health.Status != "ok" || health.Capabilities.Image.Supported {
		t.Fatalf("health=%+v status=%d err=%v", health, response.StatusCode, err)
	}
	response, err = http.Get(server.URL + "/v1/image/projects")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("image status=%d", response.StatusCode)
	}
	sess := createTestSession(t, server.URL)
	accepted := runRequest(t, server.URL, sess.ID, "standalone-no-images", "say hello")
	response, err = http.Get(server.URL + "/v1/sessions/" + sess.ID + "/events?after_seq=0")
	if err != nil {
		t.Fatal(err)
	}
	events := readSSE(t, response)
	completed := false
	text := false
	for _, event := range events {
		if event.RunID != accepted.RunID {
			t.Fatalf("unexpected run: %+v", event)
		}
		if event.Type == protocol.EventRunCompleted {
			completed = true
		}
		if event.Type == protocol.EventTextDelta {
			text = true
		}
	}
	if !completed || !text {
		t.Fatalf("standalone chat did not complete: %+v", events)
	}
}
