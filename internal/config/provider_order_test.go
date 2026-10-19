package config

import (
	"slices"
	"strings"
	"testing"
)

func TestProviderOrderRoundTripsThroughMetadataForOlderBuilds(t *testing.T) {
	cfg := &Config{Providers: map[string]Provider{
		"a": {API: "openai-completions", BaseURL: "https://a.invalid/v1"},
		"b": {API: "openai-completions", BaseURL: "https://b.invalid/v1", Metadata: map[string]any{"vendor": "relay"}},
		"c": {API: "openai-completions", BaseURL: "https://c.invalid/v1"},
	}, ProviderOrder: []string{"c", "a", "b"}}
	data, err := marshalConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	olderBuildAccepts(t, data)

	var back Config
	if err := parseConfigJSONC(data, &back); err != nil {
		t.Fatal(err)
	}
	if got := back.OrderedProviderIDs(); !slices.Equal(got, []string{"c", "a", "b"}) {
		t.Fatalf("order = %v", got)
	}
	if got := back.Providers["b"].Metadata; len(got) != 1 || got["vendor"] != "relay" {
		t.Fatalf("provider metadata = %#v", got)
	}
	if got := back.Providers["a"].Metadata; got != nil {
		t.Fatalf("order entry leaked into metadata: %#v", got)
	}
	if snap := back.Snapshot(); !slices.Equal(snap.ProviderOrder, back.ProviderOrder) {
		t.Fatalf("snapshot order = %v", snap.ProviderOrder)
	}
}

func TestProviderOrderUnsetKeepsIDOrderAndFileUnchanged(t *testing.T) {
	cfg := &Config{Providers: map[string]Provider{
		"b": {API: "openai-completions", BaseURL: "https://b.invalid/v1"},
		"a": {API: "openai-completions", BaseURL: "https://a.invalid/v1"},
	}}
	data, err := marshalConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), providerOrderMetadataKey) {
		t.Fatalf("unordered config gained an order entry:\n%s", data)
	}
	if got := cfg.OrderedProviderIDs(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("order = %v", got)
	}
}

func TestProviderOrderIgnoresInvalidAndPartialEntries(t *testing.T) {
	raw := `{"providers":{
		"a":{"api":"openai-completions","baseUrl":"https://a.invalid/v1","metadata":{"kbrainProviderOrder":"first"}},
		"b":{"api":"openai-completions","baseUrl":"https://b.invalid/v1","metadata":{"kbrainProviderOrder":1}},
		"c":{"api":"openai-completions","baseUrl":"https://c.invalid/v1","metadata":{"kbrainProviderOrder":0}},
		"d":{"api":"openai-completions","baseUrl":"https://d.invalid/v1"}
	}}`
	var cfg Config
	if err := parseConfigJSONC([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.OrderedProviderIDs(); !slices.Equal(got, []string{"c", "b", "a", "d"}) {
		t.Fatalf("order = %v", got)
	}
	if got := cfg.Providers["a"].Metadata; got != nil {
		t.Fatalf("invalid order entry was kept: %#v", got)
	}
}
