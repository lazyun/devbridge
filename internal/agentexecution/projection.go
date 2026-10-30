package agentexecution

import (
	"github.com/lazyun/devbridge/internal/run"
	"sort"
	"time"
)

// Facts are observations of authoritative semantic owners. They never confer
// mutation authority and are not a second persisted lifecycle.
type RuntimeFacts struct {
	FinalizationPending         bool
	BackendDisposition          string
	CancellationState           string
	UpperInterruptionSuperseded bool
}

type Facts struct {
	AuthorizationRequired bool
	Runtime               map[string]RuntimeFacts
	Runs                  []run.Run
	CurrentRunID          string
	DomainStatus          string
	DomainResult          string
	DevelopmentOutcome    string
	VerificationRequired  bool
	CancellationState     string
	BackendDisposition    string
	RecoveryRequired      bool
	FinalizationPending   bool
}

type Projection struct {
	UserAuthorizationRequired bool           `json:"userAuthorizationRequired"`
	ExecutionID               string         `json:"executionId"`
	ProjectID                 string         `json:"projectId"`
	TaskID                    string         `json:"taskId"`
	FeatureAreaID             string         `json:"featureAreaId,omitempty"`
	Kind                      Kind           `json:"kind"`
	SemanticOwnerKind         string         `json:"semanticOwnerKind"`
	SemanticOwnerID           string         `json:"semanticOwnerId"`
	ExecutionProposalID       string         `json:"executionProposalId,omitempty"`
	ProvenanceKind            ProvenanceKind `json:"provenanceKind"`
	RunID                     string         `json:"runId,omitempty"`
	RunIDs                    []string       `json:"runIds"`
	Stage                     string         `json:"stage"`
	Status                    string         `json:"status"`
	AgentStatus               string         `json:"agentStatus"`
	DomainStatus              string         `json:"domainStatus,omitempty"`
	DomainResult              string         `json:"domainResult,omitempty"`
	BackendDisposition        string         `json:"backendDisposition"`
	CancellationState         string         `json:"cancellationState,omitempty"`
	RecoveryRequired          bool           `json:"recoveryRequired"`
	FinalizationPending       bool           `json:"finalizationPending"`
	ReadOnly                  bool           `json:"readOnly"`
	UpdatedAt                 time.Time      `json:"updatedAt"`
}

// Terminal describes the end of Bridge-owned work, not proof of physical
// backend death. UNKNOWN_ORPHANED retains that distinction in Status and
// BackendDisposition. These helpers consume Reduce's result, never domain rows.
func (p Projection) Terminal() bool {
	switch p.Status {
	case "COMPLETED", "FAILED", "REJECTED", "CANCELED", "UNKNOWN_ORPHANED":
		return true
	default:
		return false
	}
}

// AuthorizationActionable is the pending DEVELOPMENT user decision, not a
// runtime cancellation or recovery grant. Those remain semantic-owner actions.
func (p Projection) AuthorizationActionable() bool {
	return p.Kind == KindDevelopment && p.Status == "PENDING" && p.UserAuthorizationRequired &&
		!p.ReadOnly && !p.RecoveryRequired && !p.FinalizationPending && p.RunID == "" && len(p.RunIDs) == 0
}

// Reduce is the single product Execution reducer. Selection is limited to runs
// with the exact immutable association; recency never establishes ownership.
func Reduce(e Execution, f Facts) Projection {
	p := Projection{UserAuthorizationRequired: f.AuthorizationRequired, ExecutionID: e.ID, ProjectID: e.ProjectID, TaskID: e.TaskID, FeatureAreaID: e.FeatureAreaID, Kind: e.Kind, SemanticOwnerKind: e.SemanticOwnerKind, SemanticOwnerID: e.SemanticOwnerID, ProvenanceKind: e.ProvenanceKind, RunIDs: []string{}, Stage: string(e.Kind), Status: "PENDING", AgentStatus: "NOT_STARTED", DomainStatus: f.DomainStatus, DomainResult: f.DomainResult, BackendDisposition: "NOT_STARTED", CancellationState: f.CancellationState, RecoveryRequired: f.RecoveryRequired, FinalizationPending: f.FinalizationPending, ReadOnly: string(e.ProvenanceKind) == "LEGACY_UNKNOWN", UpdatedAt: e.CreatedAt}
	if string(e.Kind) == "DEVELOPMENT" {
		p.ExecutionProposalID = e.ExecutionProposalID
	}
	exact := make([]run.Run, 0, len(f.Runs))
	for _, r := range f.Runs {
		if r.AgentExecutionID != e.ID || r.TaskID != e.TaskID || r.ProjectID != e.ProjectID || r.FeatureAreaID != e.FeatureAreaID {
			continue
		}
		if !p.ReadOnly && (string(r.OperationKind) != e.SemanticOwnerKind || r.OperationID != e.SemanticOwnerID) {
			continue
		}
		exact = append(exact, r)
	}
	sort.Slice(exact, func(i, j int) bool {
		if exact[i].CreatedAt.Equal(exact[j].CreatedAt) {
			return exact[i].ID < exact[j].ID
		}
		return exact[i].CreatedAt.Before(exact[j].CreatedAt)
	})
	var selected *run.Run
	for i := range exact {
		r := &exact[i]
		p.RunIDs = append(p.RunIDs, r.ID)
		if r.UpdatedAt.After(p.UpdatedAt) {
			p.UpdatedAt = r.UpdatedAt
		}
		if f.CurrentRunID == r.ID || f.CurrentRunID == "" {
			selected = r
		}
	}
	upperInterruptionSuperseded := false
	if selected != nil {
		if runtime, ok := f.Runtime[selected.ID]; ok {
			upperInterruptionSuperseded = runtime.UpperInterruptionSuperseded
			f.FinalizationPending = runtime.FinalizationPending || f.FinalizationPending
			if runtime.BackendDisposition != "" {
				f.BackendDisposition = runtime.BackendDisposition
			}
			if runtime.CancellationState != "" {
				p.CancellationState = runtime.CancellationState
			}
			p.FinalizationPending = f.FinalizationPending
		}
		p.RunID = selected.ID
		p.Stage = string(selected.Purpose)
		p.AgentStatus = string(selected.Status)
		p.Status = p.AgentStatus
		p.BackendDisposition = "ACTIVE"
		switch selected.Status {
		case run.StatusSucceeded:
			p.Status = "COMPLETED"
			p.BackendDisposition = "COMPLETED"
		case run.StatusFailed, run.StatusInfrastructureFailed:
			p.Status = "FAILED"
			p.BackendDisposition = "TERMINAL"
		case run.StatusCanceled:
			p.BackendDisposition = "TERMINAL"
		}
	}
	if string(e.Kind) == "DEVELOPMENT" {
		// Authorization rejection precedes the default pending outcome. Other
		// terminal results remain owned by the canonical Development outcome.
		switch {
		case f.DomainStatus == "REJECTED":
			p.Status = "REJECTED"
			p.UserAuthorizationRequired = false
		case f.DevelopmentOutcome == "RUNNING":
			p.Status = "EXECUTING"
			if selected != nil && selected.Purpose == run.PurposeVerification {
				p.Status = "VERIFYING"
			}
			if selected != nil && selected.Status == run.StatusSucceeded {
				p.Status = "FINALIZING"
				if f.VerificationRequired && selected.Purpose == run.PurposeImplementation {
					p.Stage = "VERIFICATION"
					p.Status = "PENDING"
				}
			}
		case f.DevelopmentOutcome == "SUCCEEDED":
			p.UserAuthorizationRequired = false
			p.Status = "COMPLETED"
		case f.DevelopmentOutcome == "FAILED":
			p.UserAuthorizationRequired = false
			p.Status = "FAILED"
		case f.DevelopmentOutcome == "CANCELED":
			p.UserAuthorizationRequired = false
			p.Status = "CANCELED"
		}
	}
	if f.FinalizationPending && p.Status == "COMPLETED" {
		p.Status = "FINALIZING"
	}
	if f.BackendDisposition != "" {
		p.BackendDisposition = f.BackendDisposition
	}
	if p.BackendDisposition == "UNKNOWN" || p.BackendDisposition == "UNKNOWN_ORPHANED" {
		p.Status = p.BackendDisposition
		p.RecoveryRequired = p.BackendDisposition == "UNKNOWN"
	}
	if e.Kind == KindReview && !p.ReadOnly && selected != nil && selected.ID == f.CurrentRunID && upperInterruptionSuperseded && !f.FinalizationPending && p.CancellationState == "" && p.BackendDisposition == "UNKNOWN" {
		p.Status = string(selected.Status)
		if selected.Status == run.StatusSucceeded {
			p.Status = "COMPLETED"
		}
		p.RecoveryRequired = false
	}
	if p.ReadOnly {
		p.ReadOnly = true
	}
	return p
}
