package backend

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/config"
	"github.com/Stack-Cairn/K-brain/internal/prompts/templates"
	"github.com/Stack-Cairn/K-brain/internal/protocol"
	"github.com/Stack-Cairn/K-brain/internal/resources"
)

type promptTemplateInput struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	Enabled     bool   `json:"enabled"`
}

type promptProjectInput struct {
	Workdir  string `json:"workdir"`
	Prompt   string `json:"prompt"`
	Strategy string `json:"strategy"`
}

type promptTemplateReplaceInput struct {
	Templates    []promptTemplateInput `json:"templates"`
	BaseRevision *uint64               `json:"baseRevision,omitempty"`
}

type markdownExpandInput struct {
	Args []string `json:"args,omitempty"`
	Text string   `json:"text,omitempty"`
}

func (s *Server) handlePrompts(w http.ResponseWriter, r *http.Request) {
	if s.prompts == nil {
		writeJSONError(w, http.StatusNotImplemented, "prompt resources are unavailable")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/prompts"), "/")
	if path == "" {
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.getPrompts(w, r)
		return
	}
	parts := strings.Split(path, "/")
	switch {
	case parts[0] == "templates" && len(parts) == 1:
		if r.Method == http.MethodPut {
			s.replacePromptTemplates(w, r)
			return
		}
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, map[string]any{"version": protocol.Version, "revision": s.prompts.Snapshot().Revision, "templates": s.markdownTemplates(r.URL.Query().Get("workdir"))})
			return
		}
		s.handlePromptTemplates(w, r)
	case parts[0] == "templates" && len(parts) == 2:
		s.handlePromptTemplate(w, r, parts[1])
	case parts[0] == "templates" && len(parts) == 3 && parts[2] == "expand":
		s.expandMarkdownTemplate(w, r, parts[1])
	case parts[0] == "project" && len(parts) == 1:
		s.handleProjectPrompt(w, r)
	default:
		writeJSONError(w, http.StatusNotFound, "prompt route not found")
	}
}

func (s *Server) getPrompts(w http.ResponseWriter, r *http.Request) {
	state := s.prompts.Snapshot()
	workdir := strings.TrimSpace(r.URL.Query().Get("workdir"))
	projectPrompts := make(map[string]resources.ProjectPrompt, len(state.Projects))
	for path, project := range state.Projects {
		projectPrompts[path] = project
	}
	global, project, effective, err := s.prompts.Resolve(workdir, nil)
	if err != nil && workdir != "" {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version": protocol.Version, "revision": state.Revision,
		"globalTemplates": state.Templates, "globalPrompt": global,
		"projectPrompt": project.Prompt, "projectPromptStrategy": project.Strategy,
		"projectPrompts":  projectPrompts,
		"effectivePrompt": effective, "files": s.markdownTemplates(workdir),
	})
}

func (s *Server) replacePromptTemplates(w http.ResponseWriter, r *http.Request) {
	var in promptTemplateReplaceInput
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	items := make([]resources.AgentTemplate, 0, len(in.Templates))
	for _, item := range in.Templates {
		items = append(items, resources.AgentTemplate{ID: item.ID, Name: item.Name, Description: item.Description, Prompt: item.Prompt, Enabled: item.Enabled})
	}
	state, err := s.prompts.Replace(items, in.BaseRevision)
	if err != nil {
		s.promptError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": protocol.Version, "revision": state.Revision, "templates": state.Templates})
}

func (s *Server) handlePromptTemplates(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in promptTemplateInput
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	state, err := s.prompts.Create(resources.AgentTemplate{ID: in.ID, Name: in.Name, Description: in.Description, Prompt: in.Prompt, Enabled: in.Enabled}, nil)
	if err != nil {
		s.promptError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"version": protocol.Version, "revision": state.Revision, "templates": state.Templates})
}

func (s *Server) handlePromptTemplate(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method == http.MethodDelete {
		state, err := s.prompts.Delete(id, nil)
		if err != nil {
			s.promptError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"version": protocol.Version, "revision": state.Revision, "templates": state.Templates})
		return
	}
	if r.Method != http.MethodPatch {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in resources.TemplatePatch
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	state, err := s.prompts.Patch(id, in)
	if err != nil {
		s.promptError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": protocol.Version, "revision": state.Revision, "templates": state.Templates})
}

func (s *Server) handleProjectPrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in promptProjectInput
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	state, err := s.prompts.SetProject(resources.ProjectPrompt{Workdir: in.Workdir, Prompt: in.Prompt, Strategy: in.Strategy}, nil)
	if err != nil {
		s.promptError(w, err)
		return
	}
	strategy := in.Strategy
	if strategy == "" {
		strategy = "append"
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": protocol.Version, "revision": state.Revision, "prompt": in.Prompt, "strategy": strategy})
}

func (s *Server) expandMarkdownTemplate(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in markdownExpandInput
	if err := decodeJSON(w, r, &in); err != nil {
		return
	}
	workdir := strings.TrimSpace(r.URL.Query().Get("workdir"))
	root, err := config.Dir()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dirs := []string{filepath.Join(root, "prompts")}
	if canonical, canonicalErr := resources.CanonicalWorkdir(workdir); canonicalErr == nil && config.Trusted(canonical) {
		if err := config.MigrateProjectDir(canonical); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		dirs = append(dirs, filepath.Join(config.ProjectDir(canonical), "prompts"))
	}
	items, _ := templates.Load(dirs...)
	for _, item := range items {
		if item.Name != name {
			continue
		}
		var expanded string
		var expandErr error
		if len(in.Args) > 0 {
			expanded, expandErr = item.ExpandArgs(in.Args)
		} else {
			expanded, expandErr = item.Expand(in.Text)
		}
		if expandErr != nil {
			writeJSONError(w, http.StatusUnprocessableEntity, expandErr.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"version": protocol.Version, "name": name, "text": expanded})
		return
	}
	writeJSONError(w, http.StatusNotFound, "prompt template not found")
}

func (s *Server) markdownTemplates(workdir string) []map[string]any {
	root, err := config.Dir()
	if err != nil {
		return []map[string]any{}
	}
	dirs := []string{filepath.Join(root, "prompts")}
	if workdir != "" {
		if canonical, canonicalErr := resources.CanonicalWorkdir(workdir); canonicalErr == nil && config.Trusted(canonical) {
			if err := config.MigrateProjectDir(canonical); err != nil {
				return []map[string]any{}
			}
			dirs = append(dirs, filepath.Join(config.ProjectDir(canonical), "prompts"))
		}
	}
	items, _ := templates.Load(dirs...)
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{"name": item.Name, "description": item.Description, "argumentHint": item.ArgumentHint, "path": item.Path})
	}
	return out
}

func (s *Server) promptError(w http.ResponseWriter, err error) {
	status := http.StatusUnprocessableEntity
	if errors.Is(err, resources.ErrConflict) {
		status = http.StatusConflict
	}
	if errors.Is(err, resources.ErrNotFound) {
		status = http.StatusNotFound
	}
	writeJSONError(w, status, err.Error())
}
