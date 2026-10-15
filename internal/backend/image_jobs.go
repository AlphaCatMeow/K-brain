package backend

import (
	"net/http"
	"strings"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

func (s *Server) imageJobs(w http.ResponseWriter, r *http.Request, scope imaging.Scope, parts []string) {
	jobs := s.image.Jobs
	if jobs == nil || s.image.Store == nil {
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
			result, err := jobs.ListJobs(r.Context(), scope, r.URL.Query().Get("projectId"), page)
			if err != nil {
				imageHTTPError(w, r, err)
				return
			}
			imageJSON(w, r, 200, map[string]any{"items": result.Items, "nextCursor": result.NextCursor})
			return
		}
		keys := r.Header.Values("Idempotency-Key")
		if len(keys) != 1 || len(keys[0]) == 0 || len(keys[0]) > 256 || strings.TrimSpace(keys[0]) != keys[0] || strings.IndexFunc(keys[0], func(c rune) bool { return c < 33 || c > 126 }) >= 0 {
			imageHTTPError(w, r, imageError(imaging.CodeInvalidInput))
			return
		}
		var input imaging.CreateJobRequest
		if !imageDecode(w, r, &input) {
			return
		}
		job, replay, err := jobs.CreateJob(r.Context(), scope, keys[0], input)
		if err != nil {
			imageHTTPError(w, r, err)
			return
		}
		status := http.StatusAccepted
		if replay {
			status = http.StatusOK
		}
		imageJSON(w, r, status, map[string]any{"job": imaging.PublicJob(job), "replayed": replay})
		return
	}
	if len(parts) == 2 && parts[1] != "" {
		if !imageMethod(w, r, http.MethodGet) {
			return
		}
		job, pos, err := s.image.Store.ReadJobSnapshot(scope, parts[1])
		if err != nil {
			imageHTTPError(w, r, err)
			return
		}
		imageJSON(w, r, 200, map[string]any{"job": imaging.PublicJob(job), "firstAvailableSeq": pos.FirstAvailableSeq, "lastEventSeq": pos.LastEventSeq})
		return
	}
	if len(parts) == 3 && parts[1] != "" {
		switch parts[2] {
		case "cancel":
			if !imageMethod(w, r, http.MethodPost) {
				return
			}
			if r.Body != nil && r.ContentLength != 0 {
				var input struct{}
				if !imageDecode(w, r, &input) {
					return
				}
			}
			job, err := jobs.CancelJob(r.Context(), scope, parts[1])
			if err != nil {
				imageHTTPError(w, r, err)
				return
			}
			imageJSON(w, r, 200, map[string]any{"job": imaging.PublicJob(job)})
			return
		case "events":
			if !imageMethod(w, r, http.MethodGet) {
				return
			}
			s.imageEvents(w, r, scope, parts[1])
			return
		}
	}
	imageHTTPError(w, r, imageError(imaging.CodeNotFound))
}
