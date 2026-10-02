package backend

import (
	"encoding/json"
	"io"
	"net/http"
)

func (s *Server) handleHooks(w http.ResponseWriter, r *http.Request) {
	if s.hookStore == nil {
		writeJSONError(w, http.StatusNotImplemented, "backend hooks are unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.hookStore.Snapshot())
	case http.MethodPut:
		var input HooksApplyInput
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&input); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid hooks request")
			return
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			writeJSONError(w, http.StatusBadRequest, "expected one hooks object")
			return
		}
		response, err := s.hookStore.Apply(input)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		code := http.StatusOK
		if response.Status == "conflict" {
			code = http.StatusOK
		}
		writeJSON(w, code, response)
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
