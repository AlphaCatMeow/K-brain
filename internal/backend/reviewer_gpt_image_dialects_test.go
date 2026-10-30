package backend

import (
	"reflect"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
	"github.com/Stack-Cairn/K-brain/internal/imaging/adapters/direct"
)

// Vendor dialects pick their schema by exact base URL; the new GPT Image 2/2.5
// schema must not leak into Agnes or RollDek targets that serve those model names.
func TestReviewerGPTImageNamesKeepVendorDialectSchemas(t *testing.T) {
	for _, tc := range []struct {
		base   string
		models []string
		want   []imaging.ParameterSpec
	}{
		{"https://apihub.agnes-ai.com/v1", []string{"gpt-image-2", "gpt-image-2.5", "gpt-image-2.5-flare", "gpt-image-2.5-sunburst-2026-09-08"}, direct.AgnesParameters()},
		{"https://api.agnes-ai.cn/v1", []string{"gpt-image-2.5-flare"}, direct.AgnesParameters()},
		{"https://rolldek.com/v1", []string{"gpt-image-2.5", "gpt-image-2.5-flare"}, []imaging.ParameterSpec{}},
		{"https://api-cn.rolldek.com/v1", []string{"gpt-image-2.5"}, []imaging.ParameterSpec{}},
	} {
		cfg := config.Default()
		cfg.Providers["vendor"] = config.Provider{API: "openai-completions", BaseURL: tc.base, APIKey: "fake"}
		for _, m := range tc.models {
			cfg.Models[m] = config.Model{Providers: []string{"vendor"}}
		}
		manager, _ := imageProviderManager(t, cfg)
		targets, err := manager.ListImageTargets(imageTestScope())
		if err != nil || len(targets) != len(tc.models) {
			t.Fatalf("%s targets=%d err=%v", tc.base, len(targets), err)
		}
		for _, target := range targets {
			if !target.Enabled || !reflect.DeepEqual(target.Parameters, tc.want) {
				t.Errorf("%s %s enabled=%v params=%+v", tc.base, target.ModelID, target.Enabled, target.Parameters)
			}
		}
	}
}
