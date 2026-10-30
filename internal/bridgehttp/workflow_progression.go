package bridgehttp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func (s *Server) registerWorkflowProgression(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/review-approvals/resolve", func(w http.ResponseWriter, r *http.Request) {
		var in wp.ResolveInput
		if !decode(w, r, &in) {
			return
		}
		out, err := s.api.ResolveReviewApproval(r.Context(), in)
		respond(w, http.StatusOK, out, err)
	})
	mux.HandleFunc("POST /ui/api/featureAreas/{id}/review-approval", func(w http.ResponseWriter, r *http.Request) {
		var in wp.ResolveInput
		if !decode(w, r, &in) {
			return
		}
		in.FeatureAreaID = r.PathValue("id")
		out, err := s.console.ResolveReviewApproval(r.Context(), r.URL.Query().Get("project_id"), in)
		respond(w, http.StatusOK, out, err)
	})
	mux.HandleFunc("POST /v1/executions/{id}/start-approved", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			AISessionID string `json:"aiSessionId"`
		}
		if !decode(w, r, &in) {
			return
		}
		out, err := s.api.StartApprovedExecution(r.Context(), in.AISessionID, r.PathValue("id"))
		respond(w, http.StatusOK, out, err)
	})
	mux.HandleFunc("GET /v1/workflow-guidance", func(w http.ResponseWriter, r *http.Request) {
		out, err := s.api.WorkflowGuidance(r.Context(), r.URL.Query().Get("ai_session_id"), r.URL.Query().Get("feature_area_id"))
		respond(w, http.StatusOK, out, err)
	})
}

type mutationResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *mutationResponse) Header() http.Header { return w.header }
func (w *mutationResponse) WriteHeader(n int) {
	if w.status == 0 {
		w.status = n
	}
}
func (w *mutationResponse) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.body.Write(b)
}
func identity(m map[string]any, names ...string) string {
	for _, name := range names {
		if s, ok := m[name].(string); ok && s != "" {
			return s
		}
	}
	return ""
}
func (s *Server) workflowMutationGuidance(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var in map[string]any
		_ = json.Unmarshal(raw, &in)
		response := &mutationResponse{header: make(http.Header)}
		next.ServeHTTP(response, r)
		var out map[string]any
		if json.Unmarshal(response.body.Bytes(), &out) == nil && out != nil {
			scope := wp.ResourceScope{AISessionID: identity(in, "aiSessionId", "ai_session_id", "AISessionID"), ProjectID: identity(in, "projectId", "project_id", "ProjectID"), FeatureAreaID: identity(in, "featureAreaId", "feature_area_id", "FeatureAreaID"), TaskID: identity(in, "taskId", "task_id", "TaskID"), ReviewID: identity(in, "reviewId", "review_id", "ReviewID"), ExecutionID: identity(in, "executionId", "execution_id"), RunID: identity(in, "runId", "run_id"), OwnerType: identity(in, "ownerType"), OwnerID: identity(in, "ownerId")}
			if strings.HasPrefix(r.URL.Path, "/v1/tasks/") {
				scope.TaskID = r.PathValue("id")
			}
			if strings.HasPrefix(r.URL.Path, "/v1/executions/") || strings.HasPrefix(r.URL.Path, "/v1/execution-proposals/") {
				scope.ExecutionID = r.PathValue("id")
			}
			if strings.HasPrefix(r.URL.Path, "/v1/runs/") {
				scope.RunID = r.PathValue("id")
			}
			if scope.FeatureAreaID == "" {
				scope.FeatureAreaID = identity(out, "featureAreaId", "feature_area_id", "FeatureAreaID")
			}
			if scope.ProjectID == "" {
				scope.ProjectID = identity(out, "projectId", "project_id", "ProjectID")
				if scope.ProjectID == "" {
					scope.ProjectID = r.URL.Query().Get("project_id")
				}
			}
			if scope.FeatureAreaID == "" && strings.Contains(r.URL.Path, "feature-areas") {
				scope.FeatureAreaID = r.PathValue("featureAreaID")
				if scope.FeatureAreaID == "" {
					scope.FeatureAreaID = identity(out, "id", "ID")
				}
			}
			if scope.FeatureAreaID == "" && strings.Contains(r.URL.Path, "featureAreas") {
				scope.FeatureAreaID = r.PathValue("id")
			}
			var g wp.Guidance
			var e error
			if identity(out, "generation") != "" {
				// Keep the complete Core snapshot intact across concurrent phase changes.
				e = json.Unmarshal(response.body.Bytes(), &g)
			} else {
				g, e = s.api.WorkflowGuidanceForResource(r.Context(), scope)
			}
			if e == nil && (scope.FeatureAreaID != "" || scope.TaskID != "" || scope.ReviewID != "" || scope.ExecutionID != "" || scope.RunID != "" || scope.OwnerID != "" || strings.Contains(r.URL.Path, "/tasks")) {
				if response.status >= 400 {
					g.Status = "REJECTED"
				}
				out["workflowGuidance"] = g
				out["currentState"] = g.CurrentState
				out["nextAction"] = g.NextAction
				response.body.Reset()
				_ = json.NewEncoder(&response.body).Encode(out)
			}
		}
		for k, values := range response.header {
			w.Header()[k] = values
		}
		w.Header().Del("Content-Length")
		if response.status == 0 {
			response.status = 200
		}
		w.WriteHeader(response.status)
		_, _ = w.Write(response.body.Bytes())
	})
}
