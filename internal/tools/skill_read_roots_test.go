package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSkillReadRootsAllowReadingAdvertisedSkillFiles(t *testing.T) {
	workspace := t.TempDir()
	skills := t.TempDir()
	skillFile := filepath.Join(skills, "demo", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skillFile, []byte("---\nname: demo\n---\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), workspace),
		[]WorkspaceRoot{{Path: workspace, Access: "write"}})

	// Before: the prompt advertises the file, but the root policy rejects it.
	if _, err := liveagentPath(ctx, skillFile, "read", "Read", false); err == nil ||
		!strings.Contains(err.Error(), "outside the authorized workspace roots") {
		t.Fatalf("expected rejection without skill roots, got %v", err)
	}

	ctx = WithSkillReadRoots(ctx, []string{skills})
	got, err := liveagentPath(ctx, skillFile, "read", "Read", false)
	if err != nil || got != filepath.Clean(skillFile) {
		t.Fatalf("read advertised skill: path=%q err=%v", got, err)
	}
	// Read-only: writes still have to go through SkillsManager.
	if _, err := liveagentPath(ctx, skillFile, "write", "Write", false); err == nil {
		t.Fatal("skill read root allowed a write")
	}
	// The workspace keeps its write access.
	if _, err := liveagentPath(ctx, filepath.Join(workspace, "a.txt"), "write", "Write", false); err != nil {
		t.Fatalf("workspace write: %v", err)
	}
}

func TestSkillReadRootsKeepDefaultWorkingDirGrant(t *testing.T) {
	workspace := t.TempDir()
	skills := t.TempDir()
	// No explicit roots: the working directory is the implicit writable root.
	ctx := WithSkillReadRoots(WithWorkingDir(context.Background(), workspace), []string{skills})
	if _, err := liveagentPath(ctx, filepath.Join(workspace, "a.txt"), "write", "Write", false); err != nil {
		t.Fatalf("default working-dir grant displaced: %v", err)
	}
	if _, err := liveagentPath(ctx, filepath.Join(skills, "x", "SKILL.md"), "read", "Read", false); err != nil {
		t.Fatalf("skill read with default grant: %v", err)
	}
}

func TestSkillReadRootsDoNotDowngradeWorkspaceSkills(t *testing.T) {
	workspace := t.TempDir()
	projectSkills := filepath.Join(workspace, ".agents", "skills")
	if err := os.MkdirAll(projectSkills, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := WithWorkspaceRoots(WithWorkingDir(context.Background(), workspace),
		[]WorkspaceRoot{{Path: workspace, Access: "write"}})
	ctx = WithSkillReadRoots(ctx, []string{projectSkills})
	// A skill directory inside a writable root must stay writable.
	if _, err := liveagentPath(ctx, filepath.Join(projectSkills, "mine", "SKILL.md"), "write", "Write", false); err != nil {
		t.Fatalf("workspace skill dir became read-only: %v", err)
	}
}

func TestSkillReadRootsRespectExplicitlyEmptyRoots(t *testing.T) {
	skills := t.TempDir()
	ctx := WithSkillReadRoots(WithWorkspaceRoots(WithWorkingDir(context.Background(), t.TempDir()), []WorkspaceRoot{}),
		[]string{skills})
	if _, err := liveagentPath(ctx, filepath.Join(skills, "x", "SKILL.md"), "read", "Read", false); err == nil {
		t.Fatal("skill roots bypassed an explicit no-filesystem grant")
	}
}

func TestWindowsDriveRootedPathsResolveAgainstTheDriveRoot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("drive-rooted paths only exist on Windows")
	}
	workspace := t.TempDir()
	volume := filepath.VolumeName(workspace)
	ctx := WithWorkingDir(context.Background(), workspace)
	for _, raw := range []string{"/tmp/missing.txt", `\tmp\missing.txt`} {
		if got, want := ResolvePath(ctx, raw), filepath.Join(volume, `\tmp\missing.txt`); got != want {
			t.Fatalf("ResolvePath(%q)=%q, want %q", raw, got, want)
		}
	}
	// UNC and workspace-relative paths keep their meaning.
	if got := ResolvePath(ctx, "sub/file.txt"); got != filepath.Join(workspace, "sub", "file.txt") {
		t.Fatalf("relative path=%q", got)
	}
	if !filepath.IsAbs(`\\server\share\x`) || driveRootedPath(`\\server\share\x`) {
		t.Fatal("UNC path treated as drive-rooted")
	}

	ctx = WithWorkspaceRoots(ctx, []WorkspaceRoot{{Path: workspace, Access: "write"}})
	_, err := liveagentPath(ctx, "/tmp/missing.txt", "read", "Read", false)
	if err == nil {
		t.Fatal("drive-rooted path outside the workspace was allowed")
	}
	if !strings.Contains(err.Error(), "resolved to "+filepath.Join(volume, `\tmp\missing.txt`)) ||
		!strings.Contains(err.Error(), "drive root") {
		t.Fatalf("error does not explain the resolution: %v", err)
	}
}
