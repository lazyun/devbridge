package bridgehttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/lazyun/devbridge/internal/approval"
	"github.com/lazyun/devbridge/internal/controlapi"
)

func sessionQuery(w http.ResponseWriter, r *http.Request, extras ...string) bool {
	allowed := map[string]bool{"project_id": true, "task_id": true}
	for _, key := range extras {
		allowed[key] = true
	}
	for key := range r.URL.Query() {
		if !allowed[key] {
			writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Unsupported runtime session parameter"})
			return false
		}
	}
	return true
}
func sessionCursor(w http.ResponseWriter, r *http.Request, key string) (uint64, bool) {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return 0, true
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Invalid session cursor"})
		return 0, false
	}
	return v, true
}
func (s *Server) getRuntimeSession(w http.ResponseWriter, r *http.Request) {
	if !sessionQuery(w, r) {
		return
	}
	q := r.URL.Query()
	v, err := s.console.GetRuntimeSession(r.Context(), q.Get("project_id"), q.Get("task_id"), r.PathValue("id"))
	w.Header().Set("Cache-Control", "no-store")
	respond(w, http.StatusOK, v, err)
}
func (s *Server) getRuntimeSessionItems(w http.ResponseWriter, r *http.Request) {
	if !sessionQuery(w, r, "before", "limit") {
		return
	}
	before, ok := sessionCursor(w, r, "before")
	if !ok {
		return
	}
	limit, ok := intQuery(w, r, "limit")
	if !ok {
		return
	}
	if limit < 0 || limit > 50 {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Session page limit must be between 1 and 50"})
		return
	}
	q := r.URL.Query()
	v, err := s.console.RuntimeSessionHistory(r.Context(), q.Get("project_id"), q.Get("task_id"), r.PathValue("id"), before, limit)
	w.Header().Set("Cache-Control", "no-store")
	respond(w, http.StatusOK, v, err)
}
func (s *Server) getRuntimeSessionInstructions(w http.ResponseWriter, r *http.Request) {
	if !sessionQuery(w, r) {
		return
	}
	q := r.URL.Query()
	v, err := s.console.RuntimeSessionInstructions(r.Context(), q.Get("project_id"), q.Get("task_id"), r.PathValue("id"))
	w.Header().Set("Cache-Control", "no-store")
	respond(w, http.StatusOK, map[string]any{"items": v}, err)
}

// Each client independently reads the same bounded materialization cursor. No
// UI consumes transport events. A blocked writer owns only its own request.
func (s *Server) streamRuntimeSession(w http.ResponseWriter, r *http.Request) {
	if !sessionQuery(w, r, "generation", "cursor") {
		return
	}
	cursor, ok := sessionCursor(w, r, "cursor")
	if !ok {
		return
	}
	q := r.URL.Query()
	generation := q.Get("generation")
	if generation == "" || len(generation) > 256 {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Snapshot generation is required"})
		return
	}
	read := func() (any, bool, error) {
		v, err := s.console.RuntimeSessionUpdates(r.Context(), q.Get("project_id"), q.Get("task_id"), r.PathValue("id"), generation, cursor)
		if err != nil {
			return nil, false, err
		}
		if v.ResetRequired {
			return v, true, nil
		}
		if v.Snapshot == nil {
			return nil, false, nil
		}
		cursor = v.Cursor
		return v, false, nil
	}
	initial, reset, err := read()
	if err != nil {
		writeError(w, err)
		return
	}
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, value any) bool {
		data, e := json.Marshal(value)
		if e != nil {
			return false
		}
		if e = controller.SetWriteDeadline(time.Now().Add(5 * time.Second)); e != nil {
			return false
		}
		if _, e = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); e != nil {
			return false
		}
		return controller.Flush() == nil
	}
	if reset {
		send("reset", initial)
		return
	}
	if initial != nil {
		if !send("update", initial) {
			return
		}
	} else if !send("heartbeat", map[string]string{"state": "CONNECTED"}) {
		return
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	// The transport's local operator realm has no persistent per-browser grant.
	// Re-admit the full request periodically and renew the stream after 2 minutes.
	lifetime := time.NewTimer(2 * time.Minute)
	defer lifetime.Stop()
	count := 0
	for {
		select {
		case <-r.Context().Done():
			return
		case <-lifetime.C:
			send("reconnect", map[string]string{"reason": "REAUTHORIZE_STREAM"})
			return
		case <-tick.C:
			count++
			if _, err := s.operatorIngress.AdmitHTTP(r); err != nil {
				send("unavailable", map[string]string{"reason": "ACCESS_REVOKED"})
				return
			}
			v, reset, err := read()
			if err != nil {
				send("unavailable", map[string]string{"reason": "SESSION_READ_UNAVAILABLE"})
				return
			}
			if reset {
				send("reset", v)
				return
			}
			if v != nil {
				if !send("update", v) {
					return
				}
			}
			// Core lifecycle/approval facts may change without a Codex item.
			if count%5 == 0 {
				if !send("facts", map[string]string{"runId": r.PathValue("id")}) {
					return
				}
			}
			if count%15 == 0 {
				if !send("heartbeat", map[string]string{"state": "CONNECTED"}) {
					return
				}
			}
		}
	}
}

func (s *Server) decideRuntimeApproval(w http.ResponseWriter, r *http.Request) {
	if !sessionQuery(w, r) {
		return
	}
	var input struct {
		Decision approval.Decision `json:"decision"`
	}
	if !decode(w, r, &input) {
		return
	}
	v, err := s.console.DecideRuntimeApproval(r.Context(), r.URL.Query().Get("project_id"), r.PathValue("id"), r.PathValue("approval"), input.Decision)
	respond(w, http.StatusOK, v, err)
}

func (s *Server) getCodexHistory(w http.ResponseWriter, r *http.Request) {
	if !sessionQuery(w, r, "cursor", "limit") {
		return
	}
	limit, ok := intQuery(w, r, "limit")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	q := r.URL.Query()
	value, err := s.console.CodexHistory(ctx, q.Get("project_id"), q.Get("task_id"), r.PathValue("id"), q.Get("cursor"), limit)
	w.Header().Set("Cache-Control", "no-store")
	respond(w, http.StatusOK, value, err)
}
func (s *Server) getTaskEventStatus(w http.ResponseWriter, r *http.Request) {
	if !sessionQuery(w, r) {
		return
	}
	v, e := s.console.TaskEventStatus(r.Context(), r.URL.Query().Get("project_id"), r.PathValue("id"))
	w.Header().Set("Cache-Control", "no-store")
	respond(w, http.StatusOK, v, e)
}
