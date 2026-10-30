package recoverystatus

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/inspectionresult"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/workset"
)

type Assessment struct {
	ID                        string    `json:"id"`
	Source                    Source    `json:"sourceOperation"`
	RecoveryRunID             string    `json:"recoveryRunId"`
	RecoveryTurnID            string    `json:"recoveryTurnId,omitempty"`
	ReportID                  string    `json:"reportId"`
	FactualObservationHash    string    `json:"factualObservationHash"`
	ReportHash                string    `json:"reportHash"`
	InspectionResultHash      string    `json:"inspectionResultHash,omitempty"`
	RuntimeWorksetHash        string    `json:"runtimeWorksetHash,omitempty"`
	OwnerObservationHash      string    `json:"ownerObservationHash,omitempty"`
	Consistency               string    `json:"consistency"`
	EngineeringDisposition    string    `json:"engineeringDisposition"`
	SourceResponseDisposition string    `json:"sourceResponseDisposition"`
	ReasonCodes               []string  `json:"reasonCodes"`
	AvailableActions          []string  `json:"availableActions"`
	CreatedAt                 time.Time `json:"createdAt"`
}

// CheckEvidence is populated only from Core-observed command/Git facts, never
// from the Recovery report's verificationObservations.
type CheckEvidence struct {
	CheckID         string `json:"checkId"`
	Command         string `json:"command"`
	ExpectedCommand string `json:"expectedCommand"`
	Passed          bool   `json:"passed"`
	ExitCode        *int   `json:"exitCode"`
	EvidenceHash    string `json:"evidenceHash"`
}
type FactualObservation struct {
	SourcePurpose             string                 `json:"sourcePurpose"`
	OwnerResult               json.RawMessage        `json:"ownerResult,omitempty"`
	Source                    Source                 `json:"source"`
	RecoveryRunID             string                 `json:"recoveryRunId"`
	RecoveryTurnID            string                 `json:"recoveryTurnId,omitempty"`
	ReportID                  string                 `json:"reportId"`
	ReportHash                string                 `json:"reportHash"`
	Workset                   workset.RuntimeWorkset `json:"workset"`
	Before                    []WorktreeObservation  `json:"before"`
	After                     []WorktreeObservation  `json:"after"`
	SourceTurnTerminal        bool                   `json:"sourceTurnTerminal"`
	BackendQuiescent          bool                   `json:"backendQuiescent"`
	ScopeValid                bool                   `json:"scopeValid"`
	PendingRequest            bool                   `json:"pendingRequest"`
	ActualUserRequest         bool                   `json:"actualUserRequest"`
	OwnerContractValidated    bool                   `json:"ownerContractValidated"`
	OwnerContractEvidenceHash string                 `json:"ownerContractEvidenceHash"`
	ApprovedGoalIDs           []string               `json:"approvedGoalIds"`
	RequiredChecks            []string               `json:"requiredChecks"`
	CheckEvidence             []CheckEvidence        `json:"checkEvidence"`
	ReviewReport              *review.ReviewReport   `json:"reviewReport,omitempty"`
	SourceResponseDisposition string                 `json:"sourceResponseDisposition"`
	ObservedAt                time.Time              `json:"observedAt"`
}

// Assess is deterministic over persisted inputs. Its caller must gather facts
// from canonical stores and runtime observations, never from client booleans.
func Assess(r Report, f FactualObservation) (Assessment, error) {
	if f.RecoveryRunID == "" || f.ReportID == "" || f.ReportHash == "" || f.ObservedAt.IsZero() {
		return Assessment{}, errors.New("recovery assessment identity required")
	}
	raw, _ := json.Marshal(r)
	if _, err := ParseReport(raw, f.Source); err != nil {
		return Assessment{}, err
	}
	factBytes, _ := json.Marshal(f)
	a := Assessment{Source: f.Source, RecoveryRunID: f.RecoveryRunID, ReportID: f.ReportID, ReportHash: f.ReportHash, FactualObservationHash: digest(factBytes), SourceResponseDisposition: f.SourceResponseDisposition, Consistency: "INSUFFICIENT", EngineeringDisposition: "RECOVERY_INSUFFICIENT", ReasonCodes: []string{}, AvailableActions: []string{"READ", "CANCEL_OPERATION", "STOP_AND_REVISE"}, CreatedAt: f.ObservedAt}
	inspection := f.Source.OwnerKind == "PROJECT_INSPECTION"
	if inspection {
		a.RecoveryTurnID = f.RecoveryTurnID
		a.InspectionResultHash = digest(r.InspectionResult)
		a.RuntimeWorksetHash = f.Workset.ContentHash
		a.OwnerObservationHash = ownerObservationHash(f.Before, f.After)
	}
	finish := func() (Assessment, error) {
		b, _ := json.Marshal(a)
		a.ID = "recovery-assessment-" + digest(b)
		return a, nil
	}
	reason := func(s string) { a.ReasonCodes = append(a.ReasonCodes, s) }
	var inspectionReport inspectionresult.InspectionReport
	if inspection {
		if f.RecoveryTurnID == "" || len(r.InspectionResult) == 0 || len(f.OwnerResult) == 0 || string(r.InspectionResult) != string(f.OwnerResult) || f.Source.RuntimeWorksetHash != f.Workset.ContentHash || f.Source.WorkspaceID == "" || f.Source.WorkspaceID != f.Workset.WorkspaceID || !f.OwnerContractValidated || f.OwnerContractEvidenceHash == "" {
			reason("INSPECTION_RECOVERY_BINDING_MISSING")
			return finish()
		}
		parsed, err := inspectionresult.Parse(r.InspectionResult, f.Workset)
		if err != nil {
			reason("INSPECTION_RESULT_INVALID")
			return finish()
		}
		inspectionReport = parsed
		if parsed.Source.ProjectID != f.Source.ProjectID || parsed.Source.InspectionOwnerTaskID != f.Source.TaskID || parsed.Source.InspectionOperationID != f.Source.OwnerID || parsed.Source.RunID != f.Source.RunID || parsed.Source.RuntimeWorksetHash != f.Source.RuntimeWorksetHash {
			a.Consistency = "CONFLICT"
			reason("INSPECTION_RESULT_SOURCE_CONFLICT")
			return finish()
		}
	}
	if !f.SourceTurnTerminal || !f.BackendQuiescent {
		reason("RECOVERY_TERMINAL_QUIESCENCE_REQUIRED")
		return finish()
	}
	if f.ActualUserRequest && f.PendingRequest {
		a.Consistency = "CONSISTENT"
		a.EngineeringDisposition = "USER_DECISION_REQUIRED"
		a.AvailableActions = []string{"ANSWER_CODEX_REQUEST"}
		reason("RECOVERY_EXPLICIT_USER_RESPONSE_REQUIRED")
		return finish()
	}
	if f.PendingRequest {
		reason("RECOVERY_PENDING_REQUEST")
		return finish()
	}
	if !f.ScopeValid {
		a.Consistency = "CONFLICT"
		a.EngineeringDisposition = "RECOVERY_FAILED"
		reason("SCOPE_CONFLICT")
		return finish()
	}
	if f.Source.RuntimeWorksetHash != "" && f.Source.RuntimeWorksetHash != f.Workset.ContentHash {
		a.Consistency = "CONFLICT"
		reason("RECOVERY_WORKSET_DRIFT")
		return finish()
	}
	if len(f.Workset.Members) == 0 || len(f.Before) != len(f.Workset.Members) || len(f.After) != len(f.Workset.Members) {
		reason("RECOVERY_MEMBER_EVIDENCE_MISSING")
		return finish()
	}
	before, after := map[string]WorktreeObservation{}, map[string]WorktreeObservation{}
	for _, o := range f.Before {
		if _, ok := before[o.RuntimeAlias]; ok {
			reason("RECOVERY_MEMBER_DUPLICATE")
			return finish()
		}
		before[o.RuntimeAlias] = o
	}
	for _, o := range f.After {
		if _, ok := after[o.RuntimeAlias]; ok {
			reason("RECOVERY_MEMBER_DUPLICATE")
			return finish()
		}
		after[o.RuntimeAlias] = o
	}
	aliases := map[string]bool{}
	for _, m := range f.Workset.Members {
		b, bok := before[m.RepositoryAlias]
		e, eok := after[m.RepositoryAlias]
		aliases[m.RepositoryAlias] = true
		if !bok || !eok || !b.Available || !e.Available || b.State != "OBSERVED" || e.State != "OBSERVED" {
			reason("RECOVERY_MEMBER_UNAVAILABLE")
			return finish()
		}
		if !workset.ValidCommit(b.HEAD) || !workset.ValidCommit(e.HEAD) || !workset.ValidCommit(b.Snapshot) || !workset.ValidCommit(e.Snapshot) || len(b.MetadataDigest) != 64 || len(e.MetadataDigest) != 64 || len(b.DiffDigest) != 64 || len(e.DiffDigest) != 64 {
			reason("RECOVERY_MEMBER_EVIDENCE_MISSING")
			return finish()
		}
		if b.RepositoryID != m.RepositoryID || e.RepositoryID != m.RepositoryID || b.MemberID != m.MemberID || e.MemberID != m.MemberID || b.HEAD != m.StartHEAD || e.HEAD != m.StartHEAD || b.RootIdentity != m.RootFileIdentity || e.RootIdentity != m.RootFileIdentity || b.WorktreeID != m.WorktreeID || e.WorktreeID != m.WorktreeID || b.CanonicalRoot != m.CanonicalRoot || e.CanonicalRoot != m.CanonicalRoot || b.HEAD != e.HEAD || b.Snapshot != e.Snapshot || b.MetadataDigest != e.MetadataDigest || b.Branch != e.Branch || b.DiffDigest != e.DiffDigest {
			a.Consistency = "CONFLICT"
			a.EngineeringDisposition = "RECOVERY_FAILED"
			reason("RECOVERY_PROBE_MUTATION")
			return finish()
		}
		for _, p := range e.ChangedPaths {
			if !allowedPath(m.Write, p) {
				a.Consistency = "CONFLICT"
				a.EngineeringDisposition = "RECOVERY_FAILED"
				reason("SCOPE_CONFLICT")
				return finish()
			}
		}
	}
	repoSeen := map[string]bool{}
	for _, o := range r.RepositoryObservations {
		if !aliases[o.RuntimeAlias] {
			a.Consistency = "CONFLICT"
			reason("RECOVERY_UNKNOWN_REPOSITORY_ALIAS")
			return finish()
		}
		repoSeen[o.RuntimeAlias] = true
		actual := after[o.RuntimeAlias]
		for _, p := range o.ChangedPaths {
			if !contains(actual.ChangedPaths, p) {
				a.Consistency = "CONFLICT"
				reason("RECOVERY_REPORTED_CHANGE_MISSING")
				return finish()
			}
		}
		if r.OverallStatus == "COMPLETED" && o.Completion != "COMPLETED" {
			a.Consistency = "CONFLICT"
			reason("RECOVERY_MEMBER_INCOMPLETE")
			return finish()
		}
	}
	if len(repoSeen) != len(aliases) {
		reason("RECOVERY_MEMBER_REPORT_MISSING")
		return finish()
	}
	if r.ScopeExpansionRequired || r.RecommendedNextAction == "REVISE" {
		a.Consistency = "CONSISTENT"
		a.EngineeringDisposition = "REVISION_REQUIRED"
		a.AvailableActions = []string{"STOP_AND_REVISE"}
		reason("RECOVERY_SCOPE_REVISION_REQUIRED")
		return finish()
	}
	if r.RecommendedNextAction == "REQUEST_USER" {
		reason("RECOVERY_REPORT_ONLY_QUESTION")
		return finish()
	}
	if !inspection {
		goals := map[string]bool{}
		for _, g := range append(append([]GoalObservation{}, r.CompletedGoals...), r.RemainingGoals...) {
			if !contains(f.ApprovedGoalIDs, g.GoalID) {
				a.Consistency = "CONFLICT"
				reason("RECOVERY_UNKNOWN_GOAL")
				return finish()
			}
			goals[g.GoalID] = true
		}
		if len(goals) != len(f.ApprovedGoalIDs) {
			reason("RECOVERY_GOAL_EVIDENCE_MISSING")
			return finish()
		}
	}
	if inspection && r.OverallStatus == "PARTIAL" {
		// Project Inspection has no FeatureArea Task goal contract. Its partial
		// disposition is therefore derived solely from the independently parsed
		// owner result and the full workset/observation evidence above.
		if inspectionReport.Status != "BLOCKED" {
			a.Consistency = "CONFLICT"
			reason("INSPECTION_PARTIAL_STATUS_CONFLICT")
			return finish()
		}
		switch inspectionReport.RecommendedNextAction {
		case "INSPECT_MORE":
			a.Consistency = "CONSISTENT"
			a.EngineeringDisposition = "RECOVERED_PARTIAL"
			a.AvailableActions = []string{"CONTINUE_SAME_REVISION", "CANCEL_OPERATION", "STOP_AND_REVISE"}
			reason("INSPECTION_REMAINING_SCOPE")
			return finish()
		case "REQUEST_USER_DECISION", "CREATE_FEATURE_AREA", "CREATE_TASK":
			return applyInspectionPartialDisposition(&a, inspectionReport, reason, finish)
		default:
			reason("INSPECTION_PARTIAL_CONTINUATION_REQUIRED")
			return finish()
		}
	}
	if r.OverallStatus == "PARTIAL" && len(r.RemainingGoals) > 0 {
		implementation := f.Source.OperationKind == "EXECUTION" && f.SourcePurpose == "IMPLEMENTATION"
		// Older assessment inputs named Implementation directly; an ambiguous
		// EXECUTION alone cannot distinguish Implementation from Verification.
		implementation = implementation || (f.Source.OperationKind == "IMPLEMENTATION" && (f.SourcePurpose == "" || f.SourcePurpose == "IMPLEMENTATION"))
		if !implementation {
			reason("RECOVERY_PARTIAL_SOURCE_UNSUPPORTED")
			return finish()
		}
		a.Consistency = "CONSISTENT"
		a.EngineeringDisposition = "RECOVERED_PARTIAL"
		a.AvailableActions = []string{"CONTINUE_SAME_REVISION", "CANCEL_OPERATION", "STOP_AND_REVISE"}
		reason("RECOVERY_REMAINING_GOALS")
		return finish()
	}
	if r.OverallStatus != "COMPLETED" || len(r.RemainingGoals) > 0 || len(r.UnresolvedIssues) > 0 {
		reason("RECOVERY_INCOMPLETE_REPORT")
		return finish()
	}
	if inspection {
		if inspectionReport.Status != "SUCCEEDED" {
			a.Consistency = "CONFLICT"
			reason("INSPECTION_COMPLETE_STATUS_CONFLICT")
			return finish()
		}
		if inspectionReport.RecommendedNextAction != "NONE" {
			return applyInspectionPartialDisposition(&a, inspectionReport, reason, finish)
		}
	}
	for _, g := range r.CompletedGoals {
		if g.Status != "COMPLETED" {
			reason("RECOVERY_GOAL_INCOMPLETE")
			return finish()
		}
	}
	if !f.OwnerContractValidated || f.OwnerContractEvidenceHash == "" {
		reason("RECOVERY_OWNER_CONTRACT_EVIDENCE_MISSING")
		return finish()
	}
	if f.SourcePurpose == "VERIFICATION" || strings.Contains(f.Source.OperationKind, "VERIFICATION") {
		for _, id := range f.RequiredChecks {
			found := false
			for _, e := range f.CheckEvidence {
				if e.CheckID == id && e.Passed && e.EvidenceHash != "" && e.Command == e.ExpectedCommand && (e.Command == "" || (e.ExitCode != nil && *e.ExitCode == 0)) {
					found = true
				}
			}
			if !found {
				reason("RECOVERY_REQUIRED_CHECK_EVIDENCE_MISSING")
				return finish()
			}
		}
	}
	if strings.Contains(f.Source.OperationKind, "REVIEW") {
		v := f.ReviewReport
		if v == nil {
			v = r.ReviewReport
		}
		if v == nil || v.SchemaVersion != 2 || v.Validate() != nil {
			reason("RECOVERY_REVIEW_V2_REQUIRED")
			return finish()
		}
	}
	a.Consistency = "CONSISTENT"
	a.EngineeringDisposition = "RECOVERED_COMPLETE"
	a.AvailableActions = []string{"RESOLVE_RECOVERY"}
	reason("RECOVERY_OBJECTIVE_CONTRACT_CONFIRMED")
	return finish()
}

func ownerObservationHash(before, after []WorktreeObservation) string {
	material, _ := json.Marshal(struct {
		Before []WorktreeObservation `json:"before"`
		After  []WorktreeObservation `json:"after"`
	}{Before: before, After: after})
	return digest(material)
}

// applyInspectionPartialDisposition maps the frozen Inspection recommendation
// to an engineering-only state. It never treats a blocked or follow-up result
// as a completed Inspection.
func applyInspectionPartialDisposition(a *Assessment, result inspectionresult.InspectionReport, reason func(string), finish func() (Assessment, error)) (Assessment, error) {
	a.Consistency = "CONSISTENT"
	switch result.RecommendedNextAction {
	case "REQUEST_USER_DECISION":
		a.EngineeringDisposition = "USER_DECISION_REQUIRED"
		a.AvailableActions = []string{"ANSWER_CODEX_REQUEST"}
		reason("INSPECTION_USER_DECISION_REQUIRED")
	case "CREATE_FEATURE_AREA", "CREATE_TASK":
		a.EngineeringDisposition = "REVISION_REQUIRED"
		a.AvailableActions = []string{"STOP_AND_REVISE"}
		reason("INSPECTION_FOLLOW_UP_SCOPE_REQUIRED")
	default: // NONE and INSPECT_MORE retain the bounded Inspection owner.
		a.EngineeringDisposition = "RECOVERED_PARTIAL"
		a.AvailableActions = []string{"CONTINUE_SAME_REVISION", "CANCEL_OPERATION", "STOP_AND_REVISE"}
		reason("RECOVERY_REMAINING_GOALS")
	}
	return finish()
}
func contains(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}
func allowedPath(s workset.WriteScope, p string) bool {
	if !safePath(p) {
		return false
	}
	if s.Kind == "REPOSITORY" {
		return true
	}
	if s.Kind != "PATHS" {
		return false
	}
	for _, a := range s.Paths {
		a = strings.TrimSuffix(a, "/")
		if p == a || strings.HasPrefix(p, a+"/") {
			return true
		}
	}
	return false
}
func ValidateObservationReplay(before, after []WorktreeObservation) error {
	if ObservationsHash(before) != ObservationsHash(after) {
		return fmt.Errorf("RECOVERY_OBSERVATION_DRIFT")
	}
	return nil
}
