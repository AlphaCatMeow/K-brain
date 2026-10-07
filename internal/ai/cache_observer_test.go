package ai

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCacheObserverMeasuresWirePrefixWithoutContents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	c := New(srv.URL, "private-key")
	c.SetCacheOptions(CacheOptions{ControlFormat: "anthropic"})
	var observations []CacheObservation
	ctx := WithCacheObserver(t.Context(), func(o CacheObservation) { observations = append(observations, o) })
	messages := []Message{{Role: "system", Content: "private-system"}, {Role: "user", Content: "private-user"}}
	for _, input := range [][]Message{messages, append(append([]Message(nil), messages...), Message{Role: "assistant", Content: "ok"}, Message{Role: "user", Content: "new"})} {
		if _, _, err := c.Complete(ctx, Request{Model: "model", Messages: input}); err != nil {
			t.Fatal(err)
		}
	}
	if len(observations) != 2 || SharedCacheMessages(observations[0], observations[1]) != 2 {
		t.Fatalf("unstable wire prefix: %+v", observations)
	}
	data, _ := json.Marshal(observations)
	if strings.Contains(string(data), "private-") {
		t.Fatal("diagnostics leaked prompt or credential")
	}
	changed := observations[1]
	changed.ParametersHash = "changed"
	if SharedCacheMessages(observations[0], changed) != 0 {
		t.Fatal("changed tools/model/system must invalidate comparison")
	}
}

func TestCacheObserverPreservesSchemaFieldsAndNumericPrecision(t *testing.T) {
	var observations []CacheObservation
	ctx := WithCacheObserver(t.Context(), func(o CacheObservation) { observations = append(observations, o) })
	for _, body := range []string{
		`{"model":"m","tools":[{"function":{"parameters":{"properties":{"cache_control":{"const":9007199254740992}}}}}],"messages":[]}`,
		`{"model":"m","tools":[{"function":{"parameters":{"properties":{"cache_control":{"const":9007199254740993}}}}}],"messages":[]}`,
	} {
		observeCacheRequest(ctx, "/chat/completions", []byte(body))
	}
	if observations[0].ParametersHash == observations[1].ParametersHash {
		t.Fatal("schema or numeric identity lost")
	}
}
