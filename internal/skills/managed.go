package skills

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

const (
	maxManagedFile  = 10 << 20
	maxReadBytes    = 200 << 10
	maxArchiveFiles = 4096
	maxArchiveBytes = 100 << 20
)

var managedWriteMu sync.Mutex

type SourceMetadata struct {
	Registry               string `json:"registry"`
	Slug                   string `json:"slug"`
	OwnerHandle            string `json:"ownerHandle,omitempty"`
	Version                string `json:"version,omitempty"`
	OriginalName           string `json:"originalName,omitempty"`
	NormalizedName         string `json:"normalizedName,omitempty"`
	CompatibilityTransform string `json:"compatibilityTransform,omitempty"`
}
type SkillSummary struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Target      string          `json:"target"`
	SkillFile   string          `json:"skillFile"`
	BaseDir     string          `json:"baseDir"`
	BuiltIn     bool            `json:"builtIn"`
	InstalledAt int64           `json:"installedAt,omitempty"`
	Source      *SourceMetadata `json:"source,omitempty"`
}
type InvalidSkill struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}
type InstallResult struct {
	Name      string `json:"name"`
	Target    string `json:"target"`
	Backup    string `json:"backup,omitempty"`
	SkillFile string `json:"skillFile"`
}
type Validation struct {
	Name   string   `json:"name"`
	Target string   `json:"target"`
	OK     bool     `json:"ok"`
	Errors []string `json:"errors"`
}
type PackageResult struct {
	Name    string `json:"name"`
	Target  string `json:"target"`
	Archive string `json:"archive"`
}
type DeleteResult struct {
	Name   string `json:"name"`
	Target string `json:"target"`
}
type SkillSettings struct {
	Enabled    bool     `json:"enabled"`
	Selected   []string `json:"selected"`
	Mode       string   `json:"mode,omitempty"`
	SkillNames []string `json:"skillNames,omitempty"`
}
type settingsFile struct {
	Revision uint64                   `json:"revision"`
	Global   SkillSettings            `json:"global"`
	Projects map[string]SkillSettings `json:"projects"`
}
type Job struct {
	JobID           string          `json:"jobId"`
	Phase           string          `json:"phase"`
	Source          string          `json:"source"`
	DownloadedBytes int64           `json:"downloadedBytes"`
	TotalBytes      *int64          `json:"totalBytes,omitempty"`
	Installed       []InstallResult `json:"installed,omitempty"`
	Error           string          `json:"error,omitempty"`
	StartedAt       int64           `json:"startedAt"`
	UpdatedAt       int64           `json:"updatedAt"`
	FinishedAt      int64           `json:"finishedAt,omitempty"`
	cancel          context.CancelFunc
}
type ExternalSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	BaseDir     string `json:"baseDir"`
	SkillFile   string `json:"skillFile"`
}
type ExternalScan struct {
	Tool    string          `json:"tool"`
	RootDir string          `json:"rootDir"`
	Exists  bool            `json:"exists"`
	Skills  []ExternalSkill `json:"skills"`
	Errors  []string        `json:"errors"`
}
type ClawHubCard struct {
	Slug          string `json:"slug"`
	DisplayName   string `json:"displayName"`
	Summary       string `json:"summary"`
	LatestVersion string `json:"latestVersion,omitempty"`
	Downloads     int64  `json:"downloads"`
	Stars         int64  `json:"stars"`
	OwnerHandle   string `json:"ownerHandle,omitempty"`
	WebURL        string `json:"webUrl,omitempty"`
	DownloadURL   string `json:"downloadUrl"`
}
type ManagerResponse struct {
	Action            string          `json:"action"`
	RootDir           string          `json:"rootDir"`
	Path              string          `json:"path,omitempty"`
	Content           string          `json:"content,omitempty"`
	Truncated         bool            `json:"truncated,omitempty"`
	StartLine         int             `json:"startLine,omitempty"`
	NumLines          int             `json:"numLines,omitempty"`
	Skills            []SkillSummary  `json:"skills,omitempty"`
	Invalid           []InvalidSkill  `json:"invalid,omitempty"`
	Installed         []InstallResult `json:"installed,omitempty"`
	Created           *InstallResult  `json:"created,omitempty"`
	Validation        *Validation     `json:"validation,omitempty"`
	Package           *PackageResult  `json:"package,omitempty"`
	Deleted           *DeleteResult   `json:"deleted,omitempty"`
	InstallJob        *Job            `json:"installJob,omitempty"`
	ClawhubNextCursor string          `json:"clawhubNextCursor,omitempty"`
	ClawhubResults    []ClawHubCard   `json:"clawhubResults,omitempty"`
	External          []ExternalScan  `json:"external,omitempty"`
	ExternalMCP       []any           `json:"externalMcp,omitempty"`
}

type Manager struct {
	root              string
	jobsMu            sync.Mutex
	jobs              map[string]*Job
	client            *http.Client
	allowInsecureHTTP bool
	storeURL          string
}

var managersMu sync.Mutex
var managers = map[string]*Manager{}

func NewManager() (*Manager, error) {
	d, err := config.Dir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(d, "skills")
	managersMu.Lock()
	defer managersMu.Unlock()
	if m := managers[root]; m != nil {
		return m, nil
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	m := &Manager{root: canonical, jobs: map[string]*Job{}, client: publicHTTPClient(), storeURL: "https://clawhub.ai"}
	if err := m.ensureBuiltins(); err != nil {
		return nil, err
	}
	managers[root] = m
	return m, nil
}
func (m *Manager) Root() string { return m.root }
func validManagedName(n string) bool {
	return ValidName(n) && len(n) <= specMaxName && !strings.HasPrefix(n, "-") && !strings.HasSuffix(n, "-") && !strings.Contains(n, "--")
}
func cleanName(n string) error {
	if !validManagedName(n) {
		return fmt.Errorf("invalid skill name %q", n)
	}
	return nil
}
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}
func childPath(rel string) (string, error) {
	if rel == "" || strings.ContainsAny(rel, "\\:\x00") || filepath.IsAbs(rel) {
		return "", errors.New("invalid relative skill path")
	}
	for _, c := range strings.Split(filepath.ToSlash(rel), "/") {
		if c == ".." || strings.HasPrefix(c, ".") || c == "" || strings.HasSuffix(c, " ") || strings.HasSuffix(c, ".") {
			return "", errors.New("invalid skill path component")
		}
	}
	return filepath.FromSlash(rel), nil
}
func safePath(root, rel string) (string, error) {
	rel = strings.TrimPrefix(strings.TrimPrefix(rel, "skill://"), "skill:")
	rel, err := childPath(rel)
	if err != nil {
		return "", err
	}
	p := root
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		p = filepath.Join(p, part)
		i, e := os.Lstat(p)
		if e != nil {
			return "", e
		}
		if i.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("symlink paths are not allowed")
		}
	}
	return p, nil
}
func (m *Manager) ensureBuiltins() error {
	managedWriteMu.Lock()
	defer managedWriteMu.Unlock()
	for _, n := range []string{"skills-creator", "skills-installer"} {
		d := filepath.Join(m.root, n)
		if i, err := os.Lstat(d); err == nil {
			if !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
				return errors.New("invalid builtin directory")
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		body := "Use SkillsManager to install skills from local directories, .zip/.skill archives, HTTPS or GitHub URLs. Use clawhub_search and clawhub_install with ownerHandle and slug for registry skills. Inspect the returned validation errors. Use list and read to inspect installed instructions, package to export, and delete only when requested. Built-in skills cannot be deleted or overwritten."
		if n == "skills-creator" {
			body = "Use SkillsManager create with a lowercase hyphenated name, a concise description and a Markdown body describing an actionable workflow. Put detailed reference material in files [{path,content}]. Do not replace SKILL.md via files. Validate the created skill and use package to export it. Read the generated instructions and test the workflow before reporting completion."
		}
		if err := os.Mkdir(d, 0700); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(fmt.Sprintf("---\nname: %s\ndescription: Built-in skill management instructions.\n---\n\n%s\n", n, body)), 0600); err != nil {
			return err
		}
	}
	return nil
}
func isBuiltin(n string) bool { return n == "skills-creator" || n == "skills-installer" }

func (m *Manager) List() ([]SkillSummary, []InvalidSkill, error) {
	if err := m.ensureBuiltins(); err != nil {
		return nil, nil, err
	}
	managedWriteMu.Lock()
	defer managedWriteMu.Unlock()
	es, err := os.ReadDir(m.root)
	if err != nil {
		return nil, nil, err
	}
	out := []SkillSummary{}
	bad := []InvalidSkill{}
	for _, e := range es {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p, pathErr := safePath(m.root, e.Name()+"/SKILL.md")
		if pathErr != nil {
			bad = append(bad, InvalidSkill{Path: e.Name(), Error: pathErr.Error()})
			continue
		}
		s, err := parse(p)
		if err != nil {
			bad = append(bad, InvalidSkill{Path: p, Error: err.Error()})
			continue
		}
		if s.Name == "" {
			s.Name = e.Name()
		}
		if w := validate(s); w != "" {
			bad = append(bad, InvalidSkill{Path: p, Error: w})
			continue
		}
		sum := SkillSummary{Name: s.Name, Description: s.Description, Target: filepath.Dir(p), SkillFile: e.Name() + "/SKILL.md", BaseDir: e.Name(), BuiltIn: isBuiltin(e.Name())}
		if st, e2 := os.Stat(p); e2 == nil {
			sum.InstalledAt = st.ModTime().UnixMilli()
		}
		meta, metaErr := safePath(m.root, e.Name()+"/_meta.json")
		if b, e2 := os.ReadFile(meta); metaErr == nil && e2 == nil {
			_ = json.Unmarshal(b, &sum.Source)
		}
		out = append(out, sum)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, bad, nil
}
func (m *Manager) Read(path string, offset, length int) (string, bool, int, error) {
	if offset < 0 || length < 0 {
		return "", false, 0, errors.New("invalid read window")
	}
	if length == 0 {
		length = 200
	}
	managedWriteMu.Lock()
	defer managedWriteMu.Unlock()
	p, err := safePath(m.root, path)
	if err != nil {
		return "", false, 0, err
	}
	f, err := os.Open(p)
	if err != nil {
		return "", false, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", false, 0, err
	}
	if !info.Mode().IsRegular() {
		return "", false, 0, errors.New("read requires a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxReadBytes+1))
	if err != nil {
		return "", false, 0, err
	}
	truncated := len(b) > maxReadBytes
	if truncated {
		b = b[:maxReadBytes]
	}
	lines := strings.SplitAfter(string(b), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if offset >= len(lines) {
		return "", truncated, 0, nil
	}
	end := offset + length
	if end < offset || end > len(lines) {
		end = len(lines)
	}
	return strings.Join(lines[offset:end], ""), truncated || end < len(lines), end - offset, nil
}
func (m *Manager) Validate(name string) Validation {
	v := Validation{Name: name, Target: filepath.Join(m.root, name), Errors: []string{}}
	if err := cleanName(name); err != nil {
		v.Errors = append(v.Errors, err.Error())
		return v
	}
	managedWriteMu.Lock()
	defer managedWriteMu.Unlock()
	_, err := validateTree(v.Target)
	v.OK = err == nil
	if err != nil {
		v.Errors = append(v.Errors, err.Error())
	}
	return v
}

func copyTree(dst, src string) error {
	if err := os.MkdirAll(dst, 0700); err != nil {
		return err
	}
	var total int64
	count := 0
	return filepath.Walk(src, func(p string, i os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if i.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlinks are not allowed in skills")
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "." {
			return nil
		}
		if _, err := childPath(filepath.ToSlash(rel)); err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if i.IsDir() {
			return os.MkdirAll(out, 0700)
		}
		count++
		total += i.Size()
		if !i.Mode().IsRegular() || i.Size() > maxManagedFile || total > maxArchiveBytes || count > maxArchiveFiles {
			return errors.New("invalid or oversized skill tree")
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		o, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600|i.Mode().Perm()&0111)
		if err != nil {
			return err
		}
		n, err := io.Copy(o, io.LimitReader(in, maxManagedFile+1))
		ce := o.Close()
		if err != nil {
			return err
		}
		if n > maxManagedFile {
			return errors.New("skill file is too large")
		}
		return ce
	})
}
func validateTree(root string) (string, error) {
	var total int64
	count := 0
	err := filepath.Walk(root, func(p string, i os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if i.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlinks are not allowed")
		}
		if i.IsDir() {
			return nil
		}
		count++
		total += i.Size()
		if !i.Mode().IsRegular() || count > maxArchiveFiles || i.Size() > maxManagedFile || total > maxArchiveBytes {
			return errors.New("invalid or oversized skill tree")
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	file := filepath.Join(root, "SKILL.md")
	s, err := parse(file)
	if err != nil {
		return "", err
	}
	if !validManagedName(s.Name) || strings.TrimSpace(s.Description) == "" {
		return "", errors.New("valid name and description are required")
	}
	if w := validate(s); w != "" {
		return "", errors.New(w)
	}
	return file, nil
}
func (m *Manager) installDir(ctx context.Context, src, name string, meta *SourceMetadata, conflict string) (InstallResult, error) {
	if conflict == "" {
		conflict = "backup"
	}
	if conflict != "fail" && conflict != "backup" && conflict != "overwrite" {
		return InstallResult{}, errors.New("invalid conflict mode")
	}
	stage, err := os.MkdirTemp(m.root, ".staging-")
	if err != nil {
		return InstallResult{}, err
	}
	defer os.RemoveAll(stage)
	dest := filepath.Join(stage, "content")
	if err = copyTree(dest, src); err != nil {
		return InstallResult{}, err
	}
	parsed, err := parse(filepath.Join(dest, "SKILL.md"))
	if err != nil {
		return InstallResult{}, err
	}
	original := parsed.Name
	if name == "" {
		name = original
	}
	if meta != nil {
		name = normalizeRegistryName(name)
		meta.OriginalName = original
		meta.NormalizedName = name
		if original != name {
			meta.CompatibilityTransform = "normalized-agent-skill-name"
		}
	}
	if err = cleanName(name); err != nil {
		return InstallResult{}, err
	}
	if isBuiltin(name) {
		return InstallResult{}, errors.New("built-in skills cannot be overwritten")
	}
	if name != original {
		b, e := os.ReadFile(filepath.Join(dest, "SKILL.md"))
		if e != nil {
			return InstallResult{}, e
		}
		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			if strings.HasPrefix(line, "name:") {
				lines[i] = "name: " + name
				break
			}
		}
		if e = os.WriteFile(filepath.Join(dest, "SKILL.md"), []byte(strings.Join(lines, "\n")), 0600); e != nil {
			return InstallResult{}, e
		}
	}
	if _, err = validateTree(dest); err != nil {
		return InstallResult{}, err
	}
	if meta != nil {
		b, _ := json.MarshalIndent(meta, "", "  ")
		if err = os.WriteFile(filepath.Join(dest, "_meta.json"), b, 0600); err != nil {
			return InstallResult{}, err
		}
	}
	managedWriteMu.Lock()
	defer managedWriteMu.Unlock()
	if err = ctx.Err(); err != nil {
		return InstallResult{}, err
	}
	target := filepath.Join(m.root, name)
	backup := ""
	rollback := ""
	if i, e := os.Lstat(target); e == nil {
		if i.Mode()&os.ModeSymlink != 0 {
			return InstallResult{}, errors.New("target is a symlink")
		}
		if conflict == "fail" {
			return InstallResult{}, errors.New("skill already exists")
		}
		rollback = filepath.Join(stage, "previous")
		if conflict == "backup" {
			if e = os.MkdirAll(filepath.Join(m.root, ".backups"), 0700); e != nil {
				return InstallResult{}, e
			}
			rollback = filepath.Join(m.root, ".backups", fmt.Sprintf("%s-%d", name, time.Now().UnixNano()))
			backup = rollback
		}
		if e = os.Rename(target, rollback); e != nil {
			return InstallResult{}, e
		}
	} else if !os.IsNotExist(e) {
		return InstallResult{}, e
	}
	if err = os.Rename(dest, target); err != nil {
		if rollback != "" {
			_ = os.Rename(rollback, target)
		}
		return InstallResult{}, err
	}
	return InstallResult{Name: name, Target: target, SkillFile: name + "/SKILL.md", Backup: backup}, nil
}
func (m *Manager) zipSource(src string) (string, error) {
	r, err := zip.OpenReader(src)
	if err != nil {
		return "", err
	}
	defer r.Close()
	tmp, err := os.MkdirTemp(m.root, ".staging-zip-")
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(tmp)
		}
	}()
	var total uint64
	seen := map[string]bool{}
	if len(r.File) > maxArchiveFiles {
		return "", errors.New("too many archive entries")
	}
	for _, f := range r.File {
		name := strings.TrimSuffix(f.Name, "/")
		rel, e := childPath(name)
		if e != nil {
			return "", e
		}
		key := strings.ToLower(rel)
		if seen[key] {
			return "", errors.New("duplicate archive path")
		}
		seen[key] = true
		if f.Mode()&os.ModeSymlink != 0 || (!f.FileInfo().IsDir() && !f.Mode().IsRegular()) {
			return "", errors.New("archive contains special file")
		}
		out := filepath.Join(tmp, rel)
		if f.FileInfo().IsDir() {
			if e = os.MkdirAll(out, 0700); e != nil {
				return "", e
			}
			continue
		}
		if f.UncompressedSize64 > maxManagedFile {
			return "", errors.New("archive file is too large")
		}
		total += f.UncompressedSize64
		if total > maxArchiveBytes {
			return "", errors.New("archive is too large")
		}
		if e = os.MkdirAll(filepath.Dir(out), 0700); e != nil {
			return "", e
		}
		in, e := f.Open()
		if e != nil {
			return "", e
		}
		b, e := io.ReadAll(io.LimitReader(in, maxManagedFile+1))
		_ = in.Close()
		if e != nil {
			return "", e
		}
		if len(b) > maxManagedFile {
			return "", errors.New("archive file is too large")
		}
		if e = os.WriteFile(out, b, 0600|f.Mode().Perm()&0111); e != nil {
			return "", e
		}
	}
	ok = true
	return tmp, nil
}
func (m *Manager) Install(ctx context.Context, source, name, conflict string, meta *SourceMetadata, progress func(int64, int64)) (InstallResult, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return InstallResult{}, errors.New("source is required")
	}
	subpath := ""
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		u, parseErr := validateRemoteURL(source, m.allowInsecureHTTP)
		if parseErr != nil {
			return InstallResult{}, parseErr
		}
		var err error
		if u.Host == "github.com" {
			source, subpath, err = githubSource(source)
		} else if u.Scheme != "https" && u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" {
			return InstallResult{}, errors.New("remote skill sources require HTTPS")
		}
		if err != nil {
			return InstallResult{}, err
		}
		source, err = m.download(ctx, source, progress)
		if err != nil {
			return InstallResult{}, err
		}
		defer os.Remove(source)
	}
	st, err := os.Lstat(source)
	if err != nil {
		return InstallResult{}, err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return InstallResult{}, errors.New("source is a symlink")
	}
	if st.IsDir() {
		return m.installDir(ctx, source, name, meta, conflict)
	}
	rawSkill := strings.EqualFold(filepath.Base(source), "SKILL.md")
	if !rawSkill {
		if f, e := os.Open(source); e == nil {
			var header [3]byte
			_, _ = io.ReadFull(f, header[:])
			_ = f.Close()
			rawSkill = string(header[:]) == "---"
		}
	}
	if rawSkill {
		tmp, e := os.MkdirTemp(m.root, ".staging-file-")
		if e != nil {
			return InstallResult{}, e
		}
		defer os.RemoveAll(tmp)
		b, e := os.ReadFile(source)
		if e != nil {
			return InstallResult{}, e
		}
		if e = os.WriteFile(filepath.Join(tmp, "SKILL.md"), b, 0600); e != nil {
			return InstallResult{}, e
		}
		return m.installDir(ctx, tmp, name, meta, conflict)
	}
	tmp, err := m.zipSource(source)
	if err != nil {
		return InstallResult{}, err
	}
	defer os.RemoveAll(tmp)
	base := tmp
	if _, err = os.Stat(filepath.Join(base, "SKILL.md")); err != nil {
		entries, e := os.ReadDir(base)
		if e != nil {
			return InstallResult{}, e
		}
		if len(entries) == 1 && entries[0].IsDir() {
			base = filepath.Join(base, entries[0].Name())
		}
	}
	if subpath != "" {
		base = filepath.Join(base, subpath)
	}
	return m.installDir(ctx, base, name, meta, conflict)
}

func (m *Manager) Create(name, description, body, conflict string, files []map[string]string) (InstallResult, error) {
	if err := cleanName(name); err != nil {
		return InstallResult{}, err
	}
	if isBuiltin(name) {
		return InstallResult{}, errors.New("built-in skills cannot be created")
	}
	tmp, err := os.MkdirTemp(m.root, ".staging-create-")
	if err != nil {
		return InstallResult{}, err
	}
	defer os.RemoveAll(tmp)
	if description == "" || len(description) > specMaxDesc {
		return InstallResult{}, errors.New("description is required or too long")
	}
	if body == "" {
		body = "## Workflow\n\n1. Inspect the request.\n2. Follow the skill workflow.\n3. Validate the result.\n"
	}
	quoted, _ := json.Marshal(description)
	text := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n", name, quoted, body)
	if err = os.WriteFile(filepath.Join(tmp, "SKILL.md"), []byte(text), 0600); err != nil {
		return InstallResult{}, err
	}
	for _, f := range files {
		p, pathErr := childPath(f["path"])
		if pathErr != nil || strings.EqualFold(filepath.Base(p), "SKILL.md") || strings.EqualFold(filepath.Base(p), "_meta.json") || len(f["content"]) > maxManagedFile {
			return InstallResult{}, errors.New("unsafe created file path")
		}
		out := filepath.Join(tmp, filepath.FromSlash(p))
		if !within(tmp, out) {
			return InstallResult{}, errors.New("created path escapes")
		}
		if err = os.MkdirAll(filepath.Dir(out), 0700); err != nil {
			return InstallResult{}, err
		}
		if err = os.WriteFile(out, []byte(f["content"]), 0600); err != nil {
			return InstallResult{}, err
		}
	}
	return m.installDir(context.Background(), tmp, name, nil, conflict)
}
func (m *Manager) Delete(name string) (DeleteResult, error) {
	if err := cleanName(name); err != nil {
		return DeleteResult{}, err
	}
	if isBuiltin(name) {
		return DeleteResult{}, errors.New("built-in skills cannot be deleted")
	}
	p := filepath.Join(m.root, name)
	managedWriteMu.Lock()
	defer managedWriteMu.Unlock()
	if _, err := os.Lstat(p); err != nil {
		return DeleteResult{}, err
	}
	if err := os.RemoveAll(p); err != nil {
		return DeleteResult{}, err
	}
	return DeleteResult{Name: name, Target: p}, nil
}
func (m *Manager) Package(name string) (PackageResult, error) {
	if err := cleanName(name); err != nil {
		return PackageResult{}, err
	}
	managedWriteMu.Lock()
	defer managedWriteMu.Unlock()
	src := filepath.Join(m.root, name)
	if _, err := os.Stat(src); err != nil {
		return PackageResult{}, err
	}
	if _, err := validateTree(src); err != nil {
		return PackageResult{}, err
	}
	dir := filepath.Join(m.root, ".packages")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return PackageResult{}, err
	}
	f, err := os.CreateTemp(dir, name+"-*.skill")
	out := ""
	if err == nil {
		out = f.Name()
	}
	if err != nil {
		return PackageResult{}, err
	}
	zw := zip.NewWriter(f)
	err = filepath.Walk(src, func(p string, i os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if i.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(m.root, p)
		w, e := zw.Create(filepath.ToSlash(rel))
		if e != nil {
			return e
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		_, e = w.Write(b)
		return e
	})
	if e := zw.Close(); err == nil {
		err = e
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return PackageResult{}, err
	}
	return PackageResult{Name: name, Target: src, Archive: out}, nil
}

func (m *Manager) scanExternal(root string) ExternalScan {
	x := ExternalScan{Tool: filepath.Base(root), RootDir: root}
	ents, e := os.ReadDir(root)
	if e != nil {
		return x
	}
	x.Exists = true
	for _, ent := range ents {
		if !ent.IsDir() {
			continue
		}
		p := filepath.Join(root, ent.Name(), "SKILL.md")
		s, e := parse(p)
		if e == nil {
			x.Skills = append(x.Skills, ExternalSkill{Name: s.Name, Description: s.Description, BaseDir: filepath.Dir(p), SkillFile: p})
		}
	}
	return x
}
func (m *Manager) External() []ExternalScan {
	home, _ := os.UserHomeDir()
	return []ExternalScan{m.scanExternal(filepath.Join(home, ".agents", "skills")), m.scanExternal(filepath.Join(home, ".codex", "skills")), m.scanExternal(filepath.Join(home, ".claude", "skills"))}
}

func (m *Manager) settingsPath() string {
	return filepath.Join(filepath.Dir(m.root), "skills-settings.json")
}
func (m *Manager) loadSettings() (settingsFile, error) {
	s := settingsFile{Global: SkillSettings{Enabled: true}, Projects: map[string]SkillSettings{}}
	b, err := os.ReadFile(m.settingsPath())
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	if s.Projects == nil {
		s.Projects = map[string]SkillSettings{}
	}
	return s, nil
}
func projectKey(workdir string) (string, error) {
	path, err := filepath.Abs(workdir)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	i, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !i.IsDir() {
		return "", errors.New("workdir must be a directory")
	}
	h := sha256.Sum256([]byte(path))
	return fmt.Sprintf("%x", h[:8]), nil
}
func builtinsSelected(names []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, n := range append(append([]string{}, names...), "skills-creator", "skills-installer") {
		if !seen[n] {
			out = append(out, n)
			seen[n] = true
		}
	}
	return out
}
func (m *Manager) Settings(workdir string) (uint64, SkillSettings, error) {
	managedWriteMu.Lock()
	defer managedWriteMu.Unlock()
	s, err := m.loadSettings()
	if err != nil {
		return 0, SkillSettings{}, err
	}
	v := s.Global
	if workdir != "" {
		key, e := projectKey(workdir)
		if e != nil {
			return 0, v, e
		}
		if p, ok := s.Projects[key]; ok {
			v.Mode = p.Mode
			v.SkillNames = p.SkillNames
			switch p.Mode {
			case "custom":
				v.Selected = p.SkillNames
			case "off":
				v.Enabled = false
			}
		}
	}
	v.Selected = builtinsSelected(v.Selected)
	return s.Revision, v, nil
}
func (m *Manager) SaveSettings(workdir string, v SkillSettings) (uint64, error) {
	if v.Mode != "" && v.Mode != "inherit" && v.Mode != "custom" && v.Mode != "off" {
		return 0, errors.New("mode must be inherit, custom or off")
	}
	for _, n := range append(append([]string{}, v.Selected...), v.SkillNames...) {
		if err := cleanName(n); err != nil {
			return 0, err
		}
	}
	key := ""
	if workdir != "" {
		var err error
		key, err = projectKey(workdir)
		if err != nil {
			return 0, err
		}
	}
	managedWriteMu.Lock()
	defer managedWriteMu.Unlock()
	s, err := m.loadSettings()
	if err != nil {
		return 0, err
	}
	s.Revision++
	v.Selected = builtinsSelected(v.Selected)
	if key == "" {
		s.Global = v
	} else {
		if v.Mode == "" {
			v.Mode = "custom"
			v.SkillNames = v.Selected
		}
		s.Projects[key] = v
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return 0, err
	}
	f, err := os.CreateTemp(filepath.Dir(m.root), ".skills-settings-")
	if err != nil {
		return 0, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return 0, err
	}
	if err = f.Close(); err != nil {
		return 0, err
	}
	if err = os.Rename(f.Name(), m.settingsPath()); err != nil {
		return 0, err
	}
	return s.Revision, nil
}
func (m *Manager) Effective(workdir string) ([]Skill, SkillSettings, error) {
	_, set, err := m.Settings(workdir)
	if err != nil {
		return nil, set, err
	}
	if !set.Enabled {
		return nil, set, nil
	}
	all, _, err := m.List()
	if err != nil {
		return nil, set, err
	}
	wanted := map[string]bool{}
	for _, n := range set.Selected {
		wanted[n] = true
	}
	var out []Skill
	seen := map[string]bool{}
	// Existing project and user scan directories remain compatibility sources.
	dirs := DirsFor(workdir)
	for _, s := range Scan(dirs...) {
		if seen[s.Name] {
			continue
		}
		managed := within(m.root, s.Path)
		if managed && !wanted[s.Name] {
			continue
		}
		seen[s.Name] = true
		out = append(out, s)
	}
	for _, x := range all {
		if wanted[x.Name] && !seen[x.Name] {
			s, e := parse(filepath.Join(m.root, x.SkillFile))
			if e == nil {
				out = append(out, s)
				seen[x.Name] = true
			}
		}
	}
	return out, set, nil
}
func stringArg(a map[string]any, key string) string { v, _ := a[key].(string); return v }
func (m *Manager) Manage(ctx context.Context, args map[string]any) (ManagerResponse, error) {
	if err := ctx.Err(); err != nil {
		return ManagerResponse{}, err
	}
	a := stringArg(args, "action")
	if a == "" {
		a = "list"
		if stringArg(args, "path") != "" {
			a = "read"
		}
	}
	r := ManagerResponse{Action: a, RootDir: m.root}
	var err error
	switch a {
	case "list":
		r.Skills, r.Invalid, err = m.List()
	case "read":
		r.Path = stringArg(args, "path")
		offset := intArg(args, "offset", 0)
		r.Content, r.Truncated, r.NumLines, err = m.Read(r.Path, offset, intArg(args, "length", 200))
		r.StartLine = offset + 1
	case "validate":
		v := m.Validate(stringArg(args, "name"))
		r.Validation = &v
	case "delete":
		v, e := m.Delete(stringArg(args, "name"))
		r.Deleted = &v
		err = e
	case "package":
		v, e := m.Package(stringArg(args, "name"))
		r.Package = &v
		err = e
	case "create":
		c := stringArg(args, "conflict")
		if c == "" {
			c = "fail"
		}
		var files []map[string]string
		if raw, ok := args["files"]; ok {
			b, e := json.Marshal(raw)
			if e != nil {
				return r, e
			}
			if e = json.Unmarshal(b, &files); e != nil {
				return r, e
			}
		}
		v, e := m.Create(stringArg(args, "name"), stringArg(args, "description"), stringArg(args, "body"), c, files)
		r.Created = &v
		err = e
	case "install", "clawhub_install":
		if a == "clawhub_install" {
			slug, owner := stringArg(args, "slug"), stringArg(args, "ownerHandle")
			if slug == "" || owner == "" {
				err = errors.New("clawhub_install requires ownerHandle and slug")
			} else {
				args["source"] = fmt.Sprintf("https://clawhub.ai/api/v1/download?slug=%s&tag=%s&ownerHandle=%s", url.QueryEscape(slug), url.QueryEscape(versionOrLatest(stringArg(args, "version"))), url.QueryEscape(owner))
			}
		}
		v, e := m.installArgs(ctx, args, nil)
		r.Installed = []InstallResult{v}
		if err == nil {
			err = e
		}
	case "scan_external":
		r.External = m.External()
	case "scan_external_mcp":
		r.ExternalMCP = m.externalMCP()
	case "scan_mcp_file":
		v, e := scanMCPFile(stringArg(args, "path"))
		if e != nil {
			return r, e
		}
		r.ExternalMCP = []any{v}
	case "clawhub_search":
		r.ClawhubResults, r.ClawhubNextCursor, err = m.searchStore(ctx, args)
	case "install_start":
		return m.startJob(ctx, args, r)
	case "install_status", "install_cancel":
		id := stringArg(args, "jobId")
		if id == "" {
			id = stringArg(args, "job_id")
		}
		if a == "install_cancel" {
			r.InstallJob, err = m.cancelJob(id)
		} else {
			r.InstallJob, err = m.job(id)
		}
	default:
		err = fmt.Errorf("unknown SkillsManager action: %s", a)
	}
	return r, err
}

func intArg(a map[string]any, k string, d int) int {
	if n, ok := a[k].(float64); ok {
		return int(n)
	}
	if n, ok := a[k].(int); ok {
		return n
	}
	return d
}
func (m *Manager) startJob(ctx context.Context, a map[string]any, r ManagerResponse) (ManagerResponse, error) {
	id := fmt.Sprintf("skill-%d", time.Now().UnixNano())
	cctx, cancel := context.WithCancel(context.Background())
	now := time.Now().UnixMilli()
	j := &Job{JobID: id, Phase: "queued", Source: stringArg(a, "source"), StartedAt: now, UpdatedAt: now, cancel: cancel}
	m.jobsMu.Lock()
	for key, job := range m.jobs {
		if job.FinishedAt > 0 && now-job.FinishedAt > 3600000 {
			delete(m.jobs, key)
		}
	}
	m.jobs[id] = j
	snapshot := *j
	m.jobsMu.Unlock()
	copyArgs := map[string]any{}
	for k, v := range a {
		copyArgs[k] = v
	}
	go func() {
		defer cancel()
		m.jobsMu.Lock()
		j.Phase = "installing"
		m.jobsMu.Unlock()
		v, e := m.installArgs(cctx, copyArgs, func(downloaded, total int64) {
			m.jobsMu.Lock()
			defer m.jobsMu.Unlock()
			j.Phase = "downloading"
			j.DownloadedBytes = downloaded
			if total >= 0 {
				j.TotalBytes = &total
			}
			j.UpdatedAt = time.Now().UnixMilli()
		})
		m.jobsMu.Lock()
		defer m.jobsMu.Unlock()
		j.UpdatedAt = time.Now().UnixMilli()
		j.FinishedAt = j.UpdatedAt
		if e == nil {
			j.Phase = "completed"
			j.Installed = []InstallResult{v}
		} else if errors.Is(e, context.Canceled) || errors.Is(cctx.Err(), context.Canceled) {
			j.Phase = "cancelled"
		} else {
			j.Phase = "error"
			j.Error = e.Error()
		}
	}()
	r.InstallJob = &snapshot
	return r, nil
}

func (m *Manager) job(id string) (*Job, error) {
	m.jobsMu.Lock()
	defer m.jobsMu.Unlock()
	j := m.jobs[id]
	if j == nil {
		return nil, errors.New("install job not found")
	}
	snapshot := *j
	snapshot.Installed = append([]InstallResult{}, j.Installed...)
	return &snapshot, nil
}
func (m *Manager) cancelJob(id string) (*Job, error) {
	m.jobsMu.Lock()
	j := m.jobs[id]
	if j == nil {
		m.jobsMu.Unlock()
		return nil, errors.New("install job not found")
	}
	cancel := j.cancel
	m.jobsMu.Unlock()
	cancel()
	return m.job(id)
}

func publicHTTPClient() *http.Client {
	return &http.Client{Timeout: 10 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if req.URL.Scheme != "https" && req.URL.Hostname() != "127.0.0.1" && req.URL.Hostname() != "localhost" {
			return errors.New("redirect must use HTTPS")
		}
		return nil
	}}
}
func normalizeRegistryName(n string) string {
	n = strings.ToLower(strings.TrimSpace(n))
	var b strings.Builder
	lastDash := false
	for _, r := range n {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
func githubSource(raw string) (string, string, error) {
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.Host != "github.com" {
		return "", "", errors.New("source URL must be a GitHub URL")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("invalid GitHub repository")
	}
	owner, repo := parts[0], strings.TrimSuffix(parts[1], ".git")
	ref := "main"
	sub := ""
	if len(parts) >= 4 && parts[2] == "tree" {
		ref = parts[3]
		if len(parts) > 4 {
			sub = strings.Join(parts[4:], "/")
		}
	}
	if strings.ContainsAny(ref, "/\\..") || strings.ContainsAny(owner+repo, "/\\..") {
		return "", "", errors.New("invalid GitHub ref")
	}
	return fmt.Sprintf("https://codeload.github.com/%s/%s/zip/refs/heads/%s", owner, repo, url.PathEscape(ref)), sub, nil
}
func (m *Manager) download(ctx context.Context, raw string, progress func(int64, int64)) (string, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return "", e
	}
	resp, e := m.client.Do(req)
	if e != nil {
		return "", e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	f, e := os.CreateTemp(m.root, ".staging-download-")
	if e != nil {
		return "", e
	}
	defer f.Close()
	var total int64 = -1
	if resp.ContentLength >= 0 {
		total = resp.ContentLength
	}
	var got int64
	buf := make([]byte, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, er := resp.Body.Read(buf)
		if n > 0 {
			got += int64(n)
			if got > maxArchiveBytes {
				return "", errors.New("download is too large")
			}
			if _, e = f.Write(buf[:n]); e != nil {
				return "", e
			}
			if progress != nil {
				progress(got, total)
			}
		}
		if er == io.EOF {
			break
		}
		if er != nil {
			return "", er
		}
	}
	return f.Name(), nil
}
func (m *Manager) installArgs(ctx context.Context, a map[string]any, progress func(int64, int64)) (InstallResult, error) {
	meta := &SourceMetadata{}
	if owner := stringArg(a, "ownerHandle"); owner != "" {
		meta.Registry = "clawhub"
		meta.OwnerHandle = owner
		meta.Slug = stringArg(a, "slug")
		meta.Version = stringArg(a, "version")
	}
	return m.Install(ctx, stringArg(a, "source"), stringArg(a, "name"), stringArg(a, "conflict"), metaIf(meta), progress)
}
func versionOrLatest(v string) string {
	if strings.TrimSpace(v) == "" {
		return "latest"
	}
	return v
}
func metaIf(m *SourceMetadata) *SourceMetadata {
	if m.Registry == "" {
		return nil
	}
	return m
}
func (m *Manager) searchStore(ctx context.Context, a map[string]any) ([]ClawHubCard, string, error) {
	base := strings.TrimRight(m.storeURL, "/") + "/api/v1/search"
	q := url.Values{}
	q.Set("q", stringArg(a, "query"))
	q.Set("limit", "20")
	if owner := stringArg(a, "ownerHandle"); owner != "" {
		q.Set("ownerHandle", owner)
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, base+"?"+q.Encode(), nil)
	if e != nil {
		return nil, "", e
	}
	resp, e := m.client.Do(req)
	if e != nil {
		return nil, "", fmt.Errorf("ClawHub search: %w", e)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("ClawHub search returned HTTP %d", resp.StatusCode)
	}
	var root struct {
		Results []struct {
			Slug, DisplayName, Summary, LatestVersion, OwnerHandle, WebURL, DownloadURL string
			Downloads, Stars                                                            int64
		} `json:"results"`
		NextCursor string `json:"nextCursor"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, maxReadBytes)).Decode(&root); e != nil {
		return nil, "", e
	}
	out := make([]ClawHubCard, 0, len(root.Results))
	for _, x := range root.Results {
		out = append(out, ClawHubCard{Slug: x.Slug, DisplayName: x.DisplayName, Summary: x.Summary, LatestVersion: x.LatestVersion, OwnerHandle: x.OwnerHandle, WebURL: x.WebURL, DownloadURL: x.DownloadURL, Downloads: x.Downloads, Stars: x.Stars})
	}
	return out, root.NextCursor, nil
}
func (m *Manager) externalMCP() []any { return []any{} }
func scanMCPFile(path string) (map[string]any, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	i, err := os.Stat(p)
	if err != nil || !i.Mode().IsRegular() {
		return nil, errors.New("MCP config must be a regular file")
	}
	if i.Size() > 16<<20 {
		return nil, errors.New("MCP config is too large")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var v any
	if err = json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return map[string]any{"path": p, "config": v}, nil
}
