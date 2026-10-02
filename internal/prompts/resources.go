package prompts

import (
	"fmt"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/resources"
)

// WithResources applies LiveAgent prompt resources without changing existing AGENTS/brain semantics.
func WithResources(base, workdir string, store *resources.PromptStore) (string, error) {
	if store == nil {
		return base, nil
	}
	global, project, selected, err := store.Resolve(workdir, nil)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(selected) == "" {
		return base, nil
	}
	projectPrompt := strings.TrimSpace(project.Prompt)
	if projectPrompt != "" && project.Strategy == "replace" {
		return base + "\n\nProject agent prompt (replace):\n" + projectPrompt, nil
	}
	if global != "" && projectPrompt != "" {
		return base + "\n\nGlobal agent prompt:\n" + global + "\n\nProject agent prompt:\n" + projectPrompt, nil
	}
	if projectPrompt != "" {
		return fmt.Sprintf("%s\n\nProject agent prompt:\n%s", base, projectPrompt), nil
	}
	return fmt.Sprintf("%s\n\nGlobal agent prompt:\n%s", base, selected), nil
}
