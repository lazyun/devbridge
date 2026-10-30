package contextprobe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/repository"
	"github.com/lazyun/devbridge/internal/run"
)

const CanonicalInspectionContractVersion = 1

// CanonicalTarget is the exact, user-confirmed read boundary for one Project
// Inspection repository. MemberID is deterministic and survives restart.
type CanonicalTarget struct {
	RepositoryID   string `json:"repositoryId"`
	RepositoryName string `json:"repositoryName"`
	MemberID       string `json:"memberId"`
	MemberVersion  int    `json:"memberVersion"`
	OriginCommit   string `json:"originCommit"`
	OriginRef      string `json:"originRef,omitempty"`
	Scope          Scope  `json:"scope"`
}

type PrepareInspectionInput struct {
	AISessionID         string                            `json:"aiSessionId"`
	RequestID           string                            `json:"requestId"`
	CodexInstruction    string                            `json:"codexInstruction"`
	Backend             codexconfig.BackendSelectionInput `json:"backend"`
	Model               string                            `json:"model"`
	ReasoningEffort     string                            `json:"reasoningEffort"`
	ServiceTier         string                            `json:"serviceTier,omitempty"`
	PrimaryRepositoryID string                            `json:"primaryRepositoryId"`
	Targets             []TargetSelection                 `json:"targets"`
}

type TargetSelection struct {
	RepositoryID string `json:"repositoryId"`
	Scope        Scope  `json:"scope"`
}

// InspectionConfirmationPlan is a pure, deterministic projection. Preparing
// it performs reads only. Confirm persists the exact plan and rejects drift.
type InspectionConfirmationPlan struct {
	ContractVersion  int                          `json:"contractVersion"`
	AISessionID      string                       `json:"aiSessionId"`
	ProjectID        string                       `json:"projectId"`
	RequestID        string                       `json:"requestId"`
	InspectionID     string                       `json:"inspectionId"`
	TaskID           string                       `json:"taskId"`
	PayloadSHA256    string                       `json:"payloadSha256"`
	CodexInstruction string                       `json:"codexInstruction"`
	Backend          codexconfig.BackendSelection `json:"backend"`
	Model            string                       `json:"model"`
	ReasoningEffort  string                       `json:"reasoningEffort"`
	ServiceTier      string                       `json:"serviceTier,omitempty"`
	PrimaryMemberID  string                       `json:"primaryMemberId"`
	Targets          []CanonicalTarget            `json:"targets"`
}

type ConfirmInspectionInput struct {
	Plan          InspectionConfirmationPlan `json:"plan"`
	PayloadSHA256 string                     `json:"payloadSha256"`
}

type StartInspectionInput struct {
	AISessionID           string `json:"aiSessionId"`
	ProjectID             string `json:"projectId"`
	InspectionOwnerTaskID string `json:"inspectionOwnerTaskId"`
	InspectionOperationID string `json:"inspectionOperationId"`
	ExpectedOwnerState    Status `json:"expectedOwnerState"`
	ExpectedRuntimeEpoch  string `json:"expectedRuntimeEpoch,omitempty"`
	ExpectedWorksetSHA256 string `json:"expectedWorksetSha256,omitempty"`
	RequestID             string `json:"requestId"`
	PayloadSHA256         string `json:"payloadSha256"`
}

// ConfirmedInspection is the store boundary used to create the Project Task,
// ContextProbe, and immutable confirmation receipt in one transaction.
type ConfirmedInspection struct {
	Probe       Probe
	Plan        InspectionConfirmationPlan
	ReceiptJSON string
}

type repositoryStateLookup interface {
	CurrentState(context.Context, string) (repository.State, error)
}

type canonicalInspectionStore interface {
	ConfirmCanonicalInspection(context.Context, ConfirmedInspection) (Probe, error)
}

func canonicalInspectionID(projectID, requestID string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00PROJECT_INSPECTION\x00" + requestID))
	return "inspection_" + hex.EncodeToString(sum[:16])
}

func canonicalMemberID(projectID, inspectionID, repositoryID string) string {
	sum := sha256.Sum256([]byte(projectID + "\x00PROJECT_INSPECTION\x00" + inspectionID + "\x00" + repositoryID))
	return "member_inspection_" + hex.EncodeToString(sum[:12])
}

func inspectionPlanHash(plan InspectionConfirmationPlan) (string, error) {
	plan.PayloadSHA256 = ""
	raw, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (s *Service) PrepareInspection(ctx context.Context, in PrepareInspectionInput) (InspectionConfirmationPlan, error) {
	if strings.TrimSpace(in.AISessionID) == "" || strings.TrimSpace(in.RequestID) == "" || len(in.RequestID) > 256 ||
		strings.TrimSpace(in.CodexInstruction) == "" || len(in.CodexInstruction) > 64<<10 || strings.ContainsRune(in.CodexInstruction, '\x00') ||
		in.Backend.ConfigID == "" || in.Backend.ConfigFingerprint == "" || in.Model == "" || in.ReasoningEffort == "" ||
		len(in.ServiceTier) > 128 || strings.ContainsRune(in.ServiceTier, '\x00') || in.PrimaryRepositoryID == "" || len(in.Targets) == 0 || len(in.Targets) > 128 {
		return InspectionConfirmationPlan{}, ErrInvalidArgument
	}
	session, err := s.sessions.Get(ctx, in.AISessionID)
	if err != nil {
		return InspectionConfirmationPlan{}, err
	}
	config, err := s.configs.Get(ctx, in.Backend.ConfigID)
	if err != nil {
		return InspectionConfirmationPlan{}, err
	}
	if !config.Enabled || config.Fingerprint() != in.Backend.ConfigFingerprint {
		return InspectionConfirmationPlan{}, ErrConflict
	}
	stateLookup, ok := s.repositories.(repositoryStateLookup)
	if !ok {
		return InspectionConfirmationPlan{}, fmt.Errorf("%w: repository state lookup required", ErrConflict)
	}
	targets := make([]CanonicalTarget, 0, len(in.Targets))
	seen := map[string]bool{}
	for _, supplied := range in.Targets {
		if supplied.RepositoryID == "" || seen[supplied.RepositoryID] {
			return InspectionConfirmationPlan{}, ErrInvalidArgument
		}
		seen[supplied.RepositoryID] = true
		repo, getErr := s.repositories.Get(ctx, supplied.RepositoryID)
		if getErr != nil {
			return InspectionConfirmationPlan{}, getErr
		}
		if repo.ProjectID != session.ProjectID {
			return InspectionConfirmationPlan{}, ErrNotFound
		}
		scope, scopeErr := validateScope(repo.Path, supplied.Scope)
		if scopeErr != nil {
			return InspectionConfirmationPlan{}, scopeErr
		}
		state, stateErr := stateLookup.CurrentState(ctx, repo.ID)
		if stateErr != nil {
			return InspectionConfirmationPlan{}, stateErr
		}
		if state.HeadCommit == "" {
			return InspectionConfirmationPlan{}, ErrConflict
		}
		inspectionID := canonicalInspectionID(session.ProjectID, in.RequestID)
		targets = append(targets, CanonicalTarget{RepositoryID: repo.ID, RepositoryName: repo.Name, MemberID: canonicalMemberID(session.ProjectID, inspectionID, repo.ID), MemberVersion: 1, OriginCommit: state.HeadCommit, OriginRef: state.BranchRef, Scope: scope})
	}
	if !seen[in.PrimaryRepositoryID] {
		return InspectionConfirmationPlan{}, ErrInvalidArgument
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].RepositoryID < targets[j].RepositoryID })
	inspectionID := canonicalInspectionID(session.ProjectID, in.RequestID)
	primaryMemberID := canonicalMemberID(session.ProjectID, inspectionID, in.PrimaryRepositoryID)
	plan := InspectionConfirmationPlan{ContractVersion: CanonicalInspectionContractVersion, AISessionID: session.ID, ProjectID: session.ProjectID, RequestID: in.RequestID, InspectionID: inspectionID, TaskID: "work_" + inspectionID, CodexInstruction: in.CodexInstruction, Backend: config.Selection("PROJECT_INSPECTION_CONFIRMATION"), Model: in.Model, ReasoningEffort: in.ReasoningEffort, ServiceTier: in.ServiceTier, PrimaryMemberID: primaryMemberID, Targets: targets}
	plan.PayloadSHA256, err = inspectionPlanHash(plan)
	if err != nil {
		return InspectionConfirmationPlan{}, err
	}
	return plan, nil
}

func (s *Service) ConfirmInspection(ctx context.Context, in ConfirmInspectionInput) (Probe, error) {
	plan := in.Plan
	if plan.ContractVersion != CanonicalInspectionContractVersion || plan.RequestID == "" || plan.AISessionID == "" || plan.ProjectID == "" || plan.InspectionID != canonicalInspectionID(plan.ProjectID, plan.RequestID) || plan.TaskID != "work_"+plan.InspectionID || plan.PrimaryMemberID == "" || len(plan.Targets) == 0 || strings.TrimSpace(plan.CodexInstruction) == "" || len(plan.CodexInstruction) > 64<<10 || strings.ContainsRune(plan.CodexInstruction, '\x00') || plan.Model == "" || plan.ReasoningEffort == "" || len(plan.ServiceTier) > 128 || strings.ContainsRune(plan.ServiceTier, '\x00') || in.PayloadSHA256 == "" || plan.PayloadSHA256 != in.PayloadSHA256 {
		return Probe{}, ErrInvalidArgument
	}
	hash, err := inspectionPlanHash(plan)
	if err != nil || hash != in.PayloadSHA256 {
		return Probe{}, ErrConflict
	}
	session, err := s.sessions.Get(ctx, plan.AISessionID)
	if err != nil {
		return Probe{}, err
	}
	if session.ProjectID != plan.ProjectID {
		return Probe{}, ErrNotFound
	}
	config, err := s.configs.Get(ctx, plan.Backend.ConfigID)
	if err != nil {
		return Probe{}, err
	}
	if !config.Enabled || plan.Backend != config.Selection("PROJECT_INSPECTION_CONFIRMATION") {
		return Probe{}, ErrConflict
	}
	stateLookup, ok := s.repositories.(repositoryStateLookup)
	if !ok {
		return Probe{}, fmt.Errorf("%w: repository state lookup required", ErrConflict)
	}
	primaryRepositoryID := ""
	seen := map[string]bool{}
	previousRepositoryID := ""
	for _, target := range plan.Targets {
		if target.RepositoryID == "" || target.RepositoryName == "" || seen[target.RepositoryID] || previousRepositoryID >= target.RepositoryID || target.MemberID != canonicalMemberID(plan.ProjectID, plan.InspectionID, target.RepositoryID) || target.MemberVersion != 1 || target.OriginCommit == "" {
			return Probe{}, ErrInvalidArgument
		}
		seen[target.RepositoryID] = true
		previousRepositoryID = target.RepositoryID
		repo, getErr := s.repositories.Get(ctx, target.RepositoryID)
		if getErr != nil {
			return Probe{}, getErr
		}
		if repo.ProjectID != plan.ProjectID || repo.Name != target.RepositoryName {
			return Probe{}, ErrConflict
		}
		scope, scopeErr := validateScope(repo.Path, target.Scope)
		if scopeErr != nil || !reflect.DeepEqual(scope, target.Scope) {
			return Probe{}, ErrConflict
		}
		state, stateErr := stateLookup.CurrentState(ctx, repo.ID)
		if stateErr != nil {
			return Probe{}, stateErr
		}
		if state.HeadCommit != target.OriginCommit || state.BranchRef != target.OriginRef {
			return Probe{}, ErrConflict
		}
		if target.MemberID == plan.PrimaryMemberID {
			primaryRepositoryID = target.RepositoryID
		}
	}
	if primaryRepositoryID == "" {
		return Probe{}, ErrInvalidArgument
	}
	if _, err := s.contexts.EnsureRequired(ctx, plan.ProjectID); err != nil {
		return Probe{}, err
	}
	now := s.now().UTC()
	receipt, err := json.Marshal(plan)
	if err != nil {
		return Probe{}, err
	}
	value := Probe{TaskID: plan.TaskID, ID: plan.InspectionID, AISessionID: plan.AISessionID, ProjectID: plan.ProjectID, RepositoryID: primaryRepositoryID, CodexConfigID: plan.Backend.ConfigID, Scope: Scope{Kind: ScopeRepository, Paths: []string{}}, CodexInstruction: plan.CodexInstruction, Status: StatusCreated, TokenUsage: run.TokenUsageAccounting{Status: run.TokenUsagePending}, Canonical: &plan, CanonicalRequestID: plan.RequestID, CanonicalPayloadSHA256: plan.PayloadSHA256, PrimaryMemberID: plan.PrimaryMemberID, CreatedAt: now, UpdatedAt: now}
	store, ok := s.store.(canonicalInspectionStore)
	if !ok {
		return Probe{}, fmt.Errorf("%w: canonical Project Inspection store required", ErrConflict)
	}
	return store.ConfirmCanonicalInspection(ctx, ConfirmedInspection{Probe: value, Plan: plan, ReceiptJSON: string(receipt)})
}
