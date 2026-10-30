package consoleapi

import (
	"context"
)

type TaskIntent struct {
	Title            string   `json:"title"`
	ProblemStatement string   `json:"problemStatement"`
	RepositoryID     string   `json:"repositoryId"`
	Constraints      []string `json:"constraints,omitempty"`
}

// CreateTaskIntent creates the semantic Task. Review is a separate explicit action.
func (r *Reader) CreateTaskIntent(ctx context.Context, projectID, sessionID, areaID, requestID string, in TaskIntent) (ProductTaskSummary, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return ProductTaskSummary{}, err
	}
	return ProductTaskSummary{}, operatorActionUnavailable("CREATE_TASK")
}
