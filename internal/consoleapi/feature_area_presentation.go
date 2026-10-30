package consoleapi

import (
	"context"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"

	presentation "github.com/lazyun/devbridge/internal/workflowpresentation"
)

// GetFeatureAreaPresentation shares the exact projection used by text and the
// optional ChatGPT adapter. Product reads authorize the Project independently
// of any supplied or historical Session identity.
func (r *Reader) GetFeatureAreaPresentation(ctx context.Context, projectID, sessionID, areaID string) (presentation.Projection, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return presentation.Projection{}, err
	}
	return r.control.FeatureAreaPresentationForOperator(ctx, projectID, areaID)
}

// ResolveReviewApproval keeps the original Session command inaccessible until
// its persisted receipt can represent the current operator independently.
func (r *Reader) ResolveReviewApproval(ctx context.Context, projectID string, in wp.ResolveInput) (wp.Resolution, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return wp.Resolution{}, err
	}
	return wp.Resolution{}, operatorActionUnavailable("REVIEW_APPROVAL")
}

// GetDevelopmentWorkspace exposes the same inventory and selection as GPT.
func (r *Reader) GetDevelopmentWorkspace(ctx context.Context, projectID, sessionID, areaID, taskID string) (presentation.WorkspaceProjection, error) {
	if err := r.requireProject(ctx, projectID); err != nil {
		return presentation.WorkspaceProjection{}, err
	}
	return r.control.DevelopmentWorkspaceForOperator(ctx, projectID, areaID, taskID)
}
