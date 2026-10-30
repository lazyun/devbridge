package bridgehttp

import (
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/worktreeinventory"
	"net/http"
)

func (s *Server) getWorktreeInventory(w http.ResponseWriter, r *http.Request) {
	for key := range r.URL.Query() {
		if key != "project_id" && key != "format" {
			writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Worktree inventory accepts only project_id and format"})
			return
		}
	}
	format := r.URL.Query().Get("format")
	if format != "" && format != "json" && format != "text" {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "format must be json or text"})
		return
	}
	out, err := s.console.GetWorktreeInventory(r.Context(), r.URL.Query().Get("project_id"))
	w.Header().Set("Cache-Control", "no-store")
	if err == nil && format == "text" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(worktreeinventory.RenderText(out)))
		return
	}
	respond(w, http.StatusOK, out, err)
}
