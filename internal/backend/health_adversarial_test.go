package backend

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
)

// Adversarial review: the lazily created instanceId must be identical for
// concurrent first health requests (16 clients) and race-free under -race.
func TestHealthInstanceIDConcurrentFirstRequests(t *testing.T) {
	s := &Server{token: healthTestToken}
	const clients = 16
	ids := make([]any, clients)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			w := imageHTTPRequest(s, http.MethodGet, "/v1/health", nil)
			if w.Code != http.StatusOK {
				t.Errorf("health status = %d", w.Code)
				return
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Errorf("decode health: %v", err)
				return
			}
			ids[i] = body["instanceId"]
		}()
	}
	close(start)
	wg.Wait()
	for i := 1; i < clients; i++ {
		if ids[i] != ids[0] {
			t.Fatalf("concurrent first health requests saw different instance ids: %v vs %v", ids[0], ids[i])
		}
	}
}
