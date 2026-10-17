// Package goalloop holds the session-goal rules shared by the TUI and the backend:
// the continuation prompt sent after each turn, the GOAL_MET completion check, and
// the round limit resolution.
package goalloop

import (
	"fmt"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/config"
)

// MetToken is the literal a reply must lead with to declare the goal accomplished.
const MetToken = "GOAL_MET"

// MaxRounds resolves the continuation limit: a project override for dir wins over the
// global config value, which wins over the built-in default.
func MaxRounds(dir string, global int) int {
	if dir != "" {
		if n := config.ProjectGoalMaxRounds(dir); n > 0 {
			return n
		}
	}
	if global > 0 {
		return global
	}
	return config.DefaultGoalMaxRounds
}

// ContinuePrompt is the user turn submitted after every unfinished round.
func ContinuePrompt(goal string) string {
	return fmt.Sprintf(`[goal check] The session goal is:
%s

If the goal is FULLY accomplished and you have VERIFIED it with your tools (builds pass, tests pass, behavior confirmed), your reply MUST begin with the literal token %s as the very first characters — no preamble, no "Verified:" line, nothing before it. Example first line: "%s — shipped and verified."

Otherwise do not use the token %s at all — keep working toward the goal right now with your tools. If any part is incomplete, unverified, or you are unsure, that means keep working. Do not stop to ask questions; make reasonable assumptions and proceed.`, goal, MetToken, MetToken, MetToken)
}

// Met reports whether a final reply declares the goal accomplished. As the continuation
// prompt demands, the reply must lead with the token: only whitespace and Markdown
// emphasis, heading, quote or code markers may precede it, and it must end at a word
// boundary. A mention later in the reply ("not GOAL_MET yet") never counts.
func Met(final string) bool {
	head := strings.TrimLeft(final, " \t\r\n*_#>`~")
	if !strings.HasPrefix(head, MetToken) {
		return false
	}
	if rest := head[len(MetToken):]; rest != "" {
		c := rest[0]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			return false
		}
	}
	return true
}
