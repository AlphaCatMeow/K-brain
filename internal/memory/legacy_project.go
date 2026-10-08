package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/gofrs/flock"
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
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, done := legacyMigrations.Load(s.root); done {
		return
	}
	if err := s.migrateLegacyProjects(); err != nil {
		log.Printf("memory: legacy project migration: %v", err)
		return
	}
	legacyMigrations.Store(s.root, true)
}

func (s *Store) migrateLegacyProjects() error {
	lockPath := filepath.Join(s.root, ".legacy-project-migration.lock")
	if err := s.safe(lockPath); err != nil {
		return err
	}
	lock := flock.New(lockPath, flock.SetPermissions(0600))
	if err := lock.Lock(); err != nil {
		return err
	}
	defer lock.Unlock()
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
	var errs []string
	for _, item := range items {
		name := item.Name()
		if !item.IsDir() || !isProjectID(name) {
			continue
		}
		legacy := filepath.Join(projects, name)
		if err := s.safe(filepath.Join(legacy, ".workdir.json")); err != nil {
			errs = append(errs, name+": "+err.Error())
			continue
		}
		workdir := readWorkdirMarker(legacy)
		current := logicalProjectHash(name, workdir)
		if current == name {
			continue
		}
		// Persist aliases before moving their only on-disk workdir marker.
		if err := s.remapRejections(map[string]string{name: current}); err != nil {
			errs = append(errs, "rejections: "+err.Error())
			continue
		}
		if err := s.foldProjectDir(legacy, filepath.Join(projects, current), name); err != nil {
			errs = append(errs, fmt.Sprintf("%s -> %s: %v", name, current, err))
			continue
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
	if err := s.safe(conflicts); err != nil {
		return err
	}
	marker := filepath.Join(legacy, ".workdir.json")
	if err := s.safe(marker); err != nil {
		return err
	}
	if err := s.safe(filepath.Join(target, ".workdir.json")); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(target, ".workdir.json")); os.IsNotExist(err) {
		data, err := os.ReadFile(marker)
		if err != nil {
			return err
		}
		if err := atomicStoreWrite(filepath.Join(target, ".workdir.json"), data); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
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
		// Keep the marker until the fold succeeds so partial migrations can retry.
		if path == marker {
			return nil
		}
		rel, err := filepath.Rel(legacy, path)
		if err != nil {
			return err
		}
		dst := filepath.Join(target, rel)
		if err := s.safe(dst); err != nil {
			return err
		}
		if _, err := os.Lstat(dst); os.IsNotExist(err) {
			return moveFile(path, dst)
		} else if err != nil {
			return err
		}
		// Both directories hold this file. For memory entries the newer one stays
		// live; the other is preserved, out of the index, under .legacy-<id>/.
		if filepath.Ext(rel) == ".md" && entryUpdatedAt(path) > entryUpdatedAt(dst) {
			if err := s.preserveLegacyFile(dst, filepath.Join(conflicts, rel)); err != nil {
				return err
			}
			return moveFile(path, dst)
		}
		return s.preserveLegacyFile(path, filepath.Join(conflicts, rel))
	})
	if err != nil {
		return err
	}
	if err := s.preserveLegacyFile(marker, filepath.Join(conflicts, ".workdir.json")); err != nil {
		return err
	}
	// Every file has been moved; only empty directories remain.
	return os.RemoveAll(legacy)
}

// Reserve each backup exclusively; a repeated import must not replace history.
func (s *Store) preserveLegacyFile(src, dst string) error {
	if err := s.safe(src); err != nil {
		return err
	}
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return storeError("invalid_path", "memory backup source must be a regular file")
	}
	for n := 0; ; n++ {
		candidate := dst
		if n > 0 {
			candidate = fmt.Sprintf("%s.%d", dst, n)
		}
		if err := s.safe(candidate); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(candidate), 0700); err != nil {
			return err
		}
		out, err := os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		in, err := os.Open(src)
		if err == nil {
			_, err = io.Copy(out, in)
			err = errors.Join(err, in.Close())
		}
		if err == nil {
			err = out.Sync()
		}
		err = errors.Join(err, out.Close())
		if err != nil {
			return errors.Join(err, os.Remove(candidate))
		}
		return os.Remove(src)
	}
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
