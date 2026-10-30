// Package runtimepreparation owns the intended scope and durable authority
// identities used before a Run or managed worktree exists.
package runtimepreparation

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/lazyun/devbridge/internal/agentpolicy"
	"time"

	"github.com/lazyun/devbridge/internal/workset"
)

type OwnerKind string

const (
	OwnerExecution      OwnerKind = "EXECUTION"
	OwnerImplementation OwnerKind = "IMPLEMENTATION"
	OwnerVerification   OwnerKind = "VERIFICATION"
	OwnerReview         OwnerKind = "REVIEW"
	OwnerInspection     OwnerKind = "INSPECTION"
	OwnerRecoveryStatus OwnerKind = "RECOVERY_STATUS"
)

// Plan contains intended authority only. Actual Run, workspace, worktree,
// root, start HEAD, snapshot and RuntimeWorkset facts belong to later stages.
type Plan struct {
	AgentID                  string                        `json:"agentId,omitempty"`
	AgentConfigFingerprint   string                        `json:"agentConfigFingerprint,omitempty"`
	ExecutionPolicy          string                        `json:"executionPolicy,omitempty"`
	ID                       string                        `json:"planId"`
	Generation               int                           `json:"generation"`
	OwnerKind                OwnerKind                     `json:"ownerKind"`
	OwnerID                  string                        `json:"ownerId"`
	ProjectID                string                        `json:"projectId"`
	FeatureAreaID            string                        `json:"featureAreaId,omitempty"`
	TaskID                   string                        `json:"taskId,omitempty"`
	SourceCommand            string                        `json:"sourceCommand"`
	SourceReceiptID          string                        `json:"sourceReceiptId,omitempty"`
	SelectedVariantID        string                        `json:"selectedVariantId,omitempty"`
	ProposalHash             string                        `json:"proposalHash,omitempty"`
	TaskDefinitionRevisionID string                        `json:"taskDefinitionRevisionId,omitempty"`
	SourceExecutionID        string                        `json:"sourceExecutionId,omitempty"`
	SourceReviewID           string                        `json:"sourceReviewId,omitempty"`
	SuccessorExecutionID     string                        `json:"successorExecutionId,omitempty"`
	SuccessorReviewID        string                        `json:"successorReviewId,omitempty"`
	RepositoryTargets        []workset.Target              `json:"repositoryTargets"`
	PrimaryMemberID          string                        `json:"primaryMemberId"`
	BackendIdentity          string                        `json:"backendIdentity"`
	Model                    string                        `json:"model"`
	ReasoningEffort          string                        `json:"reasoningEffort"`
	RuntimeAdapterIdentity   string                        `json:"runtimeAdapterIdentity"`
	BackendConfigFingerprint string                        `json:"backendConfigFingerprint,omitempty"`
	BackendSource            string                        `json:"backendSource,omitempty"`
	BackendKind              string                        `json:"backendKind,omitempty"`
	CodexFastMode            bool                          `json:"codexFastMode,omitempty"`
	ServiceTier              string                        `json:"serviceTier,omitempty"`
	RuntimeProtocolIdentity  string                        `json:"runtimeProtocolIdentity,omitempty"`
	AuthoritySnapshotHash    string                        `json:"authoritySnapshotHash,omitempty"`
	TopologyRevision         int64                         `json:"topologyRevision"`
	DevelopmentContextHash   string                        `json:"developmentContextHash"`
	DevelopmentRefs          map[string]string             `json:"developmentRefs"`
	ExpectedSourceHEADs      map[string]string             `json:"expectedSourceHeads"`
	WriteScopes              map[string]workset.WriteScope `json:"writeScopes"`
	PreparationRequestID     string                        `json:"preparationRequestId"`
	PreparationPayloadHash   string                        `json:"preparationPayloadHash"`
	ContentHash              string                        `json:"contentHash"`
	CreatedAt                time.Time                     `json:"createdAt"`
}

func (p Plan) Validate() error {
	if err := agentpolicy.ValidateFrozenSelection(p.AgentID, p.ExecutionPolicy, p.AgentConfigFingerprint); err != nil {
		return err
	}
	if p.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 && (p.RuntimeAdapterIdentity != agentpolicy.CooperativeAdapterIdentity || p.RuntimeProtocolIdentity != agentpolicy.CooperativeProtocolIdentity) {
		return errors.New("cooperative runtime scope plan identity mismatch")
	}
	if p.ID == "" || p.Generation < 1 || p.OwnerID == "" || p.ProjectID == "" || p.SourceCommand == "" || p.PrimaryMemberID == "" || p.BackendIdentity == "" || p.Model == "" || p.ReasoningEffort == "" || p.RuntimeAdapterIdentity == "" || p.DevelopmentContextHash == "" || p.PreparationRequestID == "" || len(p.PreparationPayloadHash) != 64 || p.CreatedAt.IsZero() {
		return errors.New("runtime scope plan identity incomplete")
	}
	switch p.OwnerKind {
	case OwnerExecution, OwnerImplementation, OwnerVerification:
		if p.TaskID == "" || p.SuccessorExecutionID != "" && p.SuccessorExecutionID != p.OwnerID {
			return errors.New("execution owner identity mismatch")
		}
		if p.OwnerKind == OwnerExecution && (len(p.BackendConfigFingerprint) != 64 || p.BackendSource == "" || p.BackendKind == "" || p.RuntimeProtocolIdentity == "" || len(p.AuthoritySnapshotHash) != 64) {
			return errors.New("execution runtime authority snapshot incomplete")
		}
		if p.OwnerKind == OwnerExecution {
			hash, hashErr := p.ComputeAuthoritySnapshotHash()
			if hashErr != nil || hash != p.AuthoritySnapshotHash {
				return errors.New("execution runtime authority snapshot hash mismatch")
			}
		}
	case OwnerReview:
		if p.TaskID == "" || p.SuccessorReviewID != "" && p.SuccessorReviewID != p.OwnerID {
			return errors.New("review owner identity mismatch")
		}
	case OwnerInspection, OwnerRecoveryStatus:
	default:
		return errors.New("unknown runtime owner kind")
	}
	if err := workset.ValidateTargets(p.RepositoryTargets); err != nil {
		return err
	}
	primary := false
	for _, target := range p.RepositoryTargets {
		if target.ObservedCommit != "" {
			return errors.New("actual observation in runtime scope plan")
		}
		if target.MemberID == p.PrimaryMemberID {
			primary = true
		}
		if p.ExpectedSourceHEADs[target.MemberID] == "" || !workset.ValidCommit(p.ExpectedSourceHEADs[target.MemberID]) {
			return errors.New("expected source HEAD missing")
		}
	}
	if !primary {
		return errors.New("primary member outside plan")
	}
	expected, err := p.ComputeContentHash()
	if err != nil || expected != p.ContentHash {
		return errors.New("runtime scope plan hash mismatch")
	}
	return nil
}

// ComputeAuthoritySnapshotHash uses the frozen Skeleton field vocabulary and
// compact sorted-key JSON. Time, filesystem paths and display names are not
// part of this value identity.
func (p Plan) ComputeAuthoritySnapshotHash() (string, error) {
	value := map[string]any{
		"backendIdentity":          p.BackendIdentity,
		"backendConfigFingerprint": p.BackendConfigFingerprint,
		"backendSource":            p.BackendSource,
		"backendKind":              p.BackendKind,
		"model":                    p.Model,
		"reasoningEffort":          p.ReasoningEffort,
		"codexFastMode":            p.CodexFastMode,
		"serviceTier":              p.ServiceTier,
		"runtimeAdapterIdentity":   p.RuntimeAdapterIdentity,
		"runtimeProtocolIdentity":  p.RuntimeProtocolIdentity,
	}
	if p.AgentConfigFingerprint != "" {
		value["agentConfigFingerprint"] = p.AgentConfigFingerprint
	}
	if p.AgentID != "" {
		value["agentId"] = p.AgentID
	}
	if p.ExecutionPolicy != "" {
		value["executionPolicy"] = p.ExecutionPolicy
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (p Plan) ComputeContentHash() (string, error) {
	p.ContentHash = ""
	p.CreatedAt = time.Time{}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
