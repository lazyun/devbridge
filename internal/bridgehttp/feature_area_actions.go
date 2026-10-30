package bridgehttp

import (
	"net/http"

	"github.com/lazyun/devbridge/internal/consoleapi"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/featurearea"
)

// FeatureArea actions delegate to their canonical command owners. Task creation
// replays its exact durable request before checking fresh creation capability.
func (s *Server) featureAreaAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ReleasePlan            bool                  `json:"releasePlan"`
		Action                 string                `json:"action"`
		RequestID              string                `json:"requestId"`
		MergeRequestID         string                `json:"mergeRequestId"`
		RepositoryID           string                `json:"repositoryId"`
		PredecessorOperationID string                `json:"predecessorOperationId"`
		Task                   consoleapi.TaskIntent `json:"task"`
	}
	if !decode(w, r, &in) {
		return
	}
	projectID := r.URL.Query().Get("project_id")
	areaID := r.PathValue("id")
	areas, err := s.plane.FeatureAreas.ListHistoryByProject(r.Context(), projectID, 100)
	var area featurearea.FeatureArea
	for _, candidate := range areas {
		if candidate.ID == areaID {
			area = candidate
			break
		}
	}
	if err != nil || area.ID == "" || area.ProjectID != projectID {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeNotFound, Message: "FeatureArea not found"})
		return
	}
	p, err := s.api.FeatureAreaPresentationForOperator(r.Context(), projectID, areaID)
	if err != nil {
		respond(w, http.StatusOK, nil, err)
		return
	}
	allowed := false
	for _, c := range p.Capabilities {
		if c.Action == in.Action && c.Available {
			allowed = true
		}
	}
	if !allowed && in.Action != "COMMIT_FEATURE_AREA" && in.Action != "CREATE_TASK" && in.Action != "RECOVER_FEATURE_AREA_MERGE" && in.Action != "REPREPARE_FEATURE_AREA_MERGE" {
		writeAPIError(w, &controlapi.Error{Code: controlapi.CodeInvalidState, Message: "Action no longer available"})
		return
	}
	var out any
	switch in.Action {
	case "RECOVER_FEATURE_AREA_MERGE":
		out, err = s.api.RecoverFeatureAreaMergeForOperator(r.Context(), projectID, areaID, in.MergeRequestID)
	case "REPREPARE_FEATURE_AREA_MERGE":
		out, err = s.api.ReprepareFeatureAreaMergeForOperator(r.Context(), projectID, delivery.MergeRetryInput{ReleasePlan: in.ReleasePlan, FeatureAreaID: areaID, MergeRequestID: in.MergeRequestID, RepositoryID: in.RepositoryID, PredecessorOperationID: in.PredecessorOperationID, RequestID: in.RequestID})
	case "COMMIT_FEATURE_AREA":
		out, err = s.api.CommitFeatureAreaForOperator(r.Context(), projectID, areaID, in.RequestID)
	case "RETRY_TASK_COMMIT":
		if p.Commit == nil || p.Commit.RetryTaskID == "" {
			err = featurearea.ErrConflict
		} else if p.Commit.RetryOperation != nil {
			out, err = s.api.ReprepareAcceptedTaskCommitForOperator(r.Context(), projectID, delivery.RetryInput{TaskID: p.Commit.RetryTaskID, RepositoryID: p.Commit.RetryOperation.RepositoryID, PredecessorOperationID: p.Commit.RetryOperation.OperationID, RequestID: in.RequestID})
		} else {
			out, err = s.api.RetryAcceptedTaskCommitForOperator(r.Context(), projectID, p.Commit.RetryTaskID)
		}
	case "RESET_AGENT_CONTEXT":
		out, err = s.api.ResetFeatureAreaAgentContextForOperator(r.Context(), projectID, areaID, in.RequestID)
	case "CREATE_TASK":
		out, err = s.console.CreateTaskIntent(r.Context(), projectID, "", areaID, in.RequestID, in.Task)
	default:
		err = &controlapi.Error{Code: controlapi.CodeInvalidArgument, Message: "Unsupported FeatureArea action"}
	}
	respond(w, http.StatusOK, out, err)
}
