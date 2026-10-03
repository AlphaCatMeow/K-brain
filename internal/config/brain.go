package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/datapath"
)

type PromptFile struct {
	Scope string
	Path  string
	Text  string
}

func BrainPath() string {
	dir, err := Dir()
	if err != nil {
		return ""
	}
	path := filepath.Join(dir, "brain.md")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return ""
		}
	}
	return path
}

func BrainInstructions() string {
	path := BrainPath()
	if path == "" {
		return ""
	}
	return readPromptFile(path)
}

func readPromptFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var lines []string
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// hasProjectMarker reports whether dir is a project root, which ends the ancestor walk.
func hasProjectMarker(dir string) bool {
	for _, marker := range []string{".git", ".liveagent"} {
		if info, err := os.Stat(filepath.Join(dir, marker)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

func projectPromptCandidates(wd string) []string {
	wd, err := filepath.Abs(wd)
	if err != nil {
		return nil
	}
	info, err := os.Stat(wd)
	if err == nil && !info.IsDir() {
		wd = filepath.Dir(wd)
	}
	var dirs []string
	for {
		dirs = append(dirs, wd)
		// Include the project root itself, then stop before walking into its parent.
		if hasProjectMarker(wd) {
			break
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			break
		}
		wd = parent
	}
	var paths []string
	userBrain := ""
	if dir, err := Dir(); err == nil {
		userBrain = filepath.Clean(filepath.Join(dir, "brain.md"))
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		dir := dirs[i]
		datapath.Report(datapath.MigrateProjectDir(dir))
		for _, candidate := range []string{
			filepath.Join(dir, "AGENTS.md"),
			filepath.Join(ProjectDir(dir), "brain.md"),
		} {
			if filepath.Clean(candidate) == userBrain {
				continue
			}
			if _, err := os.Stat(candidate); err == nil {
				paths = append(paths, candidate)
			}
		}
	}
	return paths
}

func ProjectPromptFiles(wd string) []PromptFile {
	var out []PromptFile
	seen := map[string]bool{}
	for _, path := range projectPromptCandidates(wd) {
		path, err := filepath.Abs(path)
		if err != nil || seen[path] {
			continue
		}
		seen[path] = true
		text := readPromptFile(path)
		if text == "" {
			continue
		}
		scope := "project"
		if filepath.Base(path) == "AGENTS.md" {
			scope = "project AGENTS.md"
		}
		out = append(out, PromptFile{Scope: scope, Path: path, Text: text})
	}
	return out
}
