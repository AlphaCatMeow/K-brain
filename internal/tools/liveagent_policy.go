package tools

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceRoot is the backend's authoritative filesystem boundary.
type WorkspaceRoot struct {
	Path   string
	Access string
}

type workspaceRootsKey struct{}

func WithWorkspaceRoots(ctx context.Context, roots []WorkspaceRoot) context.Context {
	return context.WithValue(ctx, workspaceRootsKey{}, append([]WorkspaceRoot(nil), roots...))
}

func workspaceRoots(ctx context.Context) []WorkspaceRoot {
	roots, _ := ctx.Value(workspaceRootsKey{}).([]WorkspaceRoot)
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
		if _, explicit := ctx.Value(workspaceRootsKey{}).([]WorkspaceRoot); explicit {
			return "", errors.New("no workspace roots are authorized")
		}
		if WorkingDir(ctx) == "" {
			return "", errors.New("working directory is required")
		}
		roots = []WorkspaceRoot{{Path: WorkingDir(ctx), Access: "write"}}
	}
	if access == "write" {
		for _, root := range roots {
			if root.Access != "write" && safeWorkspacePath(filepath.Clean(root.Path), path) {
				return "", errors.New("path belongs to a read-only workspace root")
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
	return "", errors.New(tool + ".path is outside the authorized workspace roots")
}

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
