package backend

import (
	"net/http"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func (s *Server) imageProjects(w http.ResponseWriter, r *http.Request, scope imaging.Scope, parts []string) {
	store := s.image.Store
	if store == nil {
		imageHTTPError(w, r, imageError(imaging.CodeUnavailable))
		return
	}
	if len(parts) == 1 {
		if !imageMethod(w, r, http.MethodGet, http.MethodPost) {
			return
		}
		if r.Method == http.MethodGet {
			page, err := imagePage(r)
			if err != nil {
				imageHTTPError(w, r, err)
				return
			}
			result, err := store.ListProjects(r.Context(), scope, page)
			if err != nil {
				imageHTTPError(w, r, err)
				return
			}
			imageJSON(w, r, 200, map[string]any{"items": result.Items, "nextCursor": result.NextCursor})
			return
		}
		var input struct {
			Title        string `json:"title"`
			WorkspaceRef string `json:"workspaceRef,omitempty"`
		}
		if !imageDecode(w, r, &input) {
			return
		}
		project, err := store.CreateProject(r.Context(), scope, input.Title, input.WorkspaceRef)
		if err != nil {
			imageHTTPError(w, r, err)
			return
		}
		imageJSON(w, r, 201, map[string]any{"project": project})
		return
	}
	if len(parts) != 2 || parts[1] == "" {
		imageHTTPError(w, r, imageError(imaging.CodeNotFound))
		return
	}
	if !imageMethod(w, r, http.MethodGet, http.MethodPatch, http.MethodDelete) {
		return
	}
	id := parts[1]
	if r.Method == http.MethodGet {
		project, err := store.ReadProject(scope, id)
		if err != nil {
			imageHTTPError(w, r, err)
			return
		}
		imageJSON(w, r, 200, map[string]any{"project": project})
		return
	}
	if r.Method == http.MethodPatch {
		var input struct {
			Title            string  `json:"title"`
			ExpectedRevision *uint64 `json:"expectedRevision"`
		}
		if !imageDecode(w, r, &input) {
			return
		}
		if input.ExpectedRevision == nil || *input.ExpectedRevision > imaging.MaxSafeInteger {
			imageHTTPError(w, r, imageError(imaging.CodeInvalidInput))
			return
		}
		project, err := store.UpdateProject(r.Context(), scope, id, input.Title, *input.ExpectedRevision)
		if err != nil {
			imageHTTPError(w, r, err)
			return
		}
		imageJSON(w, r, 200, map[string]any{"project": project})
		return
	}
	// The client contract permits DELETE without a body. In that case capture the
	// current revision; deletion still checks it atomically against concurrent edits.
	var revision uint64
	if r.Body != nil && r.ContentLength != 0 {
		var input struct {
			ExpectedRevision *uint64 `json:"expectedRevision"`
		}
		if !imageDecode(w, r, &input) {
			return
		}
		if input.ExpectedRevision == nil || *input.ExpectedRevision > imaging.MaxSafeInteger {
			imageHTTPError(w, r, imageError(imaging.CodeInvalidInput))
			return
		}
		revision = *input.ExpectedRevision
	} else {
		project, err := store.ReadProject(scope, id)
		if err != nil {
			imageHTTPError(w, r, err)
			return
		}
		revision = project.Revision
	}
	if err := store.DeleteProject(r.Context(), scope, id, revision); err != nil {
		imageHTTPError(w, r, err)
		return
	}
	imageJSON(w, r, 200, map[string]any{"deleted": true})
}
