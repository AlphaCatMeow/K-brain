package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func yamlQuote(v string) string {
	if v == "" {
		return `""`
	}
	if strings.ContainsAny(v, ":#\n\r\t\"'") || strings.HasPrefix(v, " ") || strings.HasSuffix(v, " ") {
		b, _ := json.Marshal(v)
		return string(b)
	}
	return v
}
func evidenceValues(e *Evidence) (confidence string, downgraded bool) {
	confidence = "low"
	quote := ""
	if e != nil {
		confidence = strings.ToLower(strings.TrimSpace(e.Confidence))
		quote = strings.TrimSpace(e.SourceQuote)
	}
	if confidence != "high" && confidence != "medium" {
		if confidence != "low" {
			confidence = "low"
		}
	}
	if confidence == "high" && len([]rune(quote)) < 5 {
		confidence = "medium"
		downgraded = true
	}
	if confidence == "medium" && quote == "" {
		confidence = "low"
		downgraded = true
	}
	return
}
func renderEntry(e storedEntry, body string, evidence *Evidence, now int64) (string, string, bool) {
	confidence, downgraded := evidenceValues(evidence)
	if evidence == nil && e.Confidence != "" {
		confidence = e.Confidence
	}
	source := make(map[string]any, len(e.source)+1)
	for k, v := range e.source {
		source[k] = v
	}
	source["unreviewed"] = e.Unreviewed
	lines := []string{"---", fmt.Sprintf("name: %s", yamlQuote(e.Slug)), fmt.Sprintf("type: %s", yamlQuote(e.MemoryType)), fmt.Sprintf("scope: %s", yamlQuote(e.Scope))}
	if e.Description != "" {
		lines = append(lines, fmt.Sprintf("description: %s", yamlQuote(e.Description)))
	}
	if e.Headline != "" {
		lines = append(lines, fmt.Sprintf("headline: %s", yamlQuote(e.Headline)))
	}
	if e.DateLocal != nil {
		lines = append(lines, "date: "+yamlQuote(*e.DateLocal))
	}
	if e.CreatedAt == 0 {
		e.CreatedAt = now
	}
	e.UpdatedAt = now
	lines = append(lines, fmt.Sprintf("createdAt: %s", time.UnixMilli(e.CreatedAt).UTC().Format(time.RFC3339Nano)), fmt.Sprintf("updatedAt: %s", time.UnixMilli(e.UpdatedAt).UTC().Format(time.RFC3339Nano)))
	if e.MemoryType == "daily" {
		lines = append(lines, fmt.Sprintf("appendCount: %d", e.AppendCount))
	} else {
		lines = append(lines, "source:")
		for _, k := range []string{"trigger", "conversationId", "model", "unreviewed", "actor"} {
			if v, ok := source[k]; ok {
				lines = append(lines, fmt.Sprintf("  %s: %s", k, yamlQuote(fmt.Sprint(v))))
			}
		}
		if evidence != nil {
			lines = append(lines, "  evidence: true")
		}
	}
	if evidence != nil {
		lines = append(lines, "evidence:", "  confidence: "+confidence, "  source_quote: "+yamlQuote(evidence.SourceQuote), "  reasoning: "+yamlQuote(evidence.Reasoning), "  aliases: "+yamlQuote(strings.Join(evidence.Aliases, ", ")), "  conflicts_with: "+yamlQuote(strings.Join(evidence.ConflictsWith, ", ")), "  supersedes: "+yamlQuote(evidence.Supersedes), "  override_reject: "+yamlQuote(evidence.OverrideReject))
		if downgraded {
			lines = append(lines, "  auto_downgraded: true")
		}
	}
	lines = append(lines, "links: []", "---", "", strings.TrimSpace(body), "")
	return strings.Join(lines, "\n"), confidence, downgraded
}
func entryPath(root string, e storedEntry, workdir string) (string, error) {
	if e.MemoryType == "daily" {
		date := e.Slug
		if strings.HasPrefix(date, "daily-") {
			date = strings.TrimPrefix(date, "daily-")
		}
		if date == "" {
			date = time.Now().Format("2006-01-02")
		}
		return filepath.Join(root, "global", "daily", date+".md"), nil
	}
	if e.Scope == "global" {
		dir := filepath.Join(root, "global")
		if e.MemoryType == "user" || e.MemoryType == "feedback" {
			dir = filepath.Join(dir, "user")
		}
		return filepath.Join(dir, e.Slug+".md"), nil
	}
	hash, err := scopeHash(workdir, e.WorkdirHash)
	if err != nil {
		return "", err
	}
	if hash == "" {
		return "", storeError("workdir_required", "project memory requires workdir")
	}
	if workdir != "" {
		marker := filepath.Join(root, "projects", hash, ".workdir.json")
		if _, err := os.Stat(marker); os.IsNotExist(err) {
			data, _ := json.Marshal(map[string]string{"path": workdir, "createdAt": time.Now().UTC().Format(time.RFC3339Nano)})
			if err := atomicStoreWrite(marker, data); err != nil {
				return "", err
			}
		}
	}
	return filepath.Join(root, "projects", hash, e.Slug+".md"), nil
}
func (s *Store) validateWrite(e storedEntry, body string) error {
	if err := validateSlug(e.Slug); err != nil {
		return err
	}
	if e.Scope != "global" && e.Scope != "project" {
		return storeError("invalid_scope", "scope must be global or project")
	}
	if e.MemoryType == "daily" {
		return storeError("invalid_type", "daily is append-only")
	}
	if e.MemoryType != "user" && e.MemoryType != "feedback" && e.MemoryType != "project" && e.MemoryType != "reference" {
		return storeError("invalid_type", "unsupported memory type")
	}
	if strings.TrimSpace(body) == "" {
		return storeError("body_required", "memory body required")
	}
	if len([]byte(body)) > 8*1024 {
		return storeError("body_too_large", "memory body exceeds 8 KiB")
	}
	return nil
}
func (s *Store) writeLocked(args WriteArgs) (MutationResponse, error) {
	return s.writeLockedReview(args, false)
}

func (s *Store) writeDailyLocked(args WriteArgs) (MutationResponse, error) {
	return s.writeLockedReview(args, true)
}

func (s *Store) writeLockedReview(args WriteArgs, allowDaily bool) (MutationResponse, error) {
	now := time.Now().UnixMilli()
	entries, err := s.load()
	if err != nil {
		return MutationResponse{}, err
	}
	hash, err := scopeHash(args.Workdir, "")
	if err != nil {
		return MutationResponse{}, err
	}
	e := storedEntry{Meta: Meta{Slug: args.Slug, Scope: args.Scope, WorkdirHash: hash, MemoryType: args.MemoryType, Description: args.Description, Headline: args.Description, Unreviewed: true, CreatedAt: now, UpdatedAt: now}, body: args.Body, source: map[string]any{"trigger": args.Actor, "conversationId": args.ConversationID, "model": args.Model, "actor": args.Actor}}
	if args.Unreviewed != nil {
		e.Unreviewed = *args.Unreviewed
	}
	if allowDaily {
		if e.MemoryType != "daily" || e.Scope != "global" {
			return MutationResponse{}, storeError("invalid_daily", "daily entries require global daily scope")
		}
		if strings.TrimSpace(args.Body) == "" || len([]byte(args.Body)) > 8*1024 {
			return MutationResponse{}, storeError("body_invalid", "daily body is empty or exceeds 8 KiB")
		}
	} else if err := s.validateWrite(e, args.Body); err != nil {
		return MutationResponse{}, err
	}
	found := false
	for _, old := range entries {
		if old.Slug == e.Slug && old.Scope == e.Scope && old.WorkdirHash == e.WorkdirHash {
			found = true
			e = old
			if args.Unreviewed != nil {
				e.Unreviewed = *args.Unreviewed
			}
			e.Description = args.Description
			e.Headline = args.Description
			e.body = args.Body
			break
		}
	}
	if args.Evidence == nil && e.Confidence == "" {
		e.Confidence = "unknown"
	}
	if args.Evidence == nil && e.evidence != nil {
		copy := *e.evidence
		copy.Aliases = append([]string(nil), e.evidence.Aliases...)
		copy.ConflictsWith = append([]string(nil), e.evidence.ConflictsWith...)
		args.Evidence = &copy
	} else if args.Evidence == nil && (e.Confidence == "high" || e.Confidence == "medium" || e.Confidence == "low") {
		args.Evidence = &Evidence{Confidence: e.Confidence}
	}
	if e.MemoryType == "daily" {
		if found {
			e.AppendCount++
		} else {
			e.AppendCount = 1
		}
	}
	if !found && e.MemoryType != "daily" {
		count := 0
		for _, old := range entries {
			if old.MemoryType != "daily" && !old.Archived && old.Scope == e.Scope && old.WorkdirHash == e.WorkdirHash {
				count++
			}
		}
		if count >= ScopeLimit {
			return MutationResponse{}, storeError("quota_exceeded", "memory quota exceeded")
		}
	}
	rendered, confidence, downgraded := renderEntry(e, args.Body, args.Evidence, now)
	path, err := entryPath(s.root, e, args.Workdir)
	if err != nil {
		return MutationResponse{}, err
	}
	if err := s.safe(path); err != nil {
		return MutationResponse{}, err
	}
	if err := atomicStoreWrite(path, []byte(rendered)); err != nil {
		return MutationResponse{}, err
	}
	// An entry loaded from a legacy project directory is now stored under the
	// current id; drop the superseded copy so the project does not split.
	if found && e.legacyDir != "" && e.path != "" && e.path != path {
		if err := s.safe(e.path); err != nil {
			return MutationResponse{}, err
		}
		if err := os.Remove(e.path); err != nil && !os.IsNotExist(err) {
			return MutationResponse{}, err
		}
	}
	return MutationResponse{Slug: e.Slug, Scope: e.Scope, Created: !found, Updated: found, IndexUpdated: true, AppliedConfidence: confidence, AutoDowngraded: &downgraded}, nil
}
func (s *Store) Write(args WriteArgs) (MutationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeLocked(args)
}
func (s *Store) updateLocked(args UpdateArgs) (MutationResponse, error) {
	entries, err := s.load()
	if err != nil {
		return MutationResponse{}, err
	}
	old, err := findEntry(entries, args.ReadArgs)
	if err != nil {
		return MutationResponse{}, err
	}
	body := old.body
	if args.Body != nil {
		switch args.Mode {
		case "append":
			body = strings.TrimSpace(body) + "\n\n" + strings.TrimSpace(*args.Body)
		case "merge":
			body = mergeBody(body, *args.Body)
		default:
			body = *args.Body
		}
	}
	if args.Description != nil {
		old.Description = *args.Description
	}
	if args.MemoryType != nil {
		old.MemoryType = *args.MemoryType
	}
	scope := old.Scope
	if args.Scope != "" && args.Scope != "auto" {
		scope = args.Scope
	}
	workdir := args.Workdir
	if workdir == "" {
		workdir = old.WorkdirPath
	}
	r, err := s.writeLocked(WriteArgs{Slug: old.Slug, Scope: scope, Workdir: workdir, MemoryType: old.MemoryType, Description: old.Description, Body: body, Actor: args.Actor, ConversationID: args.ConversationID, Model: args.Model, Evidence: args.Evidence})
	if err != nil {
		return r, err
	}
	r.Created = false
	r.Updated = true
	return r, nil
}
func mergeBody(a, b string) string {
	if strings.TrimSpace(b) == "" {
		return a
	}
	if strings.TrimSpace(a) == "" {
		return b
	}
	seen := map[string]bool{}
	out := []string{}
	for _, x := range strings.Split(a+"\n\n"+b, "\n\n") {
		x = strings.TrimSpace(x)
		k := strings.ToLower(x)
		if x != "" && !seen[k] {
			seen[k] = true
			out = append(out, x)
		}
	}
	return strings.Join(out, "\n\n")
}
func (s *Store) Update(args UpdateArgs) (MutationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateLocked(args)
}
func (s *Store) deleteLocked(args DeleteArgs) (MutationResponse, error) {
	entries, err := s.load()
	if err != nil {
		return MutationResponse{}, err
	}
	e, err := findEntry(entries, args.ReadArgs)
	if err != nil {
		return MutationResponse{}, err
	}
	if err := s.safe(e.path); err != nil {
		return MutationResponse{}, err
	}
	if err := os.Remove(e.path); err != nil {
		return MutationResponse{}, err
	}
	if args.Reason != "" {
		state, _ := s.state()
		state.Rejections = append(state.Rejections, Rejection{Slug: e.Slug, Scope: e.Scope, WorkdirHash: e.WorkdirHash, RejectedAt: time.Now().UnixMilli(), Actor: args.Actor, Reason: args.Reason})
		if err := s.saveState(state); err != nil {
			return MutationResponse{}, err
		}
	}
	return MutationResponse{Slug: e.Slug, Scope: e.Scope, Deleted: true, IndexUpdated: true}, nil
}
func (s *Store) Delete(args DeleteArgs) (MutationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deleteLocked(args)
}
func (s *Store) acceptLocked(args ReadArgs) (MutationResponse, error) {
	entries, err := s.load()
	if err != nil {
		return MutationResponse{}, err
	}
	e, err := findEntry(entries, args)
	if err != nil {
		return MutationResponse{}, err
	}
	reviewed := false
	var evidence *Evidence
	if e.Confidence == "high" || e.Confidence == "medium" || e.Confidence == "low" {
		evidence = &Evidence{Confidence: e.Confidence}
	}
	workdir := e.WorkdirPath
	if args.Workdir != "" {
		workdir = args.Workdir
	}
	r, err := s.writeLocked(WriteArgs{Slug: e.Slug, Scope: e.Scope, Workdir: workdir, MemoryType: e.MemoryType, Description: e.Description, Body: e.body, Actor: "accept", Evidence: evidence, Unreviewed: &reviewed})
	if err != nil {
		return MutationResponse{}, err
	}
	r.Created = false
	r.Updated = true
	return r, nil
}

func (s *Store) Accept(args ReadArgs) (MutationResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acceptLocked(args)
}
func (s *Store) ApplyBatch(args BatchArgs) (BatchResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := BatchResponse{}
	for i, d := range args.Decisions {
		scope := d.Scope
		if scope == "" {
			scope = "project"
			if args.Workdir == "" {
				scope = "global"
			}
		}
		switch d.Op {
		case "upsert":
			if d.Description == nil || d.Body == nil {
				out.WarningDetails = append(out.WarningDetails, BatchWarning{Code: "missing_payload", Message: "upsert requires description and body", Slug: d.Slug, Op: d.Op, DecisionIndex: i})
				continue
			}
			r, e := s.writeLocked(WriteArgs{Slug: d.Slug, Scope: scope, Workdir: args.Workdir, MemoryType: d.MemoryType, Description: *d.Description, Body: *d.Body, Actor: "batch", ConversationID: args.ConversationID, Model: args.Model, Evidence: d.Evidence})
			if e != nil {
				out.Warnings = append(out.Warnings, e.Error())
				out.WarningDetails = append(out.WarningDetails, BatchWarning{Code: "rejected", Message: e.Error(), Slug: d.Slug, Op: d.Op, DecisionIndex: i})
				continue
			}
			if r.Created {
				out.Created = append(out.Created, d.Slug)
			} else {
				out.Updated = append(out.Updated, d.Slug)
			}
		case "update":
			r, e := s.updateLocked(UpdateArgs{ReadArgs: ReadArgs{Slug: d.Slug, Scope: scope, Workdir: args.Workdir, WorkdirHash: d.WorkdirHash}, Description: d.Description, Body: d.Body, Mode: d.Mode, Evidence: d.Evidence})
			if e != nil {
				out.Warnings = append(out.Warnings, e.Error())
				continue
			}
			out.Updated = append(out.Updated, r.Slug)
		case "delete":
			r, e := s.deleteLocked(DeleteArgs{ReadArgs: ReadArgs{Slug: d.Slug, Scope: scope, Workdir: args.Workdir, WorkdirHash: d.WorkdirHash}, Reason: d.Reason, Actor: "batch"})
			if e != nil {
				out.Warnings = append(out.Warnings, e.Error())
				continue
			}
			out.Deleted = append(out.Deleted, r.Slug)
		case "accept":
			r, e := s.acceptLocked(ReadArgs{Slug: d.Slug, Scope: scope, Workdir: args.Workdir, WorkdirHash: d.WorkdirHash})
			if e != nil {
				out.Warnings = append(out.Warnings, e.Error())
				continue
			}
			out.Updated = append(out.Updated, r.Slug)
		default:
			out.WarningDetails = append(out.WarningDetails, BatchWarning{Code: "invalid_op", Message: "unsupported batch op", Slug: d.Slug, Op: d.Op, DecisionIndex: i})
		}
	}
	if args.DailyAppend != nil {
		date := args.LocalDate
		if date == "" {
			date = time.Now().Format("2006-01-02")
		}
		entries, er := s.load()
		body := "- " + strings.TrimSpace(args.DailyAppend.Bullet)
		for _, existing := range entries {
			if existing.MemoryType == "daily" && existing.Slug == "daily-"+date {
				body = strings.TrimSpace(existing.body) + "\n" + body
				break
			}
		}
		e := WriteArgs{Slug: "daily-" + date, Scope: "global", MemoryType: "daily", Description: "Daily journal", Body: body, Actor: "daily"}
		if _, er = s.writeDailyLocked(e); er != nil {
			out.Warnings = append(out.Warnings, er.Error())
		}
	}
	return out, nil
}
