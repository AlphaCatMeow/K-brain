package memory

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Store) DeleteProject(workdir, actor, reason string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hash, err := ProjectHash(workdir)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(s.root, "projects", hash)
	if err := s.safe(dir); err != nil {
		return nil, err
	}
	entries, err := s.load()
	if err != nil {
		return nil, err
	}
	count := 0
	state, err := s.state()
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.Scope == "project" && e.WorkdirHash == hash {
			count++
			if reason != "" {
				state.Rejections = append(state.Rejections, Rejection{Slug: e.Slug, Scope: e.Scope, WorkdirHash: hash, RejectedAt: time.Now().UnixMilli(), Actor: actor, Reason: reason})
			}
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if reason != "" {
		if err := s.saveState(state); err != nil {
			return nil, err
		}
	}
	return map[string]any{"workdirHash": hash, "deletedCount": count, "quarantinePath": nil}, nil
}

func (s *Store) PathsInfo() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.load()
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": s.root, "isFresh": len(entries) == 0, "isInCloud": false, "cloudProvider": nil}, nil
}

func (s *Store) TodayLocalDate() string { return time.Now().Format("2006-01-02") }

func (s *Store) TodayDaily() (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.load()
	if err != nil {
		return nil, err
	}
	e, err := findEntry(entries, ReadArgs{Slug: "daily-" + s.TodayLocalDate(), Scope: "global"})
	if err != nil {
		return nil, err
	}
	return readResult(e, ReadArgs{})
}

func (s *Store) WipeAll() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.load()
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if err := os.Remove(e.path); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	for _, dir := range []string{filepath.Join(s.root, "global", "user"), filepath.Join(s.root, "global", "daily"), filepath.Join(s.root, "projects")} {
		_ = os.RemoveAll(dir)
	}
	state, stateErr := s.state()
	if stateErr != nil {
		return nil, stateErr
	}
	state.Runs = nil
	state.OrganizerEpoch++
	if err := s.saveState(state); err != nil {
		return nil, err
	}
	return map[string]any{"root": s.root, "isFresh": true, "isInCloud": false, "cloudProvider": nil, "organizerEpoch": state.OrganizerEpoch}, nil
}

func trimCommand(s string) string { return strings.TrimSpace(s) }
