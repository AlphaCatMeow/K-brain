package backend

import (
	"reflect"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestProviderImageGPTImageSizeSchemas(t *testing.T) {
	cfg := config.Default()
	// An OpenAI-compatible proxy keeps the standard contract (no vendor dialect).
	cfg.Providers["moonup"] = config.Provider{API: "openai-completions", BaseURL: "https://proxy.example/v1", APIKey: "fake"}
	for _, model := range []string{"gpt-image-2.5-flare", "gpt-image-2", "gpt-image-1.5"} {
		cfg.Models[model] = config.Model{Providers: []string{"moonup"}}
	}
	manager, _ := imageProviderManager(t, cfg)
	targets, err := manager.ListImageTargets(imageTestScope())
	if err != nil || len(targets) != 3 {
		t.Fatalf("targets=%d err=%v", len(targets), err)
	}
	want := map[string][]string{
		"gpt-image-2.5-flare": {"aspectRatio", "imageSize", "quality", "output_format", "background", "output_compression"},
		"gpt-image-2":         {"aspectRatio", "imageSize", "quality", "output_format", "background", "output_compression"},
		"gpt-image-1.5":       {"size", "quality", "output_format", "background", "output_compression"},
	}
	for _, target := range targets {
		var keys []string
		for _, p := range target.Parameters {
			keys = append(keys, p.Key)
		}
		if !target.Enabled || !reflect.DeepEqual(keys, want[target.ModelID]) {
			t.Errorf("%s enabled=%v keys=%v", target.ModelID, target.Enabled, keys)
		}
	}
}
