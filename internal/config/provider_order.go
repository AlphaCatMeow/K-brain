package config

import (
	"maps"
	"math"
	"slices"
)

// providerOrderMetadataKey stores, inside Provider.Metadata, the provider's position in the
// user's display order. Like modelOverridesMetadataKey it lives in the free-form metadata map
// because older builds decode the config with DisallowUnknownFields; they simply ignore it.
const providerOrderMetadataKey = "kbrainProviderOrder"

// OrderedProviderIDs returns every configured provider ID in display order: IDs listed in
// ProviderOrder first, then any remaining providers sorted by ID.
func (c *Config) OrderedProviderIDs() []string {
	out := make([]string, 0, len(c.Providers))
	seen := make(map[string]bool, len(c.Providers))
	for _, id := range c.ProviderOrder {
		if _, ok := c.Providers[id]; ok && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(c.Providers)) {
		if !seen[id] {
			out = append(out, id)
		}
	}
	return out
}

// takeProviderOrder removes the order entry from the provider metadata. Invalid values are
// ignored so a hand-edited config never stops K-brain from starting.
func takeProviderOrder(provider *Provider) (int, bool) {
	raw, ok := provider.Metadata[providerOrderMetadataKey]
	if !ok {
		return 0, false
	}
	metadata := maps.Clone(provider.Metadata)
	delete(metadata, providerOrderMetadataKey)
	if len(metadata) == 0 {
		metadata = nil
	}
	provider.Metadata = metadata
	n, ok := raw.(float64)
	if !ok || n < 0 || n != math.Trunc(n) {
		return 0, false
	}
	return int(n), true
}

// withProviderOrder returns the provider metadata to persist, carrying the order entry only
// when the user has ordered providers explicitly.
func withProviderOrder(metadata map[string]any, index int, ordered bool) map[string]any {
	out := maps.Clone(metadata)
	delete(out, providerOrderMetadataKey)
	if ordered {
		if out == nil {
			out = map[string]any{}
		}
		out[providerOrderMetadataKey] = index
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
