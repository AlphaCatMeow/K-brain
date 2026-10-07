package routing

import (
	"context"
	"fmt"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

func ClientForProvider(prov config.Provider, name string, maxRetries int) (ai.Client, error) {
	return ClientForProviderContext(context.Background(), prov, name, maxRetries)
}

func ClientForProviderContext(ctx context.Context, prov config.Provider, name string, maxRetries int) (ai.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := prov.CacheCapabilities.Validate(); err != nil {
		return nil, err
	}
	if !ai.SupportedAPI(prov.API) {
		return nil, fmt.Errorf("unsupported API %q for provider %q", prov.API, name)
	}
	key, err := prov.ResolveKeyContext(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(key) == "" {
		return nil, fmt.Errorf("no API key for provider %q (set apiKey in ~/.liveagent/config.json)", name)
	}
	headers := make(map[string]string, len(prov.CustomHeaders))
	for _, h := range prov.CustomHeaders {
		headers[h.Key] = h.Value
	}
	api := prov.API
	if api == "" {
		switch prov.Type {
		case "claude_code":
			api = ai.APIMessages
		case "gemini":
			api = ai.APIGemini
		case "xai":
			api = ai.APIResponses
		case "codex":
			api = prov.RequestFormat
			if api == "" {
				api = ai.APIResponses
			}
		default:
			api = ai.APIChatCompletions
		}
	}
	client, err := ai.NewClient(ai.ClientOptions{API: api, BaseURL: prov.BaseURL, APIKey: key, MaxRetries: maxRetries, IsFullURL: prov.IsFullURL, ModelsURL: prov.ModelsURL, Headers: headers})
	if err != nil {
		return nil, fmt.Errorf("provider %q: %w", name, err)
	}
	configureCache(client, prov)
	return client, nil
}

func ClientForProviderOptional(prov config.Provider, name string, maxRetries int) (ai.Client, error) {
	return ClientForProviderOptionalContext(context.Background(), prov, name, maxRetries)
}

func ClientForProviderOptionalContext(ctx context.Context, prov config.Provider, name string, maxRetries int) (ai.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := prov.CacheCapabilities.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(prov.BaseURL) == "" || strings.TrimSpace(prov.APIKey) == "" {
		client := ai.New(prov.BaseURL, "")
		client.MaxRetries = maxRetries
		configureCache(client, prov)
		return client, nil
	}
	return ClientForProviderContext(ctx, prov, name, maxRetries)
}
func configureCache(client ai.Client, prov config.Provider) {
	o, ok := client.(interface{ SetCacheOptions(ai.CacheOptions) })
	if !ok {
		return
	}
	retention := prov.PromptCacheRetention
	if retention == "" {
		retention = "short"
	}
	if prov.PromptCachingEnabled != nil && !*prov.PromptCachingEnabled {
		retention = "none"
	}
	affinity := prov.CacheSessionAffinity != nil && *prov.CacheSessionAffinity
	host := ai.CacheEndpointHost(prov.BaseURL)
	long := host == "api.openai.com" || host == "api.anthropic.com"
	if prov.CacheCapabilities.SupportsLongRetention != nil {
		long = *prov.CacheCapabilities.SupportsLongRetention
	}
	key := true
	if host == "api.deepseek.com" || host == "api.x.ai" || prov.Type == "deepseek" || prov.Type == "xai" || prov.Type == "claude_code" || prov.Type == "gemini" || prov.API == ai.APIMessages || prov.API == ai.APIGemini {
		key = false
	}
	if prov.CacheCapabilities.SupportsPromptCacheKey != nil {
		key = *prov.CacheCapabilities.SupportsPromptCacheKey
	}
	o.SetCacheOptions(ai.CacheOptions{Retention: retention, SessionAffinity: affinity, ControlFormat: prov.CacheControlFormat, SupportsLong: long, SupportsKey: &key, AffinityFormat: prov.CacheCapabilities.SessionAffinityFormat, ResponsesCacheOptions: prov.CacheCapabilities.ResponsesCacheOptions})
}
