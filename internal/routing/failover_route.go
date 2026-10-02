package routing

import (
	"context"
	"fmt"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
)

// ResolveFailoverRouteContext builds the backend-owned same-vendor queue for a
// selected model. The selected provider remains primary; provider settings and
// retry policy are captured per candidate at route construction time.
func ResolveFailoverRouteContext(ctx context.Context, cfg *config.Config, modelName, providerName string, optional bool) (Route, error) {
	if cfg == nil {
		return Route{}, fmt.Errorf("configuration is nil")
	}
	primaryProvider, model, apiID, err := ResolveWithRefreshContext(ctx, cfg, modelName, providerName)
	if err != nil {
		return Route{}, err
	}
	modelName, providerName = SelectionNames(cfg, model, modelName, providerName)
	if modelName == "" {
		modelName = apiID
	}
	if providerName == "" {
		return Route{}, fmt.Errorf("no provider configured for model %q", modelName)
	}
	primaryVendor := providerVendor(providerName, primaryProvider)
	providerNames := append([]string{providerName}, model.Providers...)
	providerNames = uniqueStrings(providerNames)

	candidates := make([]failoverCandidate, 0, len(providerNames))
	var lastErr error
	for _, name := range providerNames {
		provider, ok := cfg.Providers[name]
		if !ok || providerVendor(name, provider) != primaryVendor {
			continue
		}
		if provider.ActiveModels != nil && !containsModel(provider.ActiveModels, apiID) {
			continue
		}
		settings, settingsErr := ParseProviderRuntimeSettings(provider)
		if settingsErr != nil {
			if name == providerName {
				return Route{}, settingsErr
			}
			lastErr = settingsErr
			continue
		}
		var client ai.Client
		if optional {
			client, err = ClientForProviderOptionalContext(ctx, provider, name, 0)
		} else {
			client, err = ClientForProviderContext(ctx, provider, name, 0)
		}
		if err != nil {
			lastErr = err
			continue
		}
		applyRetryPolicy(client, settings.Retry)
		candidates = append(candidates, failoverCandidate{name: name, provider: provider, client: client, settings: settings})
	}
	if len(candidates) == 0 {
		if lastErr != nil {
			return Route{}, lastErr
		}
		return Route{}, fmt.Errorf("no configured same-vendor provider for model %q", apiID)
	}
	primaryIndex := 0
	for i, candidate := range candidates {
		if candidate.name == providerName {
			primaryIndex = i
			break
		}
	}
	if candidates[primaryIndex].name != providerName {
		return Route{}, fmt.Errorf("selected provider %q is unavailable", providerName)
	}
	if primaryIndex != 0 {
		candidates[0], candidates[primaryIndex] = candidates[primaryIndex], candidates[0]
	}
	client := &FailoverClient{candidates: candidates, primary: 0, maxSwitches: candidates[0].settings.Failover.MaxSwitches}
	catalogs := config.LoadCatalogs()
	ctxLimit, maxOut := TokenLimits(model, catalogs[providerName], apiID)
	return Route{
		ModelName: modelName, ProviderName: providerName,
		Provider: primaryProvider, Model: model, APIModel: apiID,
		Client: client, ContextLimit: ctxLimit, MaxOutput: maxOut,
		Vision: SupportsVision(cfg, modelName, apiID, catalogs, providerName),
	}, nil
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
