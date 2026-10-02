package ai

import (
	"context"
	"testing"
)

func TestHostedSearchParsesGeminiGrounding(t *testing.T) {
	var got []HostedSearch
	s := newSearchStream(WithSearchObserver(context.Background(), func(v HostedSearch) { got = append(got, v) }), "gemini")
	s.accept(`{"candidates":[{"groundingMetadata":{"webSearchQueries":["q"],"groundingChunks":[{"web":{"uri":"https://example.test","title":"Example"}}]}}]}`, "")
	if len(got) != 1 || got[0].Queries[0] != "q" || got[0].Sources[0].URL != "https://example.test" {
		t.Fatalf("got %#v", got)
	}
}

func TestNativeSearchNameRecognizesFetchAndSearch(t *testing.T) {
	for _, name := range []string{"web_search", "web_fetch", "web_search_20250305", "x_search_call"} {
		if !nativeSearchName(name) {
			t.Errorf("%q not recognized", name)
		}
	}
}
