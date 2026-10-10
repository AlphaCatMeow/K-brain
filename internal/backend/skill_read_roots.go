package backend

import (
	"os"
	"path/filepath"

	"github.com/Stack-Cairn/K-brain/internal/skills"
)

// skillReadRoots lists the directories the system prompt can advertise skills from for
// this workspace (see prompts.SkillsPrompt): the managed user Skills root plus the
// project/user compatibility scan directories. Only existing directories are returned.
func skillReadRoots(workdir string) []string {
	var dirs []string
	if manager, err := skills.NewManager(); err == nil && manager.Root() != "" {
		dirs = append(dirs, manager.Root())
	}
	dirs = append(dirs, skills.DirsFor(workdir)...)
	seen := map[string]bool{}
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		dir = filepath.Clean(dir)
		if dir == "" || seen[dir] {
			continue
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	return out
}
