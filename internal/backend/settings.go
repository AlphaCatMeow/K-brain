package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/Stack-Cairn/K-brain/internal/agent"
	"github.com/Stack-Cairn/K-brain/internal/ai"
	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
)

// SettingsStore publishes immutable snapshots only after persistence succeeds.
type SettingsStore struct {
	revision uint64
	mu       sync.RWMutex
	cfg      *config.Config
	save     func(*config.Config) error
}

func NewSettingsStore(cfg *config.Config, save func(*config.Config) error) *SettingsStore {
	return &SettingsStore{cfg: cfg.Snapshot(), save: save, revision: 1}
}

func (s *SettingsStore) Revision() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision
}

func (s *SettingsStore) Snapshot() *config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Snapshot()
}

type settingsProvider struct {
	ID               string          `json:"id"`
	Name             string          `json:"name"`
	API              string          `json:"api"`
	BaseURL          string          `json:"baseUrl"`
	APIKeyConfigured bool            `json:"apiKeyConfigured"`
	Models           []settingsModel `json:"models"`
}

type settingsModel struct {
	Provider        string `json:"provider"`
	ID              string `json:"id"`
	Name            string `json:"name,omitempty"`
	ContextWindow   int    `json:"contextWindow,omitempty"`
	MaxOutputTokens int    `json:"maxOutputTokens,omitempty"`
	Vision          bool   `json:"vision,omitempty"`
}

type settingsProjection struct {
	Version         string             `json:"version"`
	Mode            string             `json:"mode"`
	DefaultModel    string             `json:"defaultModel"`
	DefaultProvider string             `json:"defaultProvider"`
	Providers       []settingsProvider `json:"providers"`
	Models          []settingsModel    `json:"models"`
}

type settingsModelUpdate struct {
	Provider        string  `json:"provider,omitempty"`
	ID              string  `json:"id"`
	Name            *string `json:"name,omitempty"`
	ContextWindow   *int    `json:"contextWindow,omitempty"`
	MaxOutputTokens *int    `json:"maxOutputTokens,omitempty"`
	Vision          *bool   `json:"vision,omitempty"`
}

type settingsProviderUpdate struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	API         string                `json:"api"`
	BaseURL     string                `json:"baseUrl"`
	APIKey      *string               `json:"apiKey,omitempty"`
	ClearAPIKey bool                  `json:"clearApiKey,omitempty"`
	Models      []settingsModelUpdate `json:"models"`
}

type settingsUpdate struct {
	DefaultModel    *string                  `json:"defaultModel,omitempty"`
	DefaultProvider *string                  `json:"defaultProvider,omitempty"`
	Providers       []settingsProviderUpdate `json:"providers,omitempty"`
	DeleteProviders []string                 `json:"deleteProviders,omitempty"`
}

func publicBaseURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return ""
	}
	u.User = nil
	u.RawQuery = ""
	u.ForceQuery = false
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

func projectSettings(cfg *config.Config) settingsProjection {
	out := settingsProjection{Version: protocol.Version, Mode: "kbrain", DefaultModel: cfg.DefaultModel, DefaultProvider: cfg.DefaultProvider, Providers: []settingsProvider{}, Models: []settingsModel{}}
	for id, p := range cfg.Providers {
		providerView := settingsProvider{ID: id, Name: p.Name, API: p.API, BaseURL: publicBaseURL(p.BaseURL), APIKeyConfigured: p.APIKey != "", Models: []settingsModel{}}
		for modelID, m := range cfg.Models {
			if !slices.Contains(m.Providers, id) {
				continue
			}
			modelView := settingsModel{Provider: id, ID: modelID, Name: m.Name, ContextWindow: m.Context, MaxOutputTokens: m.MaxOut, Vision: m.Vision}
			providerView.Models = append(providerView.Models, modelView)
			out.Models = append(out.Models, modelView)
		}
		out.Providers = append(out.Providers, providerView)
	}
	sort.Slice(out.Providers, func(i, j int) bool { return out.Providers[i].ID < out.Providers[j].ID })
	sort.Slice(out.Models, func(i, j int) bool {
		if out.Models[i].Provider == out.Models[j].Provider {
			return out.Models[i].ID < out.Models[j].ID
		}
		return out.Models[i].Provider < out.Models[j].Provider
	})
	return out
}

func (s *Server) handleModels(w http.ResponseWriter) {
	if s.settings == nil {
		writeJSON(w, http.StatusOK, map[string]any{"version": protocol.Version, "models": s.models})
		return
	}
	projection := projectSettings(s.settings.Snapshot())
	models := make([]map[string]any, 0, len(projection.Models))
	for _, m := range projection.Models {
		models = append(models, map[string]any{"provider": m.Provider, "model": m.ID, "name": m.Name, "contextWindow": m.ContextWindow, "maxOutputTokens": m.MaxOutputTokens, "vision": m.Vision})
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": protocol.Version, "models": models, "defaultModel": protocol.ModelRef{Provider: projection.DefaultProvider, Model: projection.DefaultModel}})
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if s.settings == nil {
		writeJSONError(w, http.StatusNotImplemented, "backend settings are unavailable")
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, projectSettings(s.settings.Snapshot()))
		return
	}
	if r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var update settingsUpdate
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&update); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid settings request")
		return
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeJSONError(w, http.StatusBadRequest, "expected one settings object")
		return
	}
	s.settings.mu.Lock()
	defer s.settings.mu.Unlock()
	next := s.settings.cfg.Snapshot()
	if err := applySettings(next, update); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.settings.save == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "settings persistence is unavailable")
		return
	}
	if err := s.settings.save(next); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not persist settings")
		return
	}
	s.settings.cfg = next
	s.settings.revision++
	writeJSON(w, http.StatusOK, projectSettings(next))
}

func applySettings(cfg *config.Config, update settingsUpdate) error {
	if cfg.Providers == nil {
		cfg.Providers = map[string]config.Provider{}
	}
	if cfg.Models == nil {
		cfg.Models = map[string]config.Model{}
	}
	removeProviderModels := func(id string) {
		for modelID, m := range cfg.Models {
			m.Providers = slices.DeleteFunc(slices.Clone(m.Providers), func(p string) bool { return p == id })
			if len(m.Providers) == 0 {
				delete(cfg.Models, modelID)
			} else {
				cfg.Models[modelID] = m
			}
		}
	}
	for _, id := range update.DeleteProviders {
		delete(cfg.Providers, id)
		removeProviderModels(id)
	}
	seen := map[string]bool{}
	for _, input := range update.Providers {
		id := strings.TrimSpace(input.ID)
		if id == "" || seen[id] {
			return fmt.Errorf("provider IDs must be non-empty and unique")
		}
		seen[id] = true
		if input.API == "" || !ai.SupportedAPI(input.API) {
			return fmt.Errorf("unsupported provider API")
		}
		endpoint, err := url.Parse(strings.TrimSpace(input.BaseURL))
		if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
			return fmt.Errorf("base URL must be an HTTP(S) endpoint without credentials, query or fragment")
		}
		if input.ClearAPIKey && input.APIKey != nil {
			return fmt.Errorf("cannot replace and clear an API key together")
		}
		p := cfg.Providers[id]
		p.Name, p.API, p.BaseURL = strings.TrimSpace(input.Name), input.API, strings.TrimRight(endpoint.String(), "/")
		if input.ClearAPIKey {
			p.APIKey = ""
		}
		if input.APIKey != nil {
			key := strings.TrimSpace(*input.APIKey)
			if key == "" || strings.HasPrefix(key, "!") {
				return fmt.Errorf("API key must be non-empty and cannot execute a command")
			}
			p.APIKey = key
		}
		if input.Models != nil {
			removeProviderModels(id)
			modelIDs := map[string]bool{}
			for _, item := range input.Models {
				modelID := strings.TrimSpace(item.ID)
				if modelID == "" || modelIDs[modelID] {
					return fmt.Errorf("models require unique IDs")
				}
				modelIDs[modelID] = true
				m, exists := cfg.Models[modelID]
				if !exists {
					m.ID = modelID
				}
				if len(m.Providers) > 0 {
					if item.Name != nil && *item.Name != m.Name || item.ContextWindow != nil && *item.ContextWindow != m.Context || item.MaxOutputTokens != nil && *item.MaxOutputTokens != m.MaxOut || item.Vision != nil && *item.Vision != m.Vision {
						return fmt.Errorf("shared model IDs must use identical metadata across providers")
					}
				}
				if item.Name != nil {
					m.Name = *item.Name
				}
				if item.ContextWindow != nil {
					if *item.ContextWindow < 0 {
						return fmt.Errorf("context window must be non-negative")
					}
					m.Context = *item.ContextWindow
				}
				if item.MaxOutputTokens != nil {
					if *item.MaxOutputTokens < 0 {
						return fmt.Errorf("max output tokens must be non-negative")
					}
					m.MaxOut = *item.MaxOutputTokens
				}
				if item.Vision != nil {
					m.Vision = *item.Vision
				}
				m.ID, m.Providers = modelID, append(m.Providers, id)
				cfg.Models[modelID] = m
			}
		}
		cfg.Providers[id] = p
	}
	if update.DefaultModel != nil {
		cfg.DefaultModel = strings.TrimSpace(*update.DefaultModel)
	}
	if update.DefaultProvider != nil {
		cfg.DefaultProvider = strings.TrimSpace(*update.DefaultProvider)
	}
	if len(cfg.Models) == 0 {
		cfg.DefaultModel, cfg.DefaultProvider = "", ""
		return nil
	}
	m, ok := cfg.Models[cfg.DefaultModel]
	if !ok || (cfg.DefaultProvider != "" && !slices.Contains(m.Providers, cfg.DefaultProvider)) {
		projection := projectSettings(cfg)
		cfg.DefaultModel, cfg.DefaultProvider = projection.Models[0].ID, projection.Models[0].Provider
	} else if cfg.DefaultProvider == "" {
		cfg.DefaultProvider = m.Providers[0]
	}
	return nil
}

// Refresh only at the next idle run boundary; SetModel preserves tasks and history.
func (s *Server) refreshSettingsLocked(rt *runtimeSession) error {
	if s.settings == nil {
		return nil
	}
	revision := s.settings.Revision()
	cfg := s.settings.Snapshot()
	m, ok := cfg.Models[rt.agent.ModelName]
	if !ok || !slices.Contains(m.Providers, rt.agent.Provider) {
		return fmt.Errorf("selected model is no longer configured")
	}
	if _, ok := cfg.Providers[rt.agent.Provider]; !ok {
		return fmt.Errorf("selected provider is no longer configured")
	}
	if rt.settingsRevision == revision {
		return nil
	}
	fresh, err := s.factory(context.Background(), rt.agent.WorkingDir, protocol.ModelRef{Provider: rt.agent.Provider, Model: rt.agent.ModelName})
	if err != nil {
		return fmt.Errorf("could not refresh model configuration")
	}
	err = rt.agent.SetModel(agent.ModelConfig{Client: fresh.Client, ID: fresh.Model, Name: rt.agent.ModelName, Provider: rt.agent.Provider, MaxTokens: fresh.MaxTokens, ContextLimit: fresh.ContextLimit, Vision: fresh.Vision, Temperature: fresh.Temperature, TopP: fresh.TopP})
	if err != nil {
		return err
	}
	rt.agent.TaskDefault = fresh.TaskDefault
	rt.agent.CompactClient, rt.agent.CompactModel, rt.agent.CompactProvider = fresh.CompactClient, fresh.CompactModel, fresh.CompactProvider
	rt.settingsRevision = revision
	return nil
}
