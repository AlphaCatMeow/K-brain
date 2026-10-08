package memory

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

type Store struct {
	root string
	mu   *sync.Mutex
}

var storeLocks sync.Map

type storedEntry struct {
	Meta
	body     string
	header   string
	source   map[string]any
	evidence *Evidence
	path     string
	// legacyDir is the on-disk projects/<id> name when it differs from WorkdirHash
	// (a directory written by the legacy desktop store).
	legacyDir string
}

func OpenStore(root string) (*Store, error) {
	if root == "" {
		dir, err := config.Dir()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(dir, "memory")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	lock, _ := storeLocks.LoadOrStore(root, &sync.Mutex{})
	s := &Store{root: root, mu: lock.(*sync.Mutex)}
	s.migrateLegacyProjectsOnce()
	return s, nil
}
func (s *Store) Root() string { return s.root }
func ProjectHash(workdir string) (string, error) {
	p, err := resolveProjectPath(workdir)
	if err != nil {
		return "", err
	}
	return hashProjectPath(p), nil
}
func splitHeader(raw string) (string, string) {
	raw = strings.TrimPrefix(strings.ReplaceAll(raw, "\r\n", "\n"), "\ufeff")
	if !strings.HasPrefix(raw, "---\n") {
		return "", raw
	}
	rest := raw[4:]
	idx := strings.Index("\n"+rest, "\n---\n")
	if idx < 0 {
		if strings.HasSuffix(rest, "\n---") {
			return strings.TrimSuffix(rest, "\n---"), ""
		}
		return "", raw
	}
	return rest[:idx], strings.TrimLeft(rest[idx+4:], "\n")
}
func scalar(v string) string {
	v = strings.TrimSpace(v)
	var out string
	if json.Unmarshal([]byte(v), &out) == nil {
		return out
	}
	return strings.Trim(v, "'\"")
}
func headerFields(header string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(header, "\n") {
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if ok {
			m[k] = scalar(v)
		}
	}
	return m
}
func timestamp(v string, fallback int64) int64 {
	if t, e := time.Parse(time.RFC3339Nano, v); e == nil {
		return t.UnixMilli()
	}
	return fallback
}
func (s *Store) safe(path string) error {
	rel, err := filepath.Rel(s.root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return storeError("invalid_path", "memory path escapes root")
	}
	current := s.root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, e := os.Lstat(current)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return storeError("invalid_path", "memory symlinks are not allowed")
		}
	}
	return nil
}
func (s *Store) load() ([]storedEntry, error) {
	out := []storedEntry{}
	err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == s.root {
			return nil
		}
		name := d.Name()
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			if strings.HasPrefix(name, ".") && name != ".archive" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(name) != ".md" || name == "MEMORY.md" {
			return nil
		}
		rel, _ := filepath.Rel(s.root, path)
		parts := strings.Split(rel, string(filepath.Separator))
		if parts[0] != "global" && parts[0] != "projects" {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if info.Size() > 1<<20 {
			return fmt.Errorf("memory file exceeds 1 MiB: %s", path)
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		h, b := splitHeader(string(raw))
		f := headerFields(h)
		entry := storedEntry{Meta: Meta{Slug: f["name"], Scope: f["scope"], MemoryType: f["type"], Description: f["description"], Headline: f["headline"], Confidence: "unknown", FileSize: info.Size(), Archived: strings.Contains(filepath.ToSlash(rel), "/.archive/")}, body: b, header: h, path: path, source: map[string]any{}}
		if entry.Slug == "" {
			entry.Slug = strings.TrimSuffix(name, ".md")
		}
		if entry.Scope == "" {
			entry.Scope = "global"
			if parts[0] == "projects" {
				entry.Scope = "project"
			}
		}
		if entry.MemoryType == "" {
			entry.MemoryType = "reference"
			if strings.Contains(filepath.ToSlash(rel), "/daily/") {
				entry.MemoryType = "daily"
			}
		}
		if entry.MemoryType == "daily" {
			date := f["date"]
			if date == "" {
				date = strings.TrimPrefix(entry.Slug, "daily-")
			}
			entry.DateLocal = &date
			entry.Slug = "daily-" + date
			if entry.Headline == "" {
				entry.Headline = date
			}
		}
		if parts[0] == "projects" && len(parts) > 2 {
			marker := filepath.Join(s.root, "projects", parts[1], ".workdir.json")
			if e := s.safe(marker); e != nil {
				return e
			}
			data, e := os.ReadFile(marker)
			if e == nil {
				var m struct {
					Path string `json:"path"`
				}
				if json.Unmarshal(data, &m) == nil {
					entry.WorkdirPath = m.Path
				}
			}
			// A directory written by the legacy desktop store carries the id of a
			// differently spelled path. Report the current id for its workdir so
			// every scope comparison agrees, and remember the on-disk directory so
			// a later write can supersede it instead of leaving a duplicate.
			entry.WorkdirHash = logicalProjectHash(parts[1], entry.WorkdirPath)
			if entry.WorkdirHash != parts[1] {
				entry.legacyDir = parts[1]
			}
		}
		entry.CreatedAt = timestamp(f["createdAt"], info.ModTime().UnixMilli())
		entry.UpdatedAt = timestamp(f["updatedAt"], info.ModTime().UnixMilli())
		entry.AppendCount, _ = strconv.Atoi(f["appendCount"])
		inSource := false
		inEvidence := false
		for _, line := range strings.Split(h, "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
				inSource = strings.HasPrefix(trimmed, "source:")
				inEvidence = strings.HasPrefix(trimmed, "evidence:")
				continue
			}
			k, v, ok := strings.Cut(trimmed, ":")
			if !ok {
				continue
			}
			value := scalar(v)
			if inSource {
				entry.source[k] = value
				if k == "unreviewed" {
					entry.Unreviewed = value == "true"
					entry.source[k] = entry.Unreviewed
				}
			}
			if inEvidence && k == "confidence" {
				entry.Confidence = strings.ToLower(value)
			}
		}
		parseEvidence := func(block string) *Evidence {
			var ev Evidence
			active := false
			for _, line := range strings.Split(block, "\n") {
				trimmed := strings.TrimSpace(line)
				if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
					active = strings.HasPrefix(trimmed, "evidence:")
					continue
				}
				if !active {
					continue
				}
				key, value, ok := strings.Cut(trimmed, ":")
				if !ok {
					continue
				}
				value = scalar(value)
				switch key {
				case "confidence":
					ev.Confidence = strings.ToLower(value)
				case "source_quote":
					ev.SourceQuote = value
				case "reasoning":
					ev.Reasoning = value
				case "supersedes":
					ev.Supersedes = value
				case "override_reject":
					ev.OverrideReject = value
				case "aliases":
					if value != "" {
						ev.Aliases = strings.Split(value, ", ")
					}
				case "conflicts_with":
					if value != "" {
						ev.ConflictsWith = strings.Split(value, ", ")
					}
				}
			}
			if ev.Confidence == "" {
				fields := headerFields(block)
				ev.Confidence = strings.ToLower(fields["confidence"])
				ev.SourceQuote = fields["source_quote"]
				ev.Reasoning = fields["reasoning"]
				ev.Supersedes = fields["supersedes"]
				ev.OverrideReject = fields["override_reject"]
			}
			if ev.Confidence == "" {
				return nil
			}
			return &ev
		}
		entry.evidence = parseEvidence(h)
		if entry.evidence == nil {
			eh, _ := splitHeader(b)
			entry.evidence = parseEvidence(eh)
		}
		if entry.evidence != nil {
			entry.Confidence = entry.evidence.Confidence
		}
		if entry.Confidence != "high" && entry.Confidence != "medium" && entry.Confidence != "low" {
			entry.Confidence = "unknown"
		}
		out = append(out, entry)
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].path < out[j].path
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out, err
}
func validateScope(scope string, read bool) error {
	if scope == "global" || scope == "project" || (read && (scope == "" || scope == "auto")) {
		return nil
	}
	return storeError("invalid_scope", "scope must be global or project")
}
func validateSlug(slug string) error {
	if slug == "" || len(slug) > 160 || slug == "." || slug == ".." {
		return storeError("invalid_slug", "invalid memory slug")
	}
	for _, r := range slug {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return storeError("invalid_slug", "invalid memory slug")
		}
	}
	return nil
}
func scopeHash(workdir, hash string) (string, error) {
	if hash != "" {
		if len(hash) != 16 {
			return "", storeError("invalid_workdir_hash", "invalid project hash")
		}
		if _, e := hex.DecodeString(hash); e != nil {
			return "", storeError("invalid_workdir_hash", "invalid project hash")
		}
	}
	if workdir != "" {
		h, e := ProjectHash(workdir)
		if e != nil {
			return "", e
		}
		// Ids written by the legacy desktop store for the same workdir are the
		// same project; normalize them to the current id.
		if lower := strings.ToLower(hash); hash != "" && h != lower && !slices.Contains(legacyProjectHashes(workdir), lower) {
			return "", storeError("scope_mismatch", "workdir and hash disagree")
		}
		return h, nil
	}
	return strings.ToLower(hash), nil
}
func findEntry(entries []storedEntry, args ReadArgs) (storedEntry, error) {
	if err := validateSlug(args.Slug); err != nil {
		return storedEntry{}, err
	}
	if err := validateScope(args.Scope, true); err != nil {
		return storedEntry{}, err
	}
	hash, err := scopeHash(args.Workdir, args.WorkdirHash)
	if err != nil {
		return storedEntry{}, err
	}
	if args.Scope == "project" && hash == "" {
		return storedEntry{}, storeError("workdir_required", "project memory requires workdir or hash")
	}
	var global *storedEntry
	for _, e := range entries {
		if e.Slug != args.Slug {
			continue
		}
		if e.Scope == "project" && (e.WorkdirHash == hash || (e.legacyDir != "" && e.legacyDir == hash)) && args.Scope != "global" {
			return e, nil
		}
		if e.Scope == "global" && args.Scope != "project" {
			copy := e
			global = &copy
		}
	}
	if global != nil {
		return *global, nil
	}
	return storedEntry{}, storeError("not_found", "memory not found")
}
func atomicStoreWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".memory-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	cerr := f.Close()
	if err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	return os.Rename(f.Name(), path)
}
func (s *Store) state() (storeState, error) {
	var v storeState
	path := filepath.Join(s.root, ".kbrain-state.json")
	if err := s.safe(path); err != nil {
		return v, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	err = json.Unmarshal(data, &v)
	return v, err
}
func (s *Store) saveState(v storeState) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return atomicStoreWrite(filepath.Join(s.root, ".kbrain-state.json"), data)
}
func readResult(e storedEntry, args ReadArgs) (ReadResponse, error) {
	if args.Offset < 0 || args.Length < 0 {
		return ReadResponse{}, storeError("invalid_window", "negative read window")
	}
	body := strings.TrimRight(e.body, "\n")
	lines := strings.Split(body, "\n")
	if body == "" {
		lines = nil
	}
	start := min(args.Offset, len(lines))
	length := args.Length
	if length == 0 {
		length = 200
	}
	end := min(start+min(length, 10000), len(lines))
	return ReadResponse{Slug: e.Slug, Scope: e.Scope, MemoryType: e.MemoryType, Description: e.Description, Headline: e.Headline, Body: strings.Join(lines[start:end], "\n"), TotalLines: len(lines), Window: ReadWindow{Offset: start, Length: end - start, Truncated: end < len(lines)}, Meta: ReadMeta{Unreviewed: e.Unreviewed, Confidence: e.Confidence, Source: e.source, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt, Archived: e.Archived}}, nil
}
func (s *Store) Read(args ReadArgs) (ReadResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.load()
	if err != nil {
		return ReadResponse{}, err
	}
	e, err := findEntry(entries, args)
	if err != nil {
		return ReadResponse{}, err
	}
	return readResult(e, args)
}
func isNotFound(err error) bool {
	var e *StoreError
	return errors.As(err, &e) && e.Code == "not_found"
}
