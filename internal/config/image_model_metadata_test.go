package config

import (
	"reflect"
	"testing"
)

func TestImageModelMetadataRoundTripAndSnapshot(t *testing.T) {
	model := Model{Providers: []string{"a", "b"}, OutputModalities: []string{"image"}, ImageAdapter: "openai-images"}
	model.SetProviderMetadata("b", Model{OutputModalities: []string{"text"}, ImageAdapter: "disabled"})
	cfg := &Config{Providers: map[string]Provider{"a": {API: "openai-completions", BaseURL: "https://a.invalid/v1"}, "b": {API: "openai-completions", BaseURL: "https://b.invalid/v1"}}, Models: map[string]Model{"painter": model}}
	raw, err := marshalConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var back Config
	if err := parseConfigJSONC(raw, &back); err != nil {
		t.Fatal(err)
	}
	for _, pid := range []string{"a", "b"} {
		want := cfg.Models["painter"].ForProvider(pid).Metadata()
		got := back.Models["painter"].ForProvider(pid).Metadata()
		if !reflect.DeepEqual(want, got) {
			t.Fatalf("image metadata lost for %s: %+v", pid, got)
		}
	}
	snap := back.Snapshot()
	mutated := snap.Models["painter"]
	mutated.OutputModalities[0] = "text"
	snap.Models["painter"] = mutated
	if back.Models["painter"].OutputModalities[0] != "image" {
		t.Fatal("model snapshot aliases output modalities")
	}
}
