package routing

import (
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func TestProviderCacheEndpointDefaultsAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		url       string
		long, key bool
	}{
		{"https://api.openai.com/v1", true, true},
		{"https://api.openai.com.attacker.test/v1", false, true},
		{"https://relay.test/api.openai.com", false, true},
		{"https://api.openai.com@relay.test/v1", false, true},
		{"https://api.anthropic.com/v1", true, true},
		{"https://api.deepseek.com/v1", false, false},
		{"https://api.x.ai/v1", false, false},
	} {
		c := ai.New(tc.url, "fixture")
		configureCache(c, config.Provider{BaseURL: tc.url, PromptCacheRetention: "long"})
		if c.SupportsLongCacheRetention != tc.long || c.SupportsPromptCacheKey == nil || *c.SupportsPromptCacheKey != tc.key {
			t.Fatalf("wrong defaults for %s: %+v", tc.url, c)
		}
	}
	yes, no := true, false
	c := ai.New("https://relay.test", "fixture")
	configureCache(c, config.Provider{BaseURL: c.BaseURL, Type: "xai", CacheCapabilities: ai.CacheCapabilities{SupportsPromptCacheKey: &yes, SupportsLongRetention: &yes, SessionAffinityFormat: "openrouter"}})
	if !*c.SupportsPromptCacheKey || !c.SupportsLongCacheRetention || c.CacheSessionAffinityFormat != "openrouter" {
		t.Fatal("explicit proxy capabilities lost")
	}
	configureCache(c, config.Provider{BaseURL: "https://api.openai.com", PromptCachingEnabled: &no, CacheCapabilities: ai.CacheCapabilities{SupportsLongRetention: &no}})
	if c.CacheRetention != "none" || c.SupportsLongCacheRetention {
		t.Fatal("explicit disabled policy lost")
	}
}
