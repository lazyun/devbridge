package recoverystatus

import (
	"encoding/json"
	"path"
	"strings"

	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workset"
)

// PurposeProposal is caller-authored, typed authority for a revision-required
// Recovery. It is never reconstructed from a report, summary, or error text.
type PurposeProposal struct {
	RecoveryID               string                             `json:"recoveryId"`
	RecoveryGeneration       int                                `json:"recoveryGeneration"`
	OwnerKind                string                             `json:"ownerKind"`
	OwnerID                  string                             `json:"ownerId"`
	TaskID                   string                             `json:"taskId"`
	FeatureAreaID            string                             `json:"featureAreaId"`
	SourceExecutionID        string                             `json:"sourceExecutionId,omitempty"`
	SourceReviewID           string                             `json:"sourceReviewId,omitempty"`
	TaskDefinitionRevisionID string                             `json:"taskDefinitionRevisionId"`
	SourceOperationID        string                             `json:"sourceOperationId"`
	SourceRunID              string                             `json:"sourceRunId"`
	SourceTurnID             string                             `json:"sourceTurnId"`
	RuntimeEpoch             string                             `json:"runtimeEpoch"`
	RuntimeWorksetHash       string                             `json:"runtimeWorksetHash"`
	AssessmentHash           string                             `json:"assessmentHash"`
	SourceAISessionID        string                             `json:"sourceAiSessionId"`
	Variants                 []PurposeVariant                   `json:"variants"`
	RecommendedRuntimeConfig *codexruntime.NextRunRuntimeConfig `json:"recommendedRuntimeConfig,omitempty"`
	SubmittedHash            string                             `json:"submittedHash"`
}

type PurposeVariant struct {
	ID             string                        `json:"id"`
	Label          string                        `json:"label"`
	Summary        string                        `json:"summary"`
	Implementation *ImplementationPurposeVariant `json:"implementation,omitempty"`
	Verification   *VerificationPurposeVariant   `json:"verification,omitempty"`
	Review         *ReviewPurposeVariant         `json:"review,omitempty"`
}

type ImplementationPurposeVariant struct {
	SourceExecutionID  string                  `json:"sourceExecutionId"`
	SourceRevisionID   string                  `json:"sourceRevisionId"`
	WorkSpec           task.WorkSpec           `json:"workSpec"`
	VerificationSpec   task.VerificationSpec   `json:"verificationSpec"`
	RepositoryTargets  []workset.Target        `json:"repositoryTargets"`
	PrimaryMemberID    string                  `json:"primaryMemberId"`
	ImplementationMode task.ImplementationMode `json:"implementationMode"`
}

type VerificationPurposeVariant struct {
	SourceExecutionID  string                  `json:"sourceExecutionId"`
	SourceRevisionID   string                  `json:"sourceRevisionId"`
	WorkSpec           *task.WorkSpec          `json:"workSpec,omitempty"`
	VerificationSpec   task.VerificationSpec   `json:"verificationSpec"`
	RepositoryTargets  []workset.Target        `json:"repositoryTargets"`
	PrimaryMemberID    string                  `json:"primaryMemberId"`
	ImplementationMode task.ImplementationMode `json:"implementationMode"`
}

type ReviewPurposeVariant struct {
	SourceReviewID      string           `json:"sourceReviewId"`
	ReviewInstruction   string           `json:"reviewInstruction"`
	ReviewPlan          run.ReviewPlan   `json:"reviewPlan"`
	ReviewDepth         task.ReviewDepth `json:"reviewDepth"`
	SupplementalPurpose string           `json:"supplementalPurpose"`
	RepositoryTargets   []workset.Target `json:"repositoryTargets"`
}

func (p PurposeProposal) ComputeSubmittedHash() string {
	p.SubmittedHash = ""
	b, _ := json.Marshal(p)
	return Hash(b)
}

func (p PurposeProposal) Validate(source Source, recoveryID string, generation int, assessmentHash string) error {
	if p.RecoveryID != recoveryID || p.RecoveryGeneration != generation || p.TaskID == "" || p.TaskID != source.TaskID ||
		p.FeatureAreaID != source.FeatureAreaID || p.SourceOperationID != source.OperationID || p.SourceRunID != source.RunID ||
		p.SourceTurnID != source.TurnID || p.RuntimeEpoch != source.RuntimeEpoch || p.RuntimeWorksetHash != source.RuntimeWorksetHash ||
		p.AssessmentHash != assessmentHash || p.SourceAISessionID == "" || p.TaskDefinitionRevisionID != source.TaskDefinitionRevisionID ||
		p.SubmittedHash == "" || p.SubmittedHash != p.ComputeSubmittedHash() || len(p.Variants) == 0 {
		return ErrIneligible
	}
	switch source.OperationKind {
	case "EXECUTION":
		if p.SourceExecutionID != source.OperationID || p.SourceReviewID != "" || (p.OwnerKind != "IMPLEMENTATION" && p.OwnerKind != "VERIFICATION") {
			return ErrIneligible
		}
	case "REVIEW":
		if p.SourceReviewID != source.OperationID || p.SourceExecutionID != "" || p.OwnerKind != "REVIEW" {
			return ErrIneligible
		}
	default:
		return ErrIneligible
	}
	if p.OwnerID != source.OperationID || (source.OwnerKind != "" && p.OwnerKind != source.OwnerKind) || (source.OwnerID != "" && p.OwnerID != source.OwnerID) {
		return ErrIneligible
	}
	seen := map[string]bool{}
	for _, v := range p.Variants {
		if v.ID == "" || seen[v.ID] || strings.TrimSpace(v.Label) == "" || strings.TrimSpace(v.Summary) == "" {
			return ErrIneligible
		}
		seen[v.ID] = true
		switch p.OwnerKind {
		case "IMPLEMENTATION":
			b := v.Implementation
			if b == nil || v.Verification != nil || v.Review != nil || b.SourceExecutionID != p.SourceExecutionID || b.SourceRevisionID != p.TaskDefinitionRevisionID || b.PrimaryMemberID == "" || b.ImplementationMode == "" || b.WorkSpec.ImplementationMode != b.ImplementationMode || purposeWorkSpecValid(b.WorkSpec) != nil || task.ValidateTargetVerification(b.VerificationSpec, b.RepositoryTargets) != nil || workset.ValidateTargets(b.RepositoryTargets) != nil || !purposePrimaryInTargets(b.PrimaryMemberID, b.RepositoryTargets) {
				return ErrIneligible
			}
		case "VERIFICATION":
			b := v.Verification
			if b == nil || v.Implementation != nil || v.Review != nil || b.SourceExecutionID != p.SourceExecutionID || b.SourceRevisionID != p.TaskDefinitionRevisionID || b.PrimaryMemberID == "" || b.ImplementationMode == "" || task.ValidateTargetVerification(b.VerificationSpec, b.RepositoryTargets) != nil || workset.ValidateTargets(b.RepositoryTargets) != nil || !purposePrimaryInTargets(b.PrimaryMemberID, b.RepositoryTargets) || (b.WorkSpec != nil && (b.WorkSpec.ImplementationMode != b.ImplementationMode || purposeWorkSpecValid(*b.WorkSpec) != nil)) {
				return ErrIneligible
			}
		case "REVIEW":
			b := v.Review
			if b == nil || v.Implementation != nil || v.Verification != nil || b.SourceReviewID != p.SourceReviewID || strings.TrimSpace(b.ReviewInstruction) == "" || strings.TrimSpace(b.SupplementalPurpose) == "" || !b.ReviewDepth.Valid() || !purposeReviewPlanValid(b.ReviewPlan) || workset.ValidateTargets(b.RepositoryTargets) != nil {
				return ErrIneligible
			}
		}
	}
	return nil
}

func purposeWorkSpecValid(work task.WorkSpec) error {
	return task.ValidateTargetWorkSpec(work)
}

func purposePrimaryInTargets(primary string, targets []workset.Target) bool {
	for _, target := range targets {
		if target.MemberID == primary {
			return true
		}
	}
	return false
}

func purposeReviewPlanValid(plan run.ReviewPlan) bool {
	switch plan.Mode {
	case run.ReviewModeRepository:
		return plan.InitialScope.Kind == run.ReviewScopeRepository && len(plan.InitialScope.Paths) == 0 && len(plan.SelectedModules) == 0
	case run.ReviewModeFocused:
		if plan.InitialScope.Kind != run.ReviewScopePaths || len(plan.InitialScope.Paths) == 0 || len(plan.SelectedModules) == 0 {
			return false
		}
		seenPaths, seenModules, primary := map[string]bool{}, map[string]bool{}, false
		for _, value := range plan.InitialScope.Paths {
			if value == "" || value == "." || strings.HasPrefix(value, "/") || value == ".." || strings.HasPrefix(value, "../") || path.Clean(value) != value || seenPaths[value] {
				return false
			}
			seenPaths[value] = true
		}
		for _, module := range plan.SelectedModules {
			if module.Key == "" || seenModules[module.Key] || (module.Role != run.ReviewModulePrimary && module.Role != run.ReviewModuleSupporting) {
				return false
			}
			seenModules[module.Key] = true
			primary = primary || module.Role == run.ReviewModulePrimary
		}
		return primary
	}
	return false
}
