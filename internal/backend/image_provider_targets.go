package backend

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/imaging"
	"github.com/Stack-Cairn/K-brain/internal/imaging/adapters/direct"
)

// Provider targets are a read-only projection of the ordinary model catalog.
// They never enter ImageSettings, secret storage, or the paid verification flow.
// IDs depend only on provider/model identity, so refreshing settings preserves selection.
func providerImageID(prefix string, parts ...string) string {
	raw, _ := json.Marshal(parts)
	sum := sha256.Sum256(raw)
	return prefix + hex.EncodeToString(sum[:16])
}

func imageModelAdapter(p config.Provider, id string, m config.Model) (string, bool) {
	if m.ImageAdapter == "disabled" {
		return "", false
	}
	// "enabled" is the user's explicit opt-in; the protocol still follows the provider API.
	if m.ImageAdapter != "" && m.ImageAdapter != imageAdapterEnabled {
		return m.ImageAdapter, true
	}
	known := direct.ImageModelAdapter(id)
	if m.ImageAdapter != imageAdapterEnabled {
		// Explicit output metadata wins over name heuristics. Vision is input-only.
		if len(m.OutputModalities) > 0 && !slices.Contains(m.OutputModalities, "image") {
			return "", false
		}
		// Default guess, editable per model in provider settings.
		if known == "" && !slices.Contains(m.OutputModalities, "image") && !strings.Contains(strings.ToLower(id), "image") {
			return "", false
		}
	}
	// These known image APIs require different request/response contracts.
	if strings.HasPrefix(id, "dall-e-") || strings.HasPrefix(id, "imagen-") {
		return "", true
	}
	switch strings.TrimSpace(p.API) {
	case "", "openai-completions", "openai-responses":
		// Never guess a Google proxy contract behind an OpenAI-compatible endpoint.
		if known != direct.GoogleID {
			return direct.OpenAIID, true
		}
	case "google-generative-ai", "google-gemini":
		if known != direct.OpenAIID {
			return direct.GoogleID, true
		}
	}
	return "", true // Identified as an image model, but its protocol is unsupported.
}

const imageAdapterEnabled = "enabled"

func providerImageBaseURL(p config.Provider, adapter string) (string, bool) {
	u, err := url.Parse(p.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" || strings.Contains(u.Path, "..") {
		return "", false
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")) {
		return "", false
	}
	path := strings.TrimRight(u.Path, "/")
	if p.IsFullURL {
		matched := false
		if adapter == direct.GoogleID {
			if i := strings.LastIndex(path, "/models/"); i >= 0 && (strings.HasSuffix(path, ":generateContent") || strings.HasSuffix(path, ":streamGenerateContent")) {
				path, matched = path[:i], true
			}
		} else {
			for _, suffix := range []string{"/chat/completions", "/responses", "/completions", "/images/generations"} {
				if strings.HasSuffix(path, suffix) {
					path, matched = strings.TrimSuffix(path, suffix), true
					break
				}
			}
		}
		if !matched {
			return "", false
		}
	}
	if path == "" {
		if adapter == direct.GoogleID {
			path = "/v1beta"
		} else {
			path = "/v1"
		}
	}
	u.Path = path
	return u.String(), true
}

func providerImageCapabilities(adapter string) imaging.TargetCapabilities {
	return direct.ProtocolProfile(adapter).Capabilities
}

func (m *ImageSettingsManager) providerImageTargetsLocked() ([]imaging.ImageTarget, []imaging.ImageConnection) {
	targets := []imaging.ImageTarget{}
	connections := []imaging.ImageConnection{}
	providerIDs := make([]string, 0, len(m.store.cfg.Providers))
	for id := range m.store.cfg.Providers {
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)
	for _, pid := range providerIDs {
		p := m.store.cfg.Providers[pid]
		headers := map[string]string{}
		for _, header := range p.CustomHeaders {
			headers[header.Key] = header.Value
		}
		headersValid := direct.ValidateCustomHeaders(headers) == nil
		models := map[string]config.Model{}
		for mid, model := range m.store.cfg.Models {
			if slices.Contains(model.Providers, pid) {
				models[mid] = model.ForProvider(pid)
			}
		}
		mids := make([]string, 0, len(models))
		for mid := range models {
			mids = append(mids, mid)
		}
		sort.Strings(mids)
		seenConnections := map[string]bool{}
		for _, mid := range mids {
			if !p.ModelActive(mid) {
				continue
			}
			model := models[mid]
			adapter, image := imageModelAdapter(p, mid, model)
			if !image {
				continue
			}
			name := model.DisplayName
			if name == "" {
				name = model.Name
			}
			if name == "" {
				name = mid
			}
			providerName := strings.TrimSpace(p.Name)
			if providerName == "" {
				providerName = pid
			}
			name = providerName + " · " + name
			connectionID := providerImageID("ic_provider_", pid, adapter)
			target := imaging.ImageTarget{Source: "provider", ProviderID: pid, ModelID: mid, ID: providerImageID("it_provider_", pid, mid), Name: name, ConnectionID: connectionID, Adapter: adapter, AdapterVersion: direct.Version, Kind: "model", RemoteID: mid, Enabled: true, Capabilities: providerImageCapabilities(adapter), Parameters: direct.ModelParameters(adapter, mid), VerificationState: &imaging.VerificationSummary{State: "provider_configured"}}
			if adapter == direct.OpenAIID {
				base, _ := providerImageBaseURL(p, adapter)
				switch direct.ProfileForBaseURL(base) {
				case "agnes":
					// One JSON generations route with inputs as data URIs; no mask support.
					target.Parameters = direct.AgnesParameters()
					target.Capabilities.Operations = []imaging.Operation{imaging.OperationGenerate, imaging.OperationReference, imaging.OperationEdit}
					target.Capabilities.OutputFormats = []string{"png"}
				case "rolldek":
					// RollDek takes no tuning parameters. Edits and reference images go through
					// the standard multipart /images/edits route; masks stay off until verified.
					target.Parameters = []imaging.ParameterSpec{}
					target.Capabilities.Operations = []imaging.Operation{imaging.OperationGenerate, imaging.OperationReference, imaging.OperationEdit}
					target.Capabilities.OutputFormats = []string{"png"}
				}
			}
			switch {
			case adapter != direct.OpenAIID && adapter != direct.GoogleID:
				target.DisabledReason = "unsupported_image_protocol"
			case !direct.ValidImageModelID(mid):
				target.DisabledReason = "unsupported_image_model"
			case p.APIKey == "":
				target.DisabledReason = "auth_required"
			case !headersValid:
				target.DisabledReason = "unsupported_provider_headers"
			default:
				if _, ok := providerImageBaseURL(p, adapter); !ok {
					target.DisabledReason = "unsupported_provider_endpoint"
				}
			}
			if target.DisabledReason != "" {
				target.Enabled = false
				target.VerificationState = &imaging.VerificationSummary{State: "blocked", Reason: target.DisabledReason}
			}
			// Deterministic across restarts, changes with the execution binding. Only the
			// truncated digest is public; credentials and endpoint/header values stay private.
			raw, _ := json.Marshal(struct {
				Provider imageProviderBinding
				Model    config.Model
				Target   imaging.TargetSnapshot
			}{providerImageBinding(p), model.Metadata(), imaging.SnapshotTarget(target)})
			digest := sha256.Sum256(raw)
			target.Revision = binary.BigEndian.Uint64(digest[:8]) & imaging.MaxSafeInteger
			if target.Revision == 0 {
				target.Revision = 1
			}
			targets = append(targets, target)
			if !seenConnections[connectionID] {
				seenConnections[connectionID] = true
				connections = append(connections, imaging.ImageConnection{ID: connectionID, Name: p.Name, Enabled: true, MaxConcurrent: 1, CredentialConfigured: p.APIKey != "", Binding: imaging.ConnectionBinding{Kind: "provider_ref", ProviderID: pid}})
			}
		}
	}
	return targets, connections
}

// ListImageTargets merges legacy targets with provider-derived targets without
// persisting derived entries or exposing provider secrets/endpoints/headers.
func (m *ImageSettingsManager) ListImageTargets(scope imaging.Scope) ([]imaging.ImageTarget, error) {
	if err := checkImageScope(scope); err != nil {
		return nil, err
	}
	m.store.mu.RLock()
	defer m.store.mu.RUnlock()
	targets := m.publicLocked(scope).Targets
	derived, _ := m.providerImageTargetsLocked()
	for _, target := range derived {
		targets = append(targets, imaging.PublicTarget(target))
	}
	return targets, nil
}

// ResolveImageTargetConnection binds the execution credential to the same current
// provider target checked at resolution, and checks again immediately before send.
func (m *ImageSettingsManager) ResolveImageTargetConnection(ctx context.Context, scope imaging.Scope, target imaging.TargetSnapshot) (ResolvedImageConnection, error) {
	if target.Source == "provider" {
		if err := m.CheckProviderImageTarget(ctx, scope, target); err != nil {
			return ResolvedImageConnection{}, err
		}
	}
	connection, err := m.ResolveImageConnection(ctx, scope, target.ConnectionID)
	if err != nil {
		return ResolvedImageConnection{}, err
	}
	if target.Source == "provider" {
		if err := m.CheckProviderImageTarget(ctx, scope, target); err != nil {
			connection.HTTPClient.CloseIdleConnections()
			return ResolvedImageConnection{}, err
		}
		resolver, ok := connection.SecretResolver.(*imageProviderSecretResolver)
		if !ok {
			connection.HTTPClient.CloseIdleConnections()
			return ResolvedImageConnection{}, imageError(imaging.CodeInvalidInput)
		}
		resolver.target = &target
	}
	return connection, nil
}

// imageProviderBinding is the part of a provider that changes how an image request
// executes. Chat-only settings (active/ordered models, caching, retries, usage
// queries) are excluded so editing them never fails queued image jobs.
type imageProviderBinding struct {
	Type           string
	API            string
	BaseURL        string
	IsFullURL      bool
	APIKey         string
	CustomHeaders  []config.CustomHeader
	UseSystemProxy bool
}

func providerImageBinding(p config.Provider) imageProviderBinding {
	return imageProviderBinding{Type: p.Type, API: p.API, BaseURL: p.BaseURL, IsFullURL: p.IsFullURL, APIKey: p.APIKey, CustomHeaders: p.CustomHeaders, UseSystemProxy: p.UseSystemProxy}
}

// CheckProviderImageTarget fences queued/recovered execution against config changes.
func (m *ImageSettingsManager) CheckProviderImageTarget(ctx context.Context, scope imaging.Scope, target imaging.TargetSnapshot) error {
	if err := checkImageScope(scope); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return imageError(imaging.CodeUnavailable)
	}
	m.store.mu.RLock()
	defer m.store.mu.RUnlock()
	targets, _ := m.providerImageTargetsLocked()
	for _, current := range targets {
		if current.ID == target.ID && current.Enabled && reflect.DeepEqual(imaging.SnapshotTarget(current), target) {
			return nil
		}
	}
	return imageError(imaging.CodeRevisionConflict)
}
