package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
	"github.com/Stack-Cairn/K-brain/internal/imaging/adapters/direct"
)

func imageProviderManager(t *testing.T, cfg *config.Config) (*ImageSettingsManager, *SettingsStore) {
	t.Helper()
	store := NewSettingsStore(cfg, func(*config.Config) error { return nil })
	manager, err := NewImageSettingsManager(store, filepath.Join(t.TempDir(), "secrets.json"), ImageSettingsOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return manager, store
}

func TestProviderImageModelIdentification(t *testing.T) {
	for _, tc := range []struct {
		name, api, id string
		model         config.Model
		adapter       string
		found         bool
	}{
		{"default OpenAI API", "", "gpt-image-1", config.Model{}, direct.OpenAIID, true},
		{"Google API alias", "google-gemini", "gemini-2.5-flash-image", config.Model{}, direct.GoogleID, true},
		{"known OpenAI", "openai-responses", "gpt-image-1", config.Model{}, direct.OpenAIID, true},
		{"known dated", "openai-completions", "gpt-image-2-2026-04-21", config.Model{}, direct.OpenAIID, true},
		{"known Google", "google-generative-ai", "gemini-2.5-flash-image", config.Model{}, direct.GoogleID, true},
		{"vision is input only", "openai-completions", "gpt-4o", config.Model{Vision: true, InputModalities: []string{"text", "image"}}, "", false},
		{"Google vision chat", "google-generative-ai", "gemini-2.5-pro", config.Model{Vision: true}, "", false},
		{"name is not evidence", "openai-completions", "chat-image-helper", config.Model{DisplayName: "Image generator"}, "", false},
		{"explicit text output wins", "openai-completions", "gpt-image-1", config.Model{OutputModalities: []string{"text"}}, "", false},
		{"explicit opt out", "openai-completions", "gpt-image-1", config.Model{ImageAdapter: "disabled"}, "", false},
		{"output metadata", "openai-completions", "custom-painter", config.Model{OutputModalities: []string{"image"}}, direct.OpenAIID, true},
		{"explicit alias", "google-generative-ai", "custom-painter", config.Model{ImageAdapter: direct.GoogleID}, direct.GoogleID, true},
		{"explicit protocol wins", "openai-completions", "gemini-2.5-flash-image", config.Model{ImageAdapter: direct.GoogleID}, direct.GoogleID, true},
		{"unsupported DALL-E contract", "openai-completions", "dall-e-3", config.Model{OutputModalities: []string{"image"}}, "", true},
		{"unsupported Google Imagen", "google-generative-ai", "imagen-4", config.Model{OutputModalities: []string{"image"}}, "", true},
		{"unsupported provider protocol", "anthropic-messages", "gpt-image-1", config.Model{}, "", true},
		{"no guessed Google proxy protocol", "openai-completions", "gemini-2.5-flash-image", config.Model{}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter, found := imageModelAdapter(config.Provider{API: tc.api}, tc.id, tc.model)
			if adapter != tc.adapter || found != tc.found {
				t.Fatalf("got adapter=%q found=%v", adapter, found)
			}
		})
	}
}

func TestProviderImageTargetsWireArraysAndRequiredBoolean(t *testing.T) {
	server := imageHTTPFixture(t)
	cfg := config.Default()
	cfg.Providers["openai"] = config.Provider{API: "openai-completions", BaseURL: "https://openai.example/v1", APIKey: "fake"}
	cfg.Providers["google"] = config.Provider{API: "google-generative-ai", BaseURL: "https://google.example/v1beta", APIKey: "fake"}
	cfg.Providers["unsupported"] = config.Provider{API: "anthropic-messages", BaseURL: "https://unsupported.example/v1", APIKey: "fake"}
	cfg.Models["gpt-image-1"] = config.Model{Providers: []string{"openai", "unsupported"}}
	cfg.Models["gemini-3-pro-image-preview"] = config.Model{Providers: []string{"google"}}
	server.image.Settings.store.mu.Lock()
	server.image.Settings.store.cfg = cfg.Snapshot()
	server.image.Settings.store.mu.Unlock()
	response := imageHTTPRequest(server, http.MethodGet, "/v1/image/targets", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	var wire struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Items) != 3 {
		t.Fatalf("targets=%d", len(wire.Items))
	}
	unsupportedSeen, parameterSeen := false, false
	for _, item := range wire.Items {
		capabilities, ok := item["capabilities"].(map[string]any)
		if !ok {
			t.Fatal("capabilities must be an object")
		}
		for _, key := range []string{"operations", "outputFormats"} {
			if _, ok := capabilities[key].([]any); !ok {
				t.Fatalf("%s must always be a JSON array, got %#v", key, capabilities[key])
			}
		}
		parameters, ok := item["parameters"].([]any)
		if !ok {
			t.Fatalf("parameters must always be a JSON array, got %#v", item["parameters"])
		}
		for _, raw := range parameters {
			parameter, ok := raw.(map[string]any)
			if !ok {
				t.Fatal("parameter must be an object")
			}
			if _, ok := parameter["required"].(bool); !ok {
				t.Fatal("parameter.required must be present as a JSON boolean even when false")
			}
			parameterSeen = true
		}
		if item["providerId"] == "unsupported" {
			unsupportedSeen = true
			if item["enabled"] != false || item["disabledReason"] != "unsupported_image_protocol" || len(capabilities["outputFormats"].([]any)) != 0 || len(parameters) != 0 {
				t.Fatal("disabled unsupported target must retain empty array wire contract")
			}
		}
	}
	if !unsupportedSeen || !parameterSeen {
		t.Fatal("test did not cover disabled target and supported parameter schemas")
	}
}

func TestProviderImageTargetsHTTPRefreshAndRedaction(t *testing.T) {
	server := imageHTTPFixture(t)
	manager := server.image.Settings
	cfg := config.Default()
	cfg.Providers["p"] = config.Provider{Name: "Configured provider", API: "openai-responses", APIKey: "private-credential-sentinel", BaseURL: "https://image.example/v1", CustomHeaders: []config.CustomHeader{{Key: "X-Tenant", Value: "private-header-sentinel"}}}
	cfg.Models["gpt-image-1"] = config.Model{Providers: []string{"p"}, DisplayName: "Painter"}
	cfg.Models["gpt-4o"] = config.Model{Providers: []string{"p"}, Vision: true}
	manager.store.mu.Lock()
	manager.store.cfg = cfg.Snapshot()
	manager.store.mu.Unlock()
	list := func() []imaging.ImageTarget {
		t.Helper()
		w := imageHTTPRequest(server, http.MethodGet, "/v1/image/targets", nil)
		if w.Code != 200 {
			t.Fatalf("list status %d", w.Code)
		}
		for _, secret := range []string{"private-credential-sentinel", "private-header-sentinel", "image.example", "apiKey", "customHeaders"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("target response leaked execution configuration")
			}
		}
		var response struct{ Items []imaging.ImageTarget }
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response.Items
	}
	targets := list()
	if len(targets) != 1 {
		t.Fatalf("want 1 image model, got %d", len(targets))
	}
	first := targets[0]
	if first.Name != "Configured provider · Painter" || first.Source != "provider" || first.ProviderID != "p" || first.ModelID != "gpt-image-1" || !first.Enabled || first.VerificationState.State != "provider_configured" || first.VerificationState.LiveVerifiedAt != nil || !strings.HasPrefix(first.ID, "it_provider_") {
		t.Fatalf("bad projection: %+v", first)
	}
	if first.Capabilities.SupportsContinuation || first.Capabilities.SupportsCancel || first.Capabilities.SupportsReconcile || first.Capabilities.SupportsPreview || !slices.Equal(first.Capabilities.Operations, []imaging.Operation{imaging.OperationGenerate, imaging.OperationReference, imaging.OperationEdit, imaging.OperationMask}) || first.Capabilities.MaxInputs != 16 {
		t.Fatal("unsupported OpenAI capabilities advertised")
	}
	snapshot, err := manager.ResolveTarget(context.Background(), imageTestScope(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.CheckProviderImageTarget(context.Background(), imageTestScope(), snapshot); err != nil {
		t.Fatal(err)
	}
	publicSettings, err := manager.ImageSettings(imageTestScope())
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{snapshot, publicSettings} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		for _, private := range []string{"private-credential-sentinel", "private-header-sentinel", "image.example", "customHeaders", "apiKey"} {
			if strings.Contains(string(raw), private) {
				t.Fatal("snapshot or public settings leaked private execution configuration")
			}
		}
	}
	connection, err := manager.ResolveImageTargetConnection(context.Background(), imageTestScope(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.HTTPClient.CloseIdleConnections()
	if _, err := json.Marshal(connection); err == nil {
		t.Fatal("execution connection was serializable")
	}
	if key, err := connection.SecretResolver.ResolveSecret(context.Background(), imageTestScope(), connection.SecretRefs["apiKey"]); err != nil || key != "private-credential-sentinel" {
		t.Fatal("provider credential not inherited")
	}
	manager.store.mu.Lock()
	provider := manager.store.cfg.Providers["p"]
	provider.BaseURL = "https://changed.example/v1"
	provider.APIKey = "new-private-credential"
	manager.store.cfg.Providers["p"] = provider
	manager.store.mu.Unlock()
	refreshed := list()
	if len(refreshed) != 1 || refreshed[0].ID != first.ID || refreshed[0].Revision == first.Revision {
		t.Fatal("binding refresh lost stable selection or failed to invalidate revision")
	}
	if err := manager.CheckProviderImageTarget(context.Background(), imageTestScope(), snapshot); err == nil {
		t.Fatal("stale queued binding accepted")
	}
	if _, err := connection.SecretResolver.ResolveSecret(context.Background(), imageTestScope(), connection.SecretRefs["apiKey"]); err == nil {
		t.Fatal("resolved credential survived binding change")
	}
	fresh, err := manager.ResolveTarget(context.Background(), imageTestScope(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	current, err := manager.ResolveImageTargetConnection(context.Background(), imageTestScope(), fresh)
	if err != nil {
		t.Fatal(err)
	}
	defer current.HTTPClient.CloseIdleConnections()
	if current.BaseURL != "https://changed.example/v1" {
		t.Fatal("endpoint did not refresh")
	}
	if key, err := current.SecretResolver.ResolveSecret(context.Background(), imageTestScope(), current.SecretRefs["apiKey"]); err != nil || key != "new-private-credential" {
		t.Fatal("credential did not refresh")
	}
	// Durable snapshots remain executable after a manager/store restart with the same config.
	restarted, _ := imageProviderManager(t, manager.store.Snapshot())
	if err := restarted.CheckProviderImageTarget(context.Background(), imageTestScope(), fresh); err != nil {
		t.Fatal("deterministic revision changed across restart")
	}
	manager.store.mu.Lock()
	provider = manager.store.cfg.Providers["p"]
	provider.ActiveModels = []string{}
	manager.store.cfg.Providers["p"] = provider
	manager.store.mu.Unlock()
	if len(list()) != 0 {
		t.Fatal("inactive model remained discoverable")
	}
	if _, err := manager.ResolveTarget(context.Background(), imageTestScope(), first.ID); err == nil {
		t.Fatal("removed model still resolved")
	}
	if _, err := manager.ListImageTargets(imaging.Scope{OwnerID: "other", BackendID: "local"}); err == nil {
		t.Fatal("foreign scope accepted")
	}
	if got := manager.store.Snapshot().Image; got != nil {
		t.Fatal("derived targets were persisted into standalone image config")
	}
}

func TestProviderImageTargetsPerProviderMetadataAndDisabled(t *testing.T) {
	cfg := config.Default()
	for _, id := range []string{"a", "b"} {
		cfg.Providers[id] = config.Provider{API: "openai-completions", BaseURL: "https://image.example/v1", APIKey: "fake"}
	}
	model := config.Model{Providers: []string{"a", "b"}, OutputModalities: []string{"image"}}
	model.SetProviderMetadata("b", config.Model{OutputModalities: []string{"text"}})
	cfg.Models["alias"] = model
	cfg.Providers["missing-key"] = config.Provider{API: "google-generative-ai", BaseURL: "https://google.example"}
	cfg.Models["gemini-2.5-flash-image"] = config.Model{Providers: []string{"missing-key"}}
	manager, _ := imageProviderManager(t, cfg)
	targets, err := manager.ListImageTargets(imageTestScope())
	if err != nil || len(targets) != 2 {
		t.Fatalf("targets=%d err=%v", len(targets), err)
	}
	if targets[0].ProviderID != "a" || !targets[0].Enabled {
		t.Fatal("per-provider metadata ignored")
	}
	if targets[1].Enabled || targets[1].DisabledReason != "auth_required" {
		t.Fatal("missing credential was executable")
	}
	if _, err := manager.ResolveTarget(context.Background(), imageTestScope(), targets[1].ID); err == nil {
		t.Fatal("disabled target resolved")
	}
	if _, err := manager.UpdateImageSettings(context.Background(), imageTestScope(), imaging.ImageSettingsUpdate{Targets: targets}); err == nil {
		t.Fatal("derived targets accepted by legacy settings write")
	}
}

func TestProviderImageEndpointMapping(t *testing.T) {
	for _, tc := range []struct {
		base              string
		full              bool
		adapter, expected string
	}{
		{"https://openai.example", false, direct.OpenAIID, "https://openai.example/v1"},
		{"https://openai.example/prefix/v1/chat/completions", true, direct.OpenAIID, "https://openai.example/prefix/v1"},
		{"https://openai.example/v1/responses", true, direct.OpenAIID, "https://openai.example/v1"},
		{"https://google.example", false, direct.GoogleID, "https://google.example/v1beta"},
		{"https://google.example/v1beta/models/chat:streamGenerateContent", true, direct.GoogleID, "https://google.example/v1beta"},
		{"https://openai.example/arbitrary", true, direct.OpenAIID, ""},
		{"https://openai.example/v1?key=private", false, direct.OpenAIID, ""},
		{"https://user:private@openai.example/v1", false, direct.OpenAIID, ""},
		{"http://external.example/v1", false, direct.OpenAIID, ""},
	} {
		t.Run(tc.base, func(t *testing.T) {
			got, ok := providerImageBaseURL(config.Provider{BaseURL: tc.base, IsFullURL: tc.full}, tc.adapter)
			if got != tc.expected || ok != (tc.expected != "") {
				t.Fatalf("got %q ok=%v", got, ok)
			}
		})
	}
}

func TestProviderImageCatalogRemovalAndLegacyCompatibility(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["p"] = config.Provider{API: "openai-completions", BaseURL: "https://image.example/v1", APIKey: "fake", Models: []config.PiModel{{ID: "gpt-image-1"}}}
	cfg.Models["gpt-image-1"] = config.Model{Providers: []string{"p"}}
	cfg.Image = &imaging.ImageSettings{Connections: []imaging.ImageConnection{imageTestConnection()}, Targets: []imaging.ImageTarget{imageTestTarget()}}
	manager, store := imageProviderManager(t, cfg)
	targets, err := manager.ListImageTargets(imageTestScope())
	if err != nil || len(targets) != 2 || targets[0].ID != "it_test" {
		t.Fatal("legacy target was not retained alongside provider target")
	}
	store.mu.Lock()
	next := store.cfg.Snapshot()
	if err := applySettings(next, settingsUpdate{Providers: []settingsProviderUpdate{{ID: "p", Models: []settingsModelUpdate{}}}}); err != nil {
		store.mu.Unlock()
		t.Fatal(err)
	}
	store.cfg = next
	store.mu.Unlock()
	targets, err = manager.ListImageTargets(imageTestScope())
	if err != nil || len(targets) != 1 || targets[0].Source != "" {
		t.Fatal("removed canonical model was resurrected by stale provider model storage")
	}
}

func TestProviderImageUnsupportedHeadersAreDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["p"] = config.Provider{API: "openai-completions", BaseURL: "https://image.example/v1", APIKey: "fake", CustomHeaders: []config.CustomHeader{{Key: "Authorization", Value: "private"}}}
	cfg.Models["gpt-image-1"] = config.Model{Providers: []string{"p"}}
	manager, _ := imageProviderManager(t, cfg)
	targets, err := manager.ListImageTargets(imageTestScope())
	if err != nil || len(targets) != 1 || targets[0].Enabled || targets[0].DisabledReason != "unsupported_provider_headers" {
		t.Fatal("adapter-incompatible headers advertised as usable")
	}
}

func TestProviderImageSettingsMetadataRefresh(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["p"] = config.Provider{API: "openai-completions", BaseURL: "https://image.example/v1", APIKey: "fake"}
	cfg.Models["alias"] = config.Model{Providers: []string{"p"}, OutputModalities: []string{"image"}, ImageAdapter: direct.OpenAIID}
	// A frontend refresh that omits new metadata must preserve explicit configuration.
	if err := applySettings(cfg, settingsUpdate{Providers: []settingsProviderUpdate{{ID: "p", Models: []settingsModelUpdate{{ID: "alias"}}}}}); err != nil {
		t.Fatal(err)
	}
	got := projectSettings(cfg)
	var projected settingsModel
	for _, model := range got.Models {
		if model.Provider == "p" {
			projected = model
		}
	}
	if projected.ImageAdapter != direct.OpenAIID || !slices.Equal(projected.OutputModalities, []string{"image"}) {
		t.Fatal("settings roundtrip lost image metadata")
	}
	disabled := "disabled"
	if err := applySettings(cfg, settingsUpdate{Providers: []settingsProviderUpdate{{ID: "p", Models: []settingsModelUpdate{{ID: "alias", ImageAdapter: &disabled}}}}}); err != nil {
		t.Fatal(err)
	}
	manager, _ := imageProviderManager(t, cfg)
	targets, err := manager.ListImageTargets(imageTestScope())
	if err != nil || len(targets) != 0 {
		t.Fatal("model opt-out did not refresh discovery")
	}
}

func TestProviderImageGoogleSizeSchemas(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["p"] = config.Provider{API: "google-generative-ai", BaseURL: "https://image.example/v1beta", APIKey: "fake"}
	for _, model := range []string{"gemini-2.5-flash-image", "gemini-3-pro-image-preview", "gemini-3.1-flash-image-preview"} {
		cfg.Models[model] = config.Model{Providers: []string{"p"}}
	}
	manager, _ := imageProviderManager(t, cfg)
	targets, err := manager.ListImageTargets(imageTestScope())
	if err != nil || len(targets) != 3 {
		t.Fatalf("targets=%d err=%v", len(targets), err)
	}
	for _, target := range targets {
		if !target.Enabled || len(target.Parameters) == 0 || target.Parameters[0].Key != "aspectRatio" {
			t.Fatal("Google aspect ratio missing from target form schema")
		}
		sizes := map[string][]any{"gemini-3-pro-image-preview": {"1K", "2K", "4K"}, "gemini-3.1-flash-image-preview": {"512", "1K", "2K", "4K"}}
		if expected := sizes[target.ModelID]; expected != nil {
			if len(target.Parameters) != 2 || target.Parameters[1].Key != "imageSize" || !reflect.DeepEqual(target.Parameters[1].Enum, expected) {
				t.Fatal("wrong Google image size form schema")
			}
		} else if len(target.Parameters) != 1 {
			t.Fatal("Gemini 2.5 advertised unsupported imageSize")
		}
	}
}

func TestProviderImageConnectionProtocolOverridesChatAPI(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["proxy"] = config.Provider{API: "openai-completions", BaseURL: "https://proxy.example/v1beta", APIKey: "fake"}
	cfg.Models["native-google-alias"] = config.Model{Providers: []string{"proxy"}, ImageAdapter: direct.GoogleID}
	manager, _ := imageProviderManager(t, cfg)
	targets, err := manager.ListImageTargets(imageTestScope())
	if err != nil || len(targets) != 1 || !targets[0].Enabled {
		t.Fatalf("discovery failed: %v", err)
	}
	target, err := manager.ResolveTarget(context.Background(), imageTestScope(), targets[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := manager.ResolveImageTargetConnection(context.Background(), imageTestScope(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.HTTPClient.CloseIdleConnections()
	if connection.AuthKind != "api_key_header" {
		t.Fatal("chat API incorrectly determined explicit image authentication")
	}
	if target.Name != "proxy · native-google-alias" {
		t.Fatal("provider ID fallback missing from target display name")
	}
	if target.Capabilities.MaxInputs != 14 || !reflect.DeepEqual(target.Capabilities.Operations, []imaging.Operation{imaging.OperationGenerate, imaging.OperationReference, imaging.OperationEdit}) || len(target.Parameters) != 1 || target.Parameters[0].Key != "aspectRatio" {
		t.Fatal("incorrect Google capability contract")
	}
}

func TestProviderImageRollDekAllowsEditAndReferenceWithoutParameters(t *testing.T) {
	cfg := config.Default()
	cfg.Providers["rolldek"] = config.Provider{Name: "RollDek", API: "openai-completions", BaseURL: "https://api-cn.rolldek.com/v1", APIKey: "fake", Models: []config.PiModel{{ID: "gpt-image-2.5"}}}
	cfg.Models["gpt-image-2.5"] = config.Model{Providers: []string{"rolldek"}}
	manager, _ := imageProviderManager(t, cfg)
	targets, err := manager.ListImageTargets(imageTestScope())
	if err != nil || len(targets) != 1 || !targets[0].Enabled {
		t.Fatalf("targets=%#v err=%v", targets, err)
	}
	c := targets[0].Capabilities
	want := []imaging.Operation{imaging.OperationGenerate, imaging.OperationReference, imaging.OperationEdit}
	if !reflect.DeepEqual(c.Operations, want) || c.MaxInputs < 2 || len(targets[0].Parameters) != 0 || !reflect.DeepEqual(c.OutputFormats, []string{"png"}) {
		t.Fatalf("RollDek capabilities=%#v parameters=%#v", c, targets[0].Parameters)
	}
}

func TestProviderImageRevisionIgnoresChatOnlyProviderSettings(t *testing.T) {
	revision := func(mutate func(*config.Provider)) uint64 {
		cfg := config.Default()
		p := config.Provider{API: "openai-completions", BaseURL: "https://image.example/v1", APIKey: "fake", Models: []config.PiModel{{ID: "gpt-image-1"}, {ID: "chat-model"}}}
		mutate(&p)
		cfg.Providers["p"] = p
		cfg.Models["gpt-image-1"] = config.Model{Providers: []string{"p"}}
		manager, _ := imageProviderManager(t, cfg)
		targets, err := manager.ListImageTargets(imageTestScope())
		if err != nil || len(targets) != 1 {
			t.Fatalf("targets=%d err=%v", len(targets), err)
		}
		return targets[0].Revision
	}
	base := revision(func(*config.Provider) {})
	chatOnly := revision(func(p *config.Provider) {
		p.ModelOrder = []string{"chat-model", "gpt-image-1"}
		p.RetryPolicy = map[string]any{"maxAttempts": 3}
		p.UsageQuery = map[string]any{"enabled": true}
		p.Reasoning = "high"
	})
	if chatOnly != base {
		t.Fatal("chat-only provider settings changed the image target revision")
	}
	if revision(func(p *config.Provider) { p.BaseURL = "https://other.example/v1" }) == base {
		t.Fatal("a changed endpoint must change the image target revision")
	}
}

func TestLegacyImageSettingsRejectDerivedConnectionNamespace(t *testing.T) {
	manager, _ := imageProviderManager(t, config.Default())
	connection := imageTestConnection()
	connection.ID = "ic_provider_0123456789abcdef0123456789abcdef"
	if _, err := manager.UpdateImageSettings(context.Background(), imageTestScope(), imaging.ImageSettingsUpdate{Connections: []imaging.ImageConnection{connection}}); err == nil {
		t.Fatal("legacy connection accepted in the derived ic_provider_ namespace")
	}
}
