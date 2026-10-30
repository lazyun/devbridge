package mcpserver

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	p "github.com/lazyun/devbridge/internal/workflowpresentation"
)

// Source fields describe durable history; they confer no execution permission.
type SurfaceFeatureAreaSummary struct {
	Product          p.FeatureAreaProductProjection `json:"product"`
	ID               string                         `json:"id"`
	ProjectID        string                         `json:"projectId"`
	Title            string                         `json:"title"`
	ProblemStatement string                         `json:"problemStatement"`
	Status           string                         `json:"status"`
	UpdatedAt        time.Time                      `json:"updatedAt"`
}
type SurfaceDecisionReceipt struct {
	DecisionID string    `json:"decisionId"`
	Option     string    `json:"option"`
	RequestID  string    `json:"requestId"`
	SubjectID  string    `json:"subjectId"`
	ResultKind string    `json:"resultKind"`
	ResultID   string    `json:"resultId"`
	ResolvedAt time.Time `json:"resolvedAt"`
}
type SurfaceDefinitionSource struct {
	ID                string                  `json:"id"`
	TaskID            string                  `json:"taskId"`
	Ordinal           int                     `json:"ordinal"`
	PayloadSHA256     string                  `json:"payloadSha256"`
	CreatedAt         time.Time               `json:"createdAt"`
	Definition        task.Definition         `json:"definition"`
	InstructionSource *task.InstructionSource `json:"instructionSource,omitempty"`
	SourceReviewID    string                  `json:"sourceReviewId,omitempty"`
	ApprovalReceipt   *SurfaceDecisionReceipt `json:"approvalReceipt,omitempty"`
}
type SurfaceTaskSources struct {
	TaskID                      string                   `json:"taskId"`
	CurrentReviewID             string                   `json:"currentReviewId,omitempty"`
	CurrentExecutionID          string                   `json:"currentExecutionId,omitempty"`
	CurrentDefinitionRevisionID string                   `json:"currentDefinitionRevisionId,omitempty"`
	CurrentReviewApprovalID     string                   `json:"currentReviewApprovalId,omitempty"`
	DefinitionStatus            string                   `json:"definitionStatus"`
	SelectedDefinition          *SurfaceDefinitionSource `json:"selectedDefinition,omitempty"`
}
type SurfaceReviewSource struct {
	Summary              string                       `json:"summary,omitempty"`
	SummaryStatus        string                       `json:"summaryStatus"`
	SummaryTruncated     bool                         `json:"summaryTruncated"`
	ID                   string                       `json:"id"`
	TaskID               string                       `json:"taskId"`
	FeatureAreaID        string                       `json:"featureAreaId"`
	RepositoryID         string                       `json:"repositoryId"`
	ExecutorKind         review.ExecutorKind          `json:"executorKind"`
	Purpose              string                       `json:"purpose"`
	FocusKind            review.FocusKind             `json:"focusKind"`
	FocusSummary         string                       `json:"focusSummary"`
	FocusPaths           []string                     `json:"focusPaths"`
	Baseline             string                       `json:"baseline"`
	RepositoryProvenance *review.RepositoryProvenance `json:"repositoryProvenance,omitempty"`
	Status               string                       `json:"status"`
	RunStatus            string                       `json:"runStatus,omitempty"`
	Disposition          review.Disposition           `json:"disposition,omitempty"`
	DecisionID           string                       `json:"decisionId"`
	DecisionStatus       string                       `json:"decisionStatus"`
	DecisionReceipt      *SurfaceDecisionReceipt      `json:"decisionReceipt,omitempty"`
}

func surfaceReviewSource(v review.Review) SurfaceReviewSource {
	return SurfaceReviewSource{SummaryStatus: "NOT_AVAILABLE", ID: v.ID, TaskID: v.TaskID, FeatureAreaID: v.FeatureAreaID, RepositoryID: v.RepositoryID, ExecutorKind: v.ExecutorKind, Purpose: boundedUTF8(v.EngineeringGoal, 2048), FocusKind: v.Focus.Kind, FocusSummary: boundedUTF8(v.Focus.Summary, 2048), FocusPaths: append([]string{}, v.Focus.Paths...), Baseline: v.BaseCommit, RepositoryProvenance: v.RepositoryProvenance, Status: v.TerminalStatus, Disposition: v.ReviewDisposition, DecisionID: "REVIEW_APPROVAL:" + v.ID, DecisionStatus: "NOT_READ"}
}
func (s *StaticService) ListSurfaceFeatureAreasForSession(ctx context.Context, sessionID, areaID string, options controlapi.ListOptions) ([]featurearea.FeatureArea, error) {
	if s.control == nil {
		return nil, errors.New("FeatureArea inventory unavailable")
	}
	return s.control.ListSurfaceFeatureAreasForSession(ctx, sessionID, areaID, options)
}
func surfaceDecisionReceipt(v decisionresolution.Receipt) *SurfaceDecisionReceipt {
	return &SurfaceDecisionReceipt{DecisionID: v.DecisionID, Option: v.Option, RequestID: v.RequestID, SubjectID: v.SubjectID, ResultKind: v.ResultKind, ResultID: v.ResultID, ResolvedAt: v.ResolvedAt}
}
func readSurfaceReviewReceipt(ctx context.Context, s BridgeService, session string, owner task.Task, reviewID string, required bool) (*SurfaceDecisionReceipt, error) {
	v, err := s.GetDecisionResolution(ctx, session, "REVIEW_APPROVAL:"+reviewID)
	if err != nil {
		if !required && errors.Is(err, decisionresolution.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if v.ProjectID != owner.ProjectID || v.SubjectKind != "TASK_REVIEW_GATE" || v.SubjectID != owner.ID || v.DecisionID != "REVIEW_APPROVAL:"+reviewID || v.Kind != "REVIEW_APPROVAL" {
		return nil, errors.New("Review approval source identity mismatch")
	}
	if required && v.Option != "APPROVE" {
		return nil, errors.New("Definition approval source is not APPROVE")
	}
	return surfaceDecisionReceipt(v), nil
}
func readSurfaceDefinition(ctx context.Context, s BridgeService, session string, owner task.Task, id string) (SurfaceDefinitionSource, error) {
	out := SurfaceDefinitionSource{}
	reader, ok := s.(interface {
		GetTaskDefinitionRevision(context.Context, string, string) (task.DefinitionRevision, error)
	})
	if !ok {
		return out, errors.New("Task definition source read unavailable")
	}
	value, err := reader.GetTaskDefinitionRevision(ctx, session, id)
	if err != nil {
		return out, err
	}
	d := value.Definition
	if value.ID != id || value.TaskID != owner.ID || d.ProjectID != owner.ProjectID || d.FeatureAreaID != owner.FeatureAreaID || d.RepositoryID != owner.RepositoryID {
		return out, errors.New("Definition source identity mismatch")
	}
	out = SurfaceDefinitionSource{ID: value.ID, TaskID: value.TaskID, Ordinal: value.Ordinal, PayloadSHA256: value.PayloadSHA256, CreatedAt: value.CreatedAt, Definition: d, InstructionSource: value.InstructionSource}
	if d.ReviewApprovalID != "" {
		source, ok := strings.CutPrefix(d.ReviewApprovalID, "REVIEW_APPROVAL:")
		if !ok || source == "" {
			return out, errors.New("Definition approval source unavailable")
		}
		r, err := s.GetReview(ctx, session, source)
		if err != nil {
			return out, err
		}
		if r.ID != source || r.TaskID != owner.ID || r.ProjectID != owner.ProjectID || r.FeatureAreaID != owner.FeatureAreaID || r.RepositoryID != owner.RepositoryID {
			return out, errors.New("Definition Review source mismatch")
		}
		receipt, err := readSurfaceReviewReceipt(ctx, s, session, owner, source, true)
		if err != nil {
			return out, err
		}
		out.SourceReviewID, out.ApprovalReceipt = source, receipt
	}
	return out, nil
}
func readSurfaceTaskSources(ctx context.Context, s BridgeService, session string, owner task.Task, revisionID string) (SurfaceTaskSources, error) {
	out := SurfaceTaskSources{TaskID: owner.ID, CurrentReviewID: owner.CurrentReviewID, CurrentExecutionID: owner.CurrentExecutionID, CurrentDefinitionRevisionID: owner.CurrentDefinitionRevisionID, CurrentReviewApprovalID: owner.ReviewApprovalID, DefinitionStatus: "NOT_RECORDED"}
	if revisionID == "" {
		revisionID = owner.CurrentDefinitionRevisionID
	}
	if revisionID == "" {
		return out, nil
	}
	value, err := readSurfaceDefinition(ctx, s, session, owner, revisionID)
	if err != nil {
		return out, err
	}
	out.DefinitionStatus = "AVAILABLE"
	out.SelectedDefinition = &value
	return out, nil
}
