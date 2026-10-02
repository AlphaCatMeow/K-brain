package memory

import (
	"sort"
	"strings"
	"time"
)

func quotas(entries []storedEntry, workdir string) (QuotaSummaryResponse, error) {
	hash, err := scopeHash(workdir, "")
	if err != nil {
		return QuotaSummaryResponse{}, err
	}
	out := QuotaSummaryResponse{Scopes: []ScopeQuota{{Scope: "global", Limit: ScopeLimit, Headroom: ScopeLimit}}}
	if hash != "" {
		out.Scopes = append(out.Scopes, ScopeQuota{Scope: "project", WorkdirHash: hash, Limit: ScopeLimit, Headroom: ScopeLimit})
	}
	for _, e := range entries {
		if e.MemoryType == "daily" {
			continue
		}
		for i := range out.Scopes {
			q := &out.Scopes[i]
			if q.Scope != e.Scope || q.WorkdirHash != e.WorkdirHash {
				continue
			}
			if e.Archived {
				q.Archived++
				continue
			}
			q.Used++
			q.Headroom = ScopeLimit - q.Used
			if e.Unreviewed {
				q.UnreviewedCount++
				age := float64(time.Now().UnixMilli()-e.UpdatedAt) / 86400000
				if q.OldestUnreviewedAgeDays == nil || age > *q.OldestUnreviewedAgeDays {
					q.OldestUnreviewedAgeDays = &age
				}
			}
		}
	}
	return out, nil
}
func (s *Store) QuotaSummary(workdir string) (QuotaSummaryResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.load()
	if err != nil {
		return QuotaSummaryResponse{}, err
	}
	return quotas(entries, workdir)
}
func filterEntries(entries []storedEntry, args ListArgs) ([]storedEntry, error) {
	if err := validateScope(args.Scope, true); err != nil {
		return nil, err
	}
	hash, err := scopeHash(args.Workdir, "")
	if err != nil {
		return nil, err
	}
	if args.Scope == "project" && hash == "" && !args.IncludeAllProjects {
		return nil, storeError("workdir_required", "project memory requires workdir")
	}
	out := []storedEntry{}
	for _, e := range entries {
		if args.Scope == "global" && e.Scope != "global" || args.Scope == "project" && e.Scope != "project" {
			continue
		}
		if e.Scope == "project" && !args.IncludeAllProjects && e.WorkdirHash != hash {
			continue
		}
		if args.MemoryType != "" && e.MemoryType != args.MemoryType {
			continue
		}
		if e.MemoryType == "daily" && !args.IncludeDaily && args.MemoryType != "daily" {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}
func (s *Store) List(args ListArgs) (ListResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := ListResponse{Entries: []Meta{}}
	if args.Offset < 0 || args.Limit < 0 {
		return out, storeError("invalid_window", "negative list window")
	}
	entries, err := s.load()
	if err != nil {
		return out, err
	}
	filtered, err := filterEntries(entries, args)
	if err != nil {
		return out, err
	}
	q, err := quotas(entries, args.Workdir)
	if err != nil {
		return out, err
	}
	out.Quota = Quota{ScopeQuotas: q.Scopes}
	for _, v := range q.Scopes {
		out.Quota.Used += v.Used
		out.Quota.Limit += v.Limit
	}
	limit := args.Limit
	if limit == 0 {
		limit = 100
	}
	limit = min(limit, 10000)
	start := min(args.Offset, len(filtered))
	end := min(start+limit, len(filtered))
	for _, e := range filtered[start:end] {
		out.Entries = append(out.Entries, e.Meta)
	}
	out.Truncated = end < len(filtered)
	return out, nil
}
func (s *Store) Search(args SearchArgs) (SearchResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := SearchResponse{Matches: []SearchMatch{}, HistoryMatches: []any{}, UsedFallback: true}
	if strings.TrimSpace(args.Query) == "" {
		return out, storeError("query_required", "search query required")
	}
	if args.IncludeHistory {
		return out, storeError("history_search_required", "history search must be supplied by the session search adapter")
	}
	entries, err := s.load()
	if err != nil {
		return out, err
	}
	entries, err = filterEntries(entries, ListArgs{Scope: args.Scope, Workdir: args.Workdir, MemoryType: args.MemoryType, IncludeDaily: true})
	if err != nil {
		return out, err
	}
	for _, e := range entries {
		content := strings.ToLower(e.Description + "\n" + e.Headline + "\n" + e.body)
		query := strings.ToLower(strings.TrimSpace(args.Query))
		count := strings.Count(content, query)
		if count == 0 {
			continue
		}
		score := float64(count)
		age := float64(time.Now().UnixMilli()-e.UpdatedAt) / 86400000
		if e.MemoryType == "daily" {
			score *= 0.35 / (1 + max(age, 0)/30)
		}
		if e.Unreviewed {
			score *= 0.8
		}
		if e.Archived {
			score *= 0.5
		}
		snippet := []rune(e.body)
		if len(snippet) > 400 {
			snippet = snippet[:400]
		}
		raw := float64(count)
		out.Matches = append(out.Matches, SearchMatch{Slug: e.Slug, Scope: e.Scope, WorkdirHash: e.WorkdirHash, MemoryType: e.MemoryType, Description: e.Description, Headline: e.Headline, Snippet: string(snippet), Score: score, RawScore: &raw, AgeDays: &age, Unreviewed: e.Unreviewed, Confidence: e.Confidence})
	}
	sort.SliceStable(out.Matches, func(i, j int) bool { return out.Matches[i].Score > out.Matches[j].Score })
	limit := args.Limit
	if limit <= 0 {
		limit = 10
	}
	out.Matches = out.Matches[:min(len(out.Matches), min(limit, 32))]
	return out, nil
}
func (s *Store) Overview(workdir string) (OverviewResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := OverviewResponse{Root: s.root, User: []Meta{}, Project: []Meta{}, Global: []Meta{}, RecentDays: []Meta{}}
	entries, err := s.load()
	if err != nil {
		return out, err
	}
	entries, err = filterEntries(entries, ListArgs{Workdir: workdir, IncludeDaily: true})
	if err != nil {
		return out, err
	}
	if workdir != "" {
		hash, _ := ProjectHash(workdir)
		out.WorkdirHash = &hash
	}
	shadow := map[string]bool{}
	for _, e := range entries {
		if e.Scope == "project" && !e.Archived {
			shadow[e.Slug] = true
		}
	}
	for _, e := range entries {
		if e.Archived || (e.Scope == "global" && shadow[e.Slug]) {
			continue
		}
		var bucket *[]Meta
		switch {
		case e.MemoryType == "daily":
			bucket = &out.RecentDays
		case e.Scope == "project":
			bucket = &out.Project
		case e.MemoryType == "user" || e.MemoryType == "feedback":
			bucket = &out.User
		default:
			bucket = &out.Global
		}
		if len(*bucket) < 30 {
			*bucket = append(*bucket, e.Meta)
		}
	}
	return out, nil
}
