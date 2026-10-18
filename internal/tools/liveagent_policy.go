package tools

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// WorkspaceRoot is the backend's authoritative filesystem boundary.
type WorkspaceRoot struct {
	Path   string
	Access string
}

type workspaceRootsKey struct{}

// RunRoots is a run's workspace grant set. It can grow while the run is in
// flight, so a grant the user approves mid-turn applies to the same turn.
// Tools run concurrently, so every read takes a snapshot under the lock.
type RunRoots struct {
	mu    sync.RWMutex
	roots []WorkspaceRoot
}

func NewRunRoots(roots []WorkspaceRoot) *RunRoots {
	return &RunRoots{roots: append([]WorkspaceRoot(nil), roots...)}
}

// Snapshot returns a copy of the current grants.
func (r *RunRoots) Snapshot() []WorkspaceRoot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]WorkspaceRoot(nil), r.roots...)
}

// Extend adds grants and returns the resulting set. A path that is already
// granted is upgraded to write when requested and never downgraded, so an
// extension can only widen access the client explicitly asked for.
func (r *RunRoots) Extend(add []WorkspaceRoot) []WorkspaceRoot {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, grant := range add {
		grant.Path = filepath.Clean(grant.Path)
		existing := -1
		for i, root := range r.roots {
			if filepath.Clean(root.Path) == grant.Path {
				existing = i
				break
			}
		}
		switch {
		case existing < 0:
			r.roots = append(r.roots, grant)
		case grant.Access == "write":
			r.roots[existing].Access = "write"
		}
	}
	return append([]WorkspaceRoot(nil), r.roots...)
}

func WithWorkspaceRoots(ctx context.Context, roots []WorkspaceRoot) context.Context {
	return WithRunRoots(ctx, NewRunRoots(roots))
}

// WithRunRoots shares one grant holder with the run so later Extend calls are
// visible to every tool call that derives from ctx.
func WithRunRoots(ctx context.Context, roots *RunRoots) context.Context {
	return context.WithValue(ctx, workspaceRootsKey{}, roots)
}

// RunRootsFrom returns the grant holder installed on a run's context.
func RunRootsFrom(ctx context.Context) (*RunRoots, bool) { return runRoots(ctx) }

func runRoots(ctx context.Context) (*RunRoots, bool) {
	roots, ok := ctx.Value(workspaceRootsKey{}).(*RunRoots)
	return roots, ok && roots != nil
}

// WithWorktreeWorkspaceRoots transfers the parent's workspace grants to an
// isolated worktree. Preserve access levels and unrelated additional roots;
// an explicitly empty grant set must remain empty. The worktree gets its own
// remapped copy, so grants added to the parent run later are not inherited.
func WithWorktreeWorkspaceRoots(ctx context.Context, parent, worktree string) context.Context {
	holder, explicit := runRoots(ctx)
	if !explicit || !filepath.IsAbs(parent) || !filepath.IsAbs(worktree) {
		return ctx
	}
	roots := holder.Snapshot()
	mapped := make([]WorkspaceRoot, 0, len(roots))
	for _, root := range roots {
		grant := root
		if !filepath.IsAbs(root.Path) {
			mapped = append(mapped, grant)
			continue
		}
		if rel, err := filepath.Rel(parent, root.Path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			grant.Path = filepath.Join(worktree, rel)
		} else if rel, err := filepath.Rel(root.Path, parent); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			grant.Path = worktree
		}
		mapped = append(mapped, grant)
	}
	return WithWorkspaceRoots(ctx, mapped)
}

func workspaceRoots(ctx context.Context) []WorkspaceRoot {
	if holder, ok := runRoots(ctx); ok {
		return holder.Snapshot()
	}
	return nil
}

type skillReadRootsKey struct{}

// WithSkillReadRoots grants read-only access to the directories the system prompt
// advertises skills from. The prompt tells the model to read each skill's SKILL.md by
// absolute path, and those directories usually live outside the workspace. They extend
// the run's roots (or its default working-directory grant) and never replace them.
func WithSkillReadRoots(ctx context.Context, dirs []string) context.Context {
	return context.WithValue(ctx, skillReadRootsKey{}, append([]string(nil), dirs...))
}

// withSkillReadRoots appends advertised skill directories that no existing root already
// covers. A skill directory inside a writable workspace root keeps that write access.
func withSkillReadRoots(ctx context.Context, roots []WorkspaceRoot) []WorkspaceRoot {
	dirs, _ := ctx.Value(skillReadRootsKey{}).([]string)
	for _, dir := range dirs {
		dir = filepath.Clean(dir)
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		covered := false
		for _, root := range roots {
			if rootPath := filepath.Clean(root.Path); filepath.IsAbs(rootPath) && safeWorkspacePath(rootPath, dir) {
				covered = true
				break
			}
		}
		if !covered {
			roots = append(roots, WorkspaceRoot{Path: dir, Access: "read"})
		}
	}
	return roots
}

type liveagentNamedRootsKey struct{}

// WithLiveAgentNamedRoots maps root:// aliases and enabled skill:// names to
// explicit grants. Unmapped names never fall back to arbitrary host paths.
func WithLiveAgentNamedRoots(ctx context.Context, roots map[string]WorkspaceRoot) context.Context {
	copy := make(map[string]WorkspaceRoot, len(roots))
	for key, root := range roots {
		copy[key] = root
	}
	return context.WithValue(ctx, liveagentNamedRootsKey{}, copy)
}

func liveagentPath(ctx context.Context, raw string, access string, tool string, allowDirectory bool) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New(tool + ".path is required")
	}
	if strings.HasPrefix(raw, "file://") {
		u, err := url.Parse(raw)
		if err != nil || (u.Host != "" && u.Host != "localhost") {
			return "", errors.New("invalid local file URL")
		}
		raw = u.Path
	}
	if strings.HasPrefix(raw, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		raw = filepath.Join(home, raw[2:])
	}
	if strings.HasPrefix(raw, "root://") || strings.HasPrefix(raw, "skill://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", errors.New("invalid named root path")
		}
		named, _ := ctx.Value(liveagentNamedRootsKey{}).(map[string]WorkspaceRoot)
		grant, ok := named[u.Scheme+"://"+u.Host]
		if !ok {
			return "", errors.New("path root is not configured: " + raw)
		}
		if access == "write" && grant.Access != "write" {
			return "", errors.New("named root is read-only")
		}
		path := filepath.Join(grant.Path, filepath.FromSlash(strings.TrimPrefix(u.Path, "/")))
		if !filepath.IsAbs(grant.Path) || !safeWorkspacePath(grant.Path, path) {
			return "", errors.New("named root path escapes its grant")
		}
		return path, nil
	}
	if strings.Contains(raw, "://") {
		return "", errors.New("path root is not configured: " + raw)
	}
	path := ResolvePath(ctx, raw)
	if !filepath.IsAbs(path) {
		return "", errors.New(tool + ".path must resolve to an absolute path")
	}
	path = filepath.Clean(path)
	roots := append([]WorkspaceRoot(nil), workspaceRoots(ctx)...)
	named, _ := ctx.Value(liveagentNamedRootsKey{}).(map[string]WorkspaceRoot)
	for _, grant := range named {
		roots = append(roots, grant)
	}
	if len(roots) == 0 {
		if _, explicit := runRoots(ctx); explicit {
			return "", errors.New("no workspace roots are authorized")
		}
		if WorkingDir(ctx) == "" {
			return "", errors.New("working directory is required")
		}
		roots = []WorkspaceRoot{{Path: WorkingDir(ctx), Access: "write"}}
	}
	roots = withSkillReadRoots(ctx, roots)
	if access == "write" {
		for _, root := range roots {
			if root.Access != "write" && safeWorkspacePath(filepath.Clean(root.Path), path) {
				return "", &PathPolicyError{
					Code: PathReadOnlyRoot, Path: path, Access: access, Roots: roots,
					message: "path belongs to a read-only workspace root",
				}
			}
		}
	}
	for _, root := range roots {
		rootPath := filepath.Clean(root.Path)
		if rootPath == "" || !filepath.IsAbs(rootPath) || (access == "write" && root.Access != "write") {
			continue
		}
		if safeWorkspacePath(rootPath, path) {
			if !allowDirectory {
				if info, err := os.Stat(path); err == nil && !info.Mode().IsRegular() {
					return "", errors.New(tool + ".path must identify a regular file")
				}
			}
			return path, nil
		}
	}
	message := tool + ".path is outside the authorized workspace roots"
	if driveRootedPath(raw) {
		message += " (resolved to " + path +
			`; on Windows a path starting with "/" or "\" is relative to the drive root, not the workspace or the system temp directory)`
	}
	return "", &PathPolicyError{Code: PathOutsideRoots, Path: path, Access: access, Roots: roots, message: message}
}

// Structured denial codes carried by PathPolicyError.
const (
	PathOutsideRoots = "path_outside_roots"
	PathReadOnlyRoot = "path_read_only_root"
)

// PathPolicyError is a workspace-root denial a client can act on, e.g. by
// offering to grant Path. Error() keeps the historical text the model sees.
type PathPolicyError struct {
	Code    string
	Path    string
	Access  string
	Roots   []WorkspaceRoot
	message string
}

func (e *PathPolicyError) Error() string { return e.message }

func safeWorkspacePath(root, target string) bool {
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return false
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return false
	}
	if rel == "." {
		return true
	}
	current := root
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			// Missing descendants are valid targets for Write; the lexical root
			// boundary still prevents traversal and existing symlink escapes.
			return true
		}
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	return true
}
