package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
)

// Project memory lives under projects/<id>/, where id is the first 8 bytes of
// sha256(resolved workdir). LiveAgent desktop builds before the K-brain backend
// (<= 1.3.x, Rust memory store) hashed the output of fs::canonicalize, which on
// Windows is a verbatim path (\\?\C:\... or \\?\UNC\server\share\...), and fell
// back to the raw workdir string when canonicalize failed. Their project ids
// therefore never match ProjectHash on Windows. This file recognizes those
// legacy ids, keeps them addressable, and folds legacy directories into the
// current id so one project never splits across two directories.

// isWindows is a variable so tests can exercise the Windows legacy form anywhere.
var isWindows = runtime.GOOS == "windows"

var legacyMigrations sync.Map

func hashProjectPath(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:8])
}

func resolveProjectPath(workdir string) (string, error) {
	if strings.TrimSpace(workdir) == "" {
		return "", storeError("workdir_required", "project memory requires workdir")
	}
	p, err := filepath.Abs(workdir)
	if err != nil {
		return "", err
	}
	if real, e := filepath.EvalSymlinks(p); e == nil {
		p = real
	}
	return filepath.Clean(p), nil
}

// verbatimPath mirrors what Rust's fs::canonicalize returns on Windows.
func verbatimPath(resolved string) string {
	switch {
	case strings.HasPrefix(resolved, `\\?\`):
		return resolved
	case strings.HasPrefix(resolved, `\\`):
		return `\\?\UNC\` + resolved[2:]
	default:
		return `\\?\` + resolved
	}
}

// legacyHashesFor returns legacy ids for a workdir, excluding the current id.
func legacyHashesFor(raw, resolved string, windows bool) []string {
	current := hashProjectPath(resolved)
	candidates := []string{}
	if windows {
		candidates = append(candidates, verbatimPath(resolved))
	}
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		candidates = append(candidates, trimmed)
	}
	out := []string{}
	for _, c := range candidates {
		h := hashProjectPath(c)
		if h != current && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

func legacyProjectHashes(workdir string) []string {
	resolved, err := resolveProjectPath(workdir)
	if err != nil {
		return nil
	}
	return legacyHashesFor(workdir, resolved, isWindows)
}

func readWorkdirMarker(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ".workdir.json"))
	if err != nil {
		return ""
	}
	var m struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(data, &m) != nil {
		return ""
	}
	return m.Path
}

// logicalProjectHash maps a projects/<dir> name to the project id callers use.
// A recognized legacy directory reports the current id of its recorded workdir;
// anything else keeps its directory name.
func logicalProjectHash(dir, workdir string) string {
	if workdir == "" {
		return dir
	}
	current, err := ProjectHash(workdir)
	if err != nil || current == dir {
		return dir
	}
	if slices.Contains(legacyProjectHashes(workdir), dir) {
		return current
	}
	return dir
}

func isProjectID(name string) bool {
	if len(name) != 16 {
		return false
	}
	_, err := hex.DecodeString(name)
	return err == nil
}

// migrateLegacyProjectsOnce folds legacy project directories once per store root
// and process. Failures are logged and never block opening the store: legacy
// directories stay readable through logicalProjectHash and are moved on write.
func (s *Store) migrateLegacyProjectsOnce() {
	if _, done := legacyMigrations.LoadOrStore(s.root, true); done {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.migrateLegacyProjects(); err != nil {
		log.Printf("memory: legacy project migration: %v", err)
	}
}

func (s *Store) migrateLegacyProjects() error {
	projects := filepath.Join(s.root, "projects")
	if err := s.safe(projects); err != nil {
		return err
	}
	items, err := os.ReadDir(projects)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	remapped := map[string]string{}
	var errs []string
	for _, item := range items {
		name := item.Name()
		if !item.IsDir() || !isProjectID(name) {
			continue
		}
		legacy := filepath.Join(projects, name)
		workdir := readWorkdirMarker(legacy)
		current := logicalProjectHash(name, workdir)
		if current == name {
			continue
		}
		if err := s.foldProjectDir(legacy, filepath.Join(projects, current), name); err != nil {
			errs = append(errs, fmt.Sprintf("%s -> %s: %v", name, current, err))
			continue
		}
		remapped[name] = current
	}
	if len(remapped) > 0 {
		if err := s.remapRejections(remapped); err != nil {
			errs = append(errs, "rejections: "+err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func (s *Store) foldProjectDir(legacy, target, legacyName string) error {
	if err := s.safe(legacy); err != nil {
		return err
	}
	if err := s.safe(target); err != nil {
		return err
	}
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		return os.Rename(legacy, target)
	} else if err != nil {
		return err
	}
	conflicts := filepath.Join(target, ".legacy-"+legacyName)
	err := filepath.WalkDir(legacy, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return storeError("invalid_path", "memory symlinks are not allowed")
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(legacy, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(target, rel)
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			return moveFile(path, dst)
		} else if err != nil {
			return err
		}
		// Both directories hold this file. For memory entries the newer one stays
		// live; the other is preserved, out of the index, under .legacy-<id>/.
		if filepath.Ext(rel) == ".md" && entryUpdatedAt(path) > entryUpdatedAt(dst) {
			if err := moveFile(dst, filepath.Join(conflicts, rel)); err != nil {
				return err
			}
			return moveFile(path, dst)
		}
		return moveFile(path, filepath.Join(conflicts, rel))
	})
	if err != nil {
		return err
	}
	// Every file has been moved; only empty directories remain.
	return os.RemoveAll(legacy)
}

func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}
	return os.Rename(src, dst)
}

func entryUpdatedAt(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return info.ModTime().UnixMilli()
	}
	header, _ := splitHeader(string(raw))
	return timestamp(headerFields(header)["updatedAt"], info.ModTime().UnixMilli())
}

func (s *Store) remapRejections(remapped map[string]string) error {
	state, err := s.state()
	if err != nil {
		return err
	}
	changed := false
	for i, r := range state.Rejections {
		if current, ok := remapped[r.WorkdirHash]; ok {
			state.Rejections[i].WorkdirHash = current
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.saveState(state)
}
