package backend

import (
	"net/http"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/session"
)

func TestServerWithoutMCPReturnsUnavailable(t *testing.T) {
	store, err := session.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, server := newTestServer(t, store, t.TempDir(), &scriptedClient{response: "ok"})
	resp, err := server.Client().Get(server.URL + "/v1/mcp")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotImplemented)
	}
}
