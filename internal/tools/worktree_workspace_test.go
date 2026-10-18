package tools

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func TestWorktreeWorkspaceRootsPreserveBoundaries(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "repo")
	child := filepath.Join(base, "repo-wt")
	extra := filepath.Join(base, "shared")
	for _, tc := range []struct {
		name          string
		before, after []WorkspaceRoot
	}{
		{"primary and additional", []WorkspaceRoot{{parent, "write"}, {extra, "read"}}, []WorkspaceRoot{{child, "write"}, {extra, "read"}}},
		{"read only primary", []WorkspaceRoot{{parent, "read"}}, []WorkspaceRoot{{child, "read"}}},
		{"nested read only", []WorkspaceRoot{{parent, "write"}, {filepath.Join(parent, "protected"), "read"}}, []WorkspaceRoot{{child, "write"}, {filepath.Join(child, "protected"), "read"}}},
		{"ancestor grant", []WorkspaceRoot{{base, "read"}}, []WorkspaceRoot{{child, "read"}}},
		{"empty", []WorkspaceRoot{}, []WorkspaceRoot{}},
		{"invalid relative grant", []WorkspaceRoot{{".", "write"}}, []WorkspaceRoot{{".", "write"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := WithWorkspaceRoots(context.Background(), tc.before)
			mapped := WithWorktreeWorkspaceRoots(ctx, parent, child)
			if got := workspaceRoots(mapped); !reflect.DeepEqual(got, tc.after) && !(len(got) == 0 && len(tc.after) == 0) {
				t.Fatalf("roots=%v want %v", got, tc.after)
			}
			if !reflect.DeepEqual(workspaceRoots(ctx), tc.before) && len(tc.before) > 0 {
				t.Fatal("parent grants mutated")
			}
			if tc.name == "empty" {
				if _, explicit := runRoots(mapped); !explicit {
					t.Fatal("explicit denial lost")
				}
			}
		})
	}
	ctx := context.Background()
	if got := WithWorktreeWorkspaceRoots(ctx, parent, child); got != ctx {
		t.Fatal("implicit working-directory grant changed")
	}
}
