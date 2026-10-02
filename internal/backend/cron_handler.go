package backend

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (s *Server) handleCron(w http.ResponseWriter, r *http.Request) {
	if s.cron == nil {
		writeJSONError(w, http.StatusNotImplemented, "backend cron is unavailable")
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.EscapedPath(), "/v1/cron"), "/")
	if path == "" {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, s.cron.store.snapshot())
		case http.MethodPut:
			s.cronApply(w, r)
		default:
			w.Header().Set("Allow", "GET, PUT")
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	parts := strings.Split(path, "/")
	if parts[0] == "validate" && len(parts) == 1 && r.Method == http.MethodPost {
		s.cronValidate(w, r)
		return
	}
	if parts[0] == "prompt-runs" {
		s.cronPromptCompat(w, r, parts[1:])
		return
	}
	if len(parts) < 2 {
		writeJSONError(w, http.StatusNotFound, "cron route not found")
		return
	}
	taskID, err := url.PathUnescape(parts[0])
	if err != nil || strings.TrimSpace(taskID) == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid cron task id")
		return
	}
	switch {
	case len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost:
		if err := s.cron.cancel(taskID, ""); err != nil {
			writeJSONError(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	case len(parts) == 4 && parts[1] == "runs" && parts[3] == "cancel" && r.Method == http.MethodPost:
		runID, err := url.PathUnescape(parts[2])
		if err != nil || strings.TrimSpace(runID) == "" {
			writeJSONError(w, http.StatusBadRequest, "invalid cron run id")
			return
		}
		if err := s.cron.cancel(taskID, runID); err != nil {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case len(parts) == 2 && parts[1] == "runs" && r.Method == http.MethodGet:
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		runs, err := s.cron.store.runs(taskID, limit)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
	case len(parts) == 2 && parts[1] == "runs" && r.Method == http.MethodDelete:
		count, err := s.cron.store.clearRuns(taskID)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"clearedCount": count})
	case len(parts) == 2 && parts[1] == "run-now" && r.Method == http.MethodPost:
		response, err := s.cron.runNow(taskID)
		if err != nil {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, response)
	default:
		writeJSONError(w, http.StatusNotFound, "cron route not found")
	}
}

func (s *Server) cronApply(w http.ResponseWriter, r *http.Request) {
	var input CronApplyInput
	if err := decodeCronJSON(w, r, &input); err != nil {
		return
	}
	response, err := s.cron.store.apply(input)
	if err != nil {
		writeJSONError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, response)
}
func (s *Server) cronValidate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Expression string `json:"expression"`
	}
	if err := decodeCronJSON(w, r, &input); err != nil {
		return
	}
	if err := validateCronExpression(input.Expression); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
func (s *Server) cronPromptCompat(w http.ResponseWriter, r *http.Request, parts []string) {
	// Prompt jobs execute in K-brain's scheduler. These endpoints stay available
	// so the desktop runner can reconcile without creating a second executor.
	if len(parts) == 1 && parts[0] == "claim" && r.Method == http.MethodPost {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	if len(parts) == 1 && parts[0] == "release" && r.Method == http.MethodPost {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if len(parts) == 1 && parts[0] == "complete" && r.Method == http.MethodPost {
		writeJSON(w, http.StatusOK, map[string]any{"status": "already_finished"})
		return
	}
	writeJSONError(w, http.StatusNotFound, "cron prompt route not found")
}
func decodeCronJSON(w http.ResponseWriter, r *http.Request, value any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid cron request")
		return err
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		writeJSONError(w, http.StatusBadRequest, "expected one cron object")
		return errors.New("expected one cron object")
	}
	return nil
}
