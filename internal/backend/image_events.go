package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Stack-Cairn/K-brain/internal/imaging"
)

const imageHeartbeatInterval = 15 * time.Second
const imageEventPollInterval = 250 * time.Millisecond
const imageStreamWriteTimeout = 10 * time.Second

func imageTerminal(status imaging.JobStatus) bool {
	switch status {
	case imaging.JobSucceeded, imaging.JobPartialSucceeded, imaging.JobFailed, imaging.JobCancelled, imaging.JobResultUnknown:
		return true
	}
	return false
}
func (s *Server) imageEvents(w http.ResponseWriter, r *http.Request, scope imaging.Scope, id string) {
	var after uint64
	values := r.Header.Values("Last-Event-ID")
	if len(values) > 1 {
		imageHTTPError(w, r, imageError(imaging.CodeInvalidInput))
		return
	}
	if len(values) == 1 {
		text := values[0]
		n, err := strconv.ParseUint(text, 10, 64)
		if text == "" || strings.IndexFunc(text, func(c rune) bool { return c < '0' || c > '9' }) >= 0 || err != nil || n > imaging.MaxSafeInteger {
			imageHTTPError(w, r, imageError(imaging.CodeInvalidInput))
			return
		}
		after = n
	}
	// Snapshot before reading events: a terminal snapshot's position is a lower
	// bound that must have been replayed before closing this subscription.
	job, pos, err := s.image.Store.ReadJobSnapshot(scope, id)
	if err != nil {
		imageHTTPError(w, r, err)
		return
	}
	events, err := s.image.Jobs.ReadEvents(r.Context(), scope, id, after, 100)
	if err != nil {
		imageHTTPError(w, r, err)
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		imageHTTPError(w, r, imageError(imaging.CodeUnavailable))
		return
	}
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flush := func() bool {
		_ = controller.SetWriteDeadline(time.Now().Add(imageStreamWriteTimeout))
		err := controller.Flush()
		_ = controller.SetWriteDeadline(time.Time{})
		return err == nil
	}
	if !flush() {
		return
	}
	heartbeat := time.NewTicker(imageHeartbeatInterval)
	defer heartbeat.Stop()
	poll := time.NewTicker(imageEventPollInterval)
	defer poll.Stop()
	for {
		for _, event := range events {
			if r.Context().Err() != nil || event.JobID != id || event.Seq != after+1 {
				return
			}
			// A new defined type preserves the public fields without promoting
			// ImageEvent.MarshalJSON and losing the envelope's version field.
			type eventWire imaging.ImageEvent
			raw, marshalErr := json.Marshal(struct {
				Version string `json:"version"`
				eventWire
			}{imaging.WireVersion, eventWire(imaging.PublicEvent(event))})
			if marshalErr != nil {
				return
			}
			_ = controller.SetWriteDeadline(time.Now().Add(imageStreamWriteTimeout))
			if _, err = fmt.Fprintf(w, "id: %d\nevent: image.event\ndata: %s\n\n", event.Seq, raw); err != nil || !flush() {
				return
			}
			after = event.Seq
		}
		if imageTerminal(job.Status) && after >= pos.LastEventSeq {
			return
		}
		if len(events) == 100 {
			// Drain retained history without adding a polling delay per page.
		} else {
			select {
			case <-r.Context().Done():
				return
			case <-heartbeat.C:
				_ = controller.SetWriteDeadline(time.Now().Add(imageStreamWriteTimeout))
				if _, err = fmt.Fprint(w, ": heartbeat\n\n"); err != nil || !flush() {
					return
				}
			case <-poll.C:
			}
		}
		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return
		}
		job, pos, err = s.image.Store.ReadJobSnapshot(scope, id)
		if err != nil {
			return
		}
		events, err = s.image.Jobs.ReadEvents(r.Context(), scope, id, after, 100)
		if err != nil {
			return
		}
	}
}
