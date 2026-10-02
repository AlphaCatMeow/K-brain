package session

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	historySearchMaxSessions  = 1000
	historySearchMaxFileBytes = 8 << 20
	historySearchMaxBytes     = 64 << 20
)

type HistorySearchMatch struct {
	SessionID     string    `json:"session_id"`
	Title         string    `json:"title"`
	CWD           string    `json:"cwd"`
	MessageID     string    `json:"message_id,omitempty"`
	MessageOffset *int      `json:"message_offset,omitempty"`
	Role          string    `json:"role,omitempty"`
	Snippet       string    `json:"snippet"`
	Score         float64   `json:"score"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type HistorySearchResult struct {
	Matches   []HistorySearchMatch `json:"matches"`
	Truncated bool                 `json:"truncated"`
}

// SearchHistory scans one bounded transcript at a time; it never loads all histories.
func (s *Store) SearchHistory(ctx context.Context, query string, limit int) (HistorySearchResult, error) {
	out := HistorySearchResult{Matches: []HistorySearchMatch{}}
	query = strings.ToLower(strings.TrimSpace(query))
	if limit < 1 || limit > 200 || len(query) > 4096 {
		return out, fmt.Errorf("invalid history search query or limit")
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if query == "" {
		return out, nil
	}
	err := s.withLock(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		ids, err := s.ids()
		if err != nil {
			return err
		}
		if len(ids) > historySearchMaxSessions {
			out.Truncated = true
			ids = ids[:historySearchMaxSessions]
		}
		var bytesRead int64
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := os.Stat(s.TranscriptPath(id))
			if err != nil {
				return err
			}
			if info.Size() > historySearchMaxFileBytes || bytesRead+info.Size() > historySearchMaxBytes {
				out.Truncated = true
				continue
			}
			bytesRead += info.Size()
			d, err := s.read(id)
			if err != nil {
				return err
			}
			if d.Meta.Archived || d.Meta.TaskID != "" {
				continue
			}
			match := HistorySearchMatch{SessionID: id, Title: d.Meta.Title, CWD: d.Meta.CWD, UpdatedAt: d.Meta.UpdatedAt}
			if strings.Contains(strings.ToLower(d.Meta.Title), query) {
				match.Snippet, match.Score = historySearchSnippet(d.Meta.Title, query), 2
			}
			for _, offset := range sortedKeys(d.Messages) {
				if err := ctx.Err(); err != nil {
					return err
				}
				msg := d.Messages[offset]
				if msg.Role != "user" && msg.Role != "assistant" && msg.Role != "tool" {
					continue
				}
				text := msg.TextContent()
				if !strings.Contains(strings.ToLower(text), query) {
					continue
				}
				match.MessageID, match.MessageOffset, match.Role = canonicalAt(id, offset, msg), &offset, msg.Role
				match.Snippet = historySearchSnippet(text, query)
				if match.Score == 0 {
					match.Score = 1
				}
				break
			}
			if match.Score == 0 {
				continue
			}
			out.Matches = append(out.Matches, match)
			sort.Slice(out.Matches, func(i, j int) bool {
				a, b := out.Matches[i], out.Matches[j]
				if a.Score != b.Score {
					return a.Score > b.Score
				}
				if !a.UpdatedAt.Equal(b.UpdatedAt) {
					return a.UpdatedAt.After(b.UpdatedAt)
				}
				return a.SessionID < b.SessionID
			})
			if len(out.Matches) > limit {
				out.Matches = out.Matches[:limit]
				out.Truncated = true
			}
		}
		return ctx.Err()
	})
	return out, err
}

func historySearchSnippet(text, query string) string {
	runes := []rune(text)
	lower := []rune(strings.ToLower(text))
	index := strings.Index(string(lower), query)
	start := 0
	if index >= 0 {
		start = max(0, len([]rune(string(lower)[:index]))-80)
	}
	end := min(len(runes), start+320)
	return string(runes[start:end])
}
