// Package resources owns LiveAgent-compatible prompt resources.
package resources

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

const MaxPromptSize = 1 << 20

var (
	ErrConflict = errors.New("prompt revision conflict")
	ErrNotFound = errors.New("prompt template not found")
	identifier  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
)

type AgentTemplate struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	Enabled     bool   `json:"enabled"`
}

type ProjectPrompt struct {
	Workdir  string `json:"workdir"`
	Prompt   string `json:"prompt"`
	Strategy string `json:"strategy"`
}

type PromptState struct {
	Revision  uint64                   `json:"revision"`
	Templates []AgentTemplate          `json:"templates"`
	Projects  map[string]ProjectPrompt `json:"-"`
}

type diskState struct {
	Revision  uint64          `json:"revision"`
	Templates []AgentTemplate `json:"templates"`
}

type projectDiskState struct {
	Revision uint64                   `json:"revision"`
	Projects map[string]ProjectPrompt `json:"projects"`
}

// PromptStore is independent of provider settings and directory instruction files.
type PromptStore struct {
	mu    sync.Mutex
	root  string
	state PromptState
}

func OpenPrompts(root string) (*PromptStore, error) {
	if root == "" {
		return nil, errors.New("prompt root is required")
	}
	s := &PromptStore{root: root, state: PromptState{Templates: []AgentTemplate{}, Projects: map[string]ProjectPrompt{}}}
	if data, err := os.ReadFile(filepath.Join(root, "agents.json")); err == nil {
		var disk diskState
		if strings.HasPrefix(strings.TrimSpace(string(data)), "[") {
			if err = json.Unmarshal(data, &s.state.Templates); err != nil {
				return nil, fmt.Errorf("read agents.json: %w", err)
			}
		} else if err = json.Unmarshal(data, &disk); err != nil {
			return nil, fmt.Errorf("read agents.json: %w", err)
		} else {
			s.state.Revision, s.state.Templates = disk.Revision, disk.Templates
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if data, err := os.ReadFile(filepath.Join(root, "project-prompts.json")); err == nil {
		var disk projectDiskState
		if err := json.Unmarshal(data, &disk); err != nil {
			return nil, fmt.Errorf("read project-prompts.json: %w", err)
		}
		if disk.Revision > s.state.Revision {
			s.state.Revision = disk.Revision
		}
		s.state.Projects = disk.Projects
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if s.state.Projects == nil {
		s.state.Projects = map[string]ProjectPrompt{}
	}
	if s.state.Templates == nil {
		s.state.Templates = []AgentTemplate{}
	}
	if err := validateTemplates(s.state.Templates); err != nil {
		return nil, err
	}
	for key, project := range s.state.Projects {
		workdir := filepath.Clean(project.Workdir)
		if !filepath.IsAbs(workdir) || key != config.ProjectID(workdir) {
			return nil, errors.New("invalid stored project prompt identity")
		}
		if err := validateProject(project); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func CanonicalWorkdir(workdir string) (string, error) {
	if !filepath.IsAbs(workdir) {
		return "", errors.New("workdir must be absolute")
	}
	path, err := filepath.EvalSymlinks(filepath.Clean(workdir))
	if err != nil {
		return "", fmt.Errorf("workdir: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("workdir must be a directory")
	}
	return path, nil
}

func validateProject(project ProjectPrompt) error {
	if project.Strategy != "append" && project.Strategy != "replace" {
		return errors.New("strategy must be append or replace")
	}
	if len(project.Prompt) > MaxPromptSize {
		return errors.New("project prompt exceeds 1 MiB")
	}
	return nil
}

func validateTemplates(items []AgentTemplate) error {
	if len(items) > 256 {
		return errors.New("at most 256 agent templates are allowed")
	}
	seen := map[string]bool{}
	enabled := false
	for _, item := range items {
		if !identifier.MatchString(item.ID) || seen[item.ID] {
			return errors.New("agent template IDs must be unique portable identifiers")
		}
		seen[item.ID] = true
		if strings.TrimSpace(item.Name) == "" || len(item.Name) > 1024 || len(item.Description) > 16384 || len(item.Prompt) > MaxPromptSize {
			return errors.New("invalid agent template name or size")
		}
		if item.Enabled && enabled {
			return errors.New("at most one agent template may be enabled")
		}
		enabled = enabled || item.Enabled
	}
	return nil
}

func cloneState(in PromptState) PromptState {
	out := PromptState{Revision: in.Revision, Templates: append([]AgentTemplate{}, in.Templates...), Projects: map[string]ProjectPrompt{}}
	for key, project := range in.Projects {
		out.Projects[key] = project
	}
	return out
}

func (s *PromptStore) Snapshot() PromptState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneState(s.state)
}

func writeAtomic(path string, data []byte, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".prompts-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if _, err = file.Write(append(data, '\n')); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *PromptStore) persist(state PromptState, projectUpdate bool) error {
	var value any = diskState{Revision: state.Revision, Templates: state.Templates}
	filename := "agents.json"
	if projectUpdate {
		value = projectDiskState{Revision: state.Revision, Projects: state.Projects}
		filename = "project-prompts.json"
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.root, filename), data, s.root)
}

func (s *PromptStore) update(base *uint64, projectUpdate bool, change func(*PromptState) error) (PromptState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if base != nil && *base != s.state.Revision {
		return cloneState(s.state), ErrConflict
	}
	next := cloneState(s.state)
	if err := change(&next); err != nil {
		return cloneState(s.state), err
	}
	if err := validateTemplates(next.Templates); err != nil {
		return cloneState(s.state), err
	}
	sort.Slice(next.Templates, func(i, j int) bool { return next.Templates[i].ID < next.Templates[j].ID })
	next.Revision++
	if err := s.persist(next, projectUpdate); err != nil {
		return cloneState(s.state), err
	}
	s.state = next
	return cloneState(next), nil
}

func (s *PromptStore) Replace(items []AgentTemplate, base *uint64) (PromptState, error) {
	return s.update(base, false, func(next *PromptState) error {
		next.Templates = append([]AgentTemplate{}, items...)
		return nil
	})
}

func (s *PromptStore) Create(item AgentTemplate, base *uint64) (PromptState, error) {
	return s.update(base, false, func(next *PromptState) error {
		for i := range next.Templates {
			if next.Templates[i].ID == item.ID {
				return errors.New("agent template ID already exists")
			}
			if item.Enabled {
				next.Templates[i].Enabled = false
			}
		}
		next.Templates = append(next.Templates, item)
		return nil
	})
}

type TemplatePatch struct {
	Name         *string `json:"name,omitempty"`
	Description  *string `json:"description,omitempty"`
	Prompt       *string `json:"prompt,omitempty"`
	Enabled      *bool   `json:"enabled,omitempty"`
	BaseRevision *uint64 `json:"baseRevision,omitempty"`
}

func (s *PromptStore) Patch(id string, patch TemplatePatch) (PromptState, error) {
	return s.update(patch.BaseRevision, false, func(next *PromptState) error {
		found := false
		for i := range next.Templates {
			item := &next.Templates[i]
			if item.ID == id {
				found = true
				if patch.Name != nil {
					item.Name = *patch.Name
				}
				if patch.Description != nil {
					item.Description = *patch.Description
				}
				if patch.Prompt != nil {
					item.Prompt = *patch.Prompt
				}
				if patch.Enabled != nil {
					item.Enabled = *patch.Enabled
				}
			} else if patch.Enabled != nil && *patch.Enabled {
				item.Enabled = false
			}
		}
		if !found {
			return ErrNotFound
		}
		return nil
	})
}

func (s *PromptStore) Delete(id string, base *uint64) (PromptState, error) {
	return s.update(base, false, func(next *PromptState) error {
		for i, item := range next.Templates {
			if item.ID == id {
				next.Templates = append(next.Templates[:i], next.Templates[i+1:]...)
				return nil
			}
		}
		return ErrNotFound
	})
}

func (s *PromptStore) SetProject(project ProjectPrompt, base *uint64) (PromptState, error) {
	workdir, err := CanonicalWorkdir(project.Workdir)
	if err != nil {
		return PromptState{}, err
	}
	project.Workdir = workdir
	if project.Strategy == "" {
		project.Strategy = "append"
	}
	if err := validateProject(project); err != nil {
		return PromptState{}, err
	}
	return s.update(base, true, func(next *PromptState) error {
		next.Projects[config.ProjectID(workdir)] = project
		return nil
	})
}

// Resolve replaces only the global template layer, never system/brain/AGENTS rules.
func (s *PromptStore) Resolve(workdir string, override *ProjectPrompt) (global string, project ProjectPrompt, effective string, err error) {
	state := s.Snapshot()
	project = ProjectPrompt{Strategy: "append"}
	for _, item := range state.Templates {
		if item.Enabled {
			global = strings.TrimSpace(item.Prompt)
			break
		}
	}
	if workdir != "" {
		wd, resolveErr := CanonicalWorkdir(workdir)
		if resolveErr != nil {
			return "", project, "", resolveErr
		}
		project.Workdir = wd
		if stored, ok := state.Projects[config.ProjectID(wd)]; ok {
			project = stored
		}
	}
	if override != nil {
		project.Prompt, project.Strategy = override.Prompt, override.Strategy
		if project.Strategy == "" {
			project.Strategy = "append"
		}
		if err := validateProject(project); err != nil {
			return "", project, "", err
		}
	}
	effective = global
	if text := strings.TrimSpace(project.Prompt); text != "" {
		if project.Strategy == "replace" || global == "" {
			effective = text
		} else {
			effective = global + "\n\n" + text
		}
	}
	return global, project, effective, nil
}
