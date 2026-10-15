package imaging

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

type pageCursor struct {
	Version   int       `json:"v"`
	ScopeTag  string    `json:"scope"`
	Kind      string    `json:"kind"`
	ProjectID string    `json:"project"`
	CreatedAt time.Time `json:"createdAt"`
	ID        string    `json:"id"`
}

// Cursor payloads are visible to clients. Bind identity using a keyed tag so
// internal owner/backend IDs do not become public via base64 decoding.
func (s *Store) cursorScope(scope Scope) string {
	b, _ := json.Marshal([]string{scope.OwnerID, scope.BackendID})
	h := hmac.New(sha256.New, s.cursorKey)
	h.Write([]byte("image-cursor-scope\x00"))
	h.Write(b)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

func (s *Store) encodeCursor(c pageCursor) string {
	b, _ := json.Marshal(c)
	h := hmac.New(sha256.New, s.cursorKey)
	h.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *Store) decodeCursor(scope Scope, kind, project string, p PageRequest) (pageCursor, error) {
	var c pageCursor
	if scope.OwnerID == "" || scope.BackendID == "" {
		return c, notFound()
	}
	if p.Limit < 1 || p.Limit > 100 {
		return c, invalid("limit must be 1..100")
	}
	if p.Cursor == "" {
		return c, nil
	}
	if len(p.Cursor) > 8192 {
		return c, invalid("invalid cursor")
	}
	parts := strings.Split(p.Cursor, ".")
	if len(parts) != 2 {
		return c, invalid("invalid cursor")
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return c, invalid("invalid cursor")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return c, invalid("invalid cursor")
	}
	h := hmac.New(sha256.New, s.cursorKey)
	h.Write(b)
	if !hmac.Equal(sig, h.Sum(nil)) {
		return c, invalid("invalid cursor checksum")
	}
	if err = decodeStrict(b, &c); err != nil {
		return c, invalid("invalid cursor")
	}
	if c.Version != 1 || c.ScopeTag != s.cursorScope(scope) || c.Kind != kind || c.ProjectID != project || c.ID == "" {
		return c, invalid("cursor scope/filter mismatch")
	}
	return c, nil
}
func cursorAfter(created time.Time, id string, c pageCursor) bool {
	return c.ID == "" || created.After(c.CreatedAt) || (created.Equal(c.CreatedAt) && id > c.ID)
}
func lessRecord(a time.Time, aid string, b time.Time, bid string) bool {
	return a.Before(b) || (a.Equal(b) && aid < bid)
}
func (s *Store) ListProjects(ctx context.Context, scope Scope, p PageRequest) (Page[ImageProject], error) {
	out := Page[ImageProject]{Items: []ImageProject{}}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return out, errStoreClosed
	}
	c, err := s.decodeCursor(scope, "projects", "", p)
	if err != nil {
		return out, err
	}
	for _, r := range s.data.Projects {
		if r.Scope.matches(scope) && r.DeletedAt == nil && cursorAfter(r.Value.CreatedAt, r.Value.ID, c) {
			out.Items = append(out.Items, cloneStoreValue(r.Value))
		}
	}
	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		return lessRecord(a.CreatedAt, a.ID, b.CreatedAt, b.ID)
	})
	if len(out.Items) > p.Limit {
		out.Items = out.Items[:p.Limit]
		last := out.Items[len(out.Items)-1]
		out.NextCursor = s.encodeCursor(pageCursor{1, s.cursorScope(scope), "projects", "", last.CreatedAt, last.ID})
	}
	return out, nil
}
func (s *Store) ListJobs(ctx context.Context, scope Scope, projectID string, p PageRequest) (Page[ImageJob], error) {
	out := Page[ImageJob]{Items: []ImageJob{}}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return out, errStoreClosed
	}
	return s.listJobs(scope, projectID, p, "jobs")
}

// listJobs requires the store read lock; private recovery listings share the same
// ordering and scope checks while using a separate cursor kind.
func (s *Store) listJobs(scope Scope, projectID string, p PageRequest, kind string) (Page[ImageJob], error) {
	out := Page[ImageJob]{Items: []ImageJob{}}
	c, err := s.decodeCursor(scope, kind, projectID, p)
	if err != nil {
		return out, err
	}
	if projectID != "" {
		if _, err = readProject(s.data, scope, projectID); err != nil {
			return out, err
		}
	}
	for _, r := range s.data.Jobs {
		if r.Scope.matches(scope) && r.DeletedAt == nil && (projectID == "" || r.Value.ProjectID == projectID) && cursorAfter(r.Value.CreatedAt, r.Value.ID, c) {
			out.Items = append(out.Items, cloneStoreValue(PublicJob(r.Value)))
		}
	}
	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		return lessRecord(a.CreatedAt, a.ID, b.CreatedAt, b.ID)
	})
	if len(out.Items) > p.Limit {
		out.Items = out.Items[:p.Limit]
		last := out.Items[len(out.Items)-1]
		out.NextCursor = s.encodeCursor(pageCursor{1, s.cursorScope(scope), kind, projectID, last.CreatedAt, last.ID})
	}
	return out, nil
}

// ListAssets supports asset maintenance using exactly the same signed scope-bound
// pagination contract as projects/jobs. It never exposes private execution state.
func (s *Store) ListAssets(ctx context.Context, scope Scope, p PageRequest) (Page[ImageAsset], error) {
	out := Page[ImageAsset]{Items: []ImageAsset{}}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return out, errStoreClosed
	}
	c, err := s.decodeCursor(scope, "assets", "", p)
	if err != nil {
		return out, err
	}
	for _, r := range s.data.Assets {
		if r.Scope.matches(scope) && cursorAfter(r.Value.CreatedAt, r.Value.ID, c) {
			out.Items = append(out.Items, cloneStoreValue(PublicAsset(r.Value)))
		}
	}
	sort.Slice(out.Items, func(i, j int) bool {
		a, b := out.Items[i], out.Items[j]
		return lessRecord(a.CreatedAt, a.ID, b.CreatedAt, b.ID)
	})
	if len(out.Items) > p.Limit {
		out.Items = out.Items[:p.Limit]
		last := out.Items[len(out.Items)-1]
		out.NextCursor = s.encodeCursor(pageCursor{1, s.cursorScope(scope), "assets", "", last.CreatedAt, last.ID})
	}
	return out, nil
}
