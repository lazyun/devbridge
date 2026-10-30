package formalexecution

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/formalapproval"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// AdoptionArchive records an independent pre-Run confirmation. It either
// admits a definition-only approval or reuses an exact runtime-authorized
// admission. The prior receipt never supplies this request's completion.
type AdoptionArchive struct {
	Identity               ReadIdentity                          `json:"identity"`
	FrozenPrepareJSON      json.RawMessage                       `json:"frozenPrepareJson"`
	Commit                 confirmation.CommitInput              `json:"commit"`
	OriginalRequestJSON    json.RawMessage                       `json:"originalRequestJson"`
	PriorApprovalRole      string                                `json:"priorApprovalRole"`
	PriorApproval          formalapproval.Receipt                `json:"priorApproval"`
	PriorRuntimeApproval   *Receipt                              `json:"priorRuntimeApproval,omitempty"`
	AtomicConfirmation     bool                                  `json:"atomicConfirmation,omitempty"`
	ProjectID              string                                `json:"projectId"`
	TaskID                 string                                `json:"taskId"`
	FeatureAreaID          string                                `json:"featureAreaId"`
	RevisionID             string                                `json:"revisionId"`
	RevisionHash           string                                `json:"revisionHash"`
	DefinitionJSON         json.RawMessage                       `json:"definitionJson"`
	ExpectedTarget         wp.TaskExecutionExpectation           `json:"expectedTarget"`
	FrozenLineage          formalapproval.Lineage                `json:"frozenLineage"`
	ResultLineage          formalapproval.Lineage                `json:"resultLineage"`
	PlatformReference      runtimeselection.PlatformReference    `json:"platformReference"`
	Descriptor             runtimeplatform.Descriptor            `json:"descriptor"`
	Evidence               runtimeplatform.CompatibilityEvidence `json:"evidence"`
	Qualification          runtimeplatform.Qualification         `json:"qualification"`
	Runtime                codexruntime.NextRunRuntimeConfig     `json:"runtime"`
	Backend                codexconfig.BackendSelection          `json:"backend"`
	ExecutionID            string                                `json:"executionId"`
	AdmissionRequestID     string                                `json:"admissionRequestId"`
	AdmissionPayloadHash   string                                `json:"admissionPayloadHash"`
	Plan                   runtimepreparation.Plan               `json:"plan"`
	PreparationAuthorityID string                                `json:"preparationAuthorityId"`
}

type AdoptionReceipt struct {
	Archive                AdoptionArchive `json:"archive"`
	BusinessHash           string          `json:"businessHash"`
	ReceiptIdentity        string          `json:"receiptIdentity"`
	Role                   string          `json:"role"`
	ProducerKind           string          `json:"producerKind"`
	TransitionID           string          `json:"transitionId"`
	BusinessOutcome        string          `json:"businessOutcome"`
	ConsumptionDisposition string          `json:"consumptionDisposition"`
}

type AdoptionStore interface {
	ReadAdoptionRequired(context.Context, ReadIdentity) (bool, error)
	ReadAdoption(context.Context, ReadIdentity) (AdoptionReceipt, error)
	CommitAdoption(context.Context, ReadIdentity, runtimeselection.PlatformReference) (AdoptionReceipt, error)
}

func (a AdoptionArchive) Validate() error {
	id, c := a.Identity, a.Commit
	if a.PriorApprovalRole != formalapproval.PriorReceiptRole || id.Validate() != nil || id.TypedPayloadHash != HashCommit(c) || c.AISessionID != id.AISessionID || c.IntentID != id.IntentID || c.RequestID != id.RequestID || c.Version != id.IntentVersion || c.ContentHash != id.ContentHash || c.Purpose != confirmation.Execution || c.Action != "START_APPROVED" || !exact(c.VariantID) || c.Selection.RuntimeConfig == nil || c.Selection.Backend == nil || c.BackendSelection == nil || *c.Selection.RuntimeConfig != a.Runtime || *c.BackendSelection != a.Backend || c.Selection.Backend.ConfigID != a.Backend.ConfigID || c.Selection.Backend.ConfigFingerprint != a.Backend.ConfigFingerprint {
		return ErrCorrupt
	}
	var prepared confirmation.PrepareInput
	if Decode(a.FrozenPrepareJSON, id.ContentHash, &prepared) != nil {
		return ErrCorrupt
	}
	b := prepared.Execution
	if prepared.AISessionID != id.AISessionID || prepared.Purpose != confirmation.Execution || prepared.Action != "START_APPROVED" || b == nil || b.Action != prepared.Action || b.TaskID != a.TaskID || b.FeatureAreaID != a.FeatureAreaID || b.TaskDefinitionRevisionID != a.RevisionID || b.ExpectedTarget == nil || *b.ExpectedTarget != a.ExpectedTarget || a.ExpectedTarget.FeatureAreaID != a.FeatureAreaID || a.ExpectedTarget.TaskDefinitionRevisionID != a.RevisionID || a.ExpectedTarget.Phase != task.PhaseImplementationReady {
		return ErrCorrupt
	}
	var original confirmation.CommitInput
	if Decode(a.OriginalRequestJSON, HashBytes(a.OriginalRequestJSON), &original) != nil || original.AISessionID != c.AISessionID || original.IntentID != c.IntentID || original.Version != c.Version || original.Purpose != c.Purpose || original.Action != c.Action || original.RequestID != c.RequestID || original.ContentHash != c.ContentHash || original.VariantID != c.VariantID {
		return ErrCorrupt
	}
	var schema formalapproval.SchemaReference
	var reviewID, decisionID, decisionVersion string
	if p := a.PriorRuntimeApproval; p != nil {
		if !a.AtomicConfirmation || p.Validate() != nil || p.ProducerKind != ProducerKind || p.ReceiptIdentity == runtimeselection.ReceiptIdentity(id.AISessionID, id.RequestID) || Hash(a.PriorApproval) != Hash(formalapproval.Receipt{}) || p.Archive.Fence.ProjectID != a.ProjectID || p.Archive.Fence.TaskID != a.TaskID || p.Archive.RevisionID != a.RevisionID || p.Archive.RevisionHash != a.RevisionHash || !reflect.DeepEqual(p.Archive.DefinitionJSON, a.DefinitionJSON) || a.ExpectedTarget.ExecutionID != p.T2.ExecutionID || a.ExecutionID != p.T2.ExecutionID || !reflect.DeepEqual(a.Plan, p.Plan) || a.PreparationAuthorityID != p.PreparationAuthorityID || a.AdmissionRequestID != p.Plan.PreparationRequestID || a.AdmissionPayloadHash != p.Plan.PreparationPayloadHash || a.Runtime != p.Archive.Runtime || a.Backend != p.Archive.Backend || a.PlatformReference != p.Archive.PlatformReference {
			return ErrCorrupt
		}
		schema, reviewID, decisionID, decisionVersion = p.Archive.Fence.Schema, p.Archive.Fence.ReviewID, p.Archive.Fence.DecisionID, p.Archive.Fence.DecisionVersion
	} else {
		p := a.PriorApproval
		if p.Validate() != nil || p.ProducerKind != formalapproval.ProducerKind || p.Role != formalapproval.CurrentReceiptRole || p.ReceiptIdentity == runtimeselection.ReceiptIdentity(id.AISessionID, id.RequestID) {
			return ErrCorrupt
		}
		prior := p.Snapshot
		if prior.Fence.ProjectID != a.ProjectID || prior.Fence.TaskID != a.TaskID || prior.RevisionID != a.RevisionID || prior.RevisionHash != a.RevisionHash || !reflect.DeepEqual(prior.DefinitionJSON, a.DefinitionJSON) {
			return ErrCorrupt
		}
		schema, reviewID, decisionID, decisionVersion = prior.Fence.Schema, prior.Fence.ReviewID, prior.Fence.DecisionID, prior.Fence.DecisionVersion
	}
	d, err := formalapproval.DecodeDefinition(a.DefinitionJSON, a.RevisionHash, schema)
	if err != nil || d.ProjectID != a.ProjectID || d.FeatureAreaID != a.FeatureAreaID || d.WorkSpec.ImplementationMode != task.ImplementationCodingAgent || d.PrimaryMemberID != a.ExpectedTarget.PrimaryMemberID || a.ExpectedTarget.ApprovedWorksetHash != Hash(d.RepositoryTargets) {
		return ErrCorrupt
	}
	matched := 0
	for _, v := range prepared.Variants {
		if v.ID == c.VariantID {
			matched++
			if v.ImplementationMode != task.ImplementationCodingAgent || v.WorkSpec == nil || v.VerificationSpec == nil || v.PrimaryMemberID != d.PrimaryMemberID || !reflect.DeepEqual(v.RepositoryTargets, d.RepositoryTargets) || !reflect.DeepEqual(*v.WorkSpec, d.WorkSpec) || !reflect.DeepEqual(*v.VerificationSpec, d.VerificationSpec) {
				return ErrCorrupt
			}
		}
	}
	if matched != 1 || !exact(a.ExecutionID) || (a.PriorRuntimeApproval == nil && a.AdmissionRequestID != "confirmation_"+Hash([]string{id.IntentID, "START_IMPLEMENTATION"})[:32]) || !runtimeplatform.ValidDigest(a.AdmissionPayloadHash) {
		return ErrCorrupt
	}
	l := formalapproval.Lineage{ProjectID: a.ProjectID, TaskID: a.TaskID, ReviewID: reviewID, DecisionID: decisionID, DecisionVersion: decisionVersion, IntentID: id.IntentID, IntentVersion: id.IntentVersion, VariantID: c.VariantID, CommittedResultID: a.ExecutionID, TaskDefinitionRevisionID: a.RevisionID}
	if a.FrozenLineage != l || a.ResultLineage != l {
		return ErrCorrupt
	}
	r, q := a.PlatformReference, a.Qualification
	qp := runtimeplatform.QualificationInput{QualificationID: q.QualificationID, PlatformLineageID: q.PlatformLineageID, DescriptorID: q.DescriptorID, CompatibilityPolicyVersion: q.CompatibilityPolicyVersion, CompatibilityEvidenceHash: q.CompatibilityEvidenceHash, Generation: q.Generation}
	if r.Validate() != nil || a.Descriptor.Validate() != nil || a.Evidence.ValidateDescriptor(a.Descriptor) != nil || a.Descriptor.RuntimeRole != "IMPLEMENTATION" || r.DescriptorID != a.Descriptor.DescriptorID || r.DescriptorHash != a.Descriptor.ComputeHash() || r.PlatformLineageID != a.Descriptor.PlatformLineageID || r.QualificationID != q.QualificationID || r.QualificationGeneration != q.Generation || r.CompatibilityEvidenceHash != a.Evidence.EvidenceHash || q.CompatibilityEvidenceHash != r.CompatibilityEvidenceHash || q.DescriptorID != r.DescriptorID || q.PlatformLineageID != r.PlatformLineageID || q.State != runtimeplatform.StateCurrent || q.Outcome != runtimeplatform.OutcomeQualified || q.IssuancePayloadHash != r.QualificationPayloadHash || q.IssuancePayloadHash != qp.ComputePayloadHash() || q.CompatibilityPolicyVersion != r.CompatibilityPolicyVersion || a.Evidence.CompatibilityPolicyVersion != r.CompatibilityPolicyVersion {
		return ErrCorrupt
	}
	adapter, _ := json.Marshal(map[string]string{"adapterId": a.Descriptor.AdapterID, "adapterImplementationDigest": a.Descriptor.AdapterImplementationDigest})
	protocol, _ := json.Marshal(map[string]string{"protocolId": a.Descriptor.ProtocolID, "protocolSchemaDigest": a.Descriptor.ProtocolSchemaDigest})
	if a.Runtime.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 {
		adapter = []byte(agentpolicy.CooperativeAdapterIdentity)
		protocol = []byte(agentpolicy.CooperativeProtocolIdentity)
	}
	plan := a.Plan
	if plan.Validate() != nil || plan.OwnerID != a.ExecutionID || (a.PriorRuntimeApproval == nil && plan.OwnerKind != runtimepreparation.OwnerExecution) || plan.ProjectID != a.ProjectID || plan.TaskID != a.TaskID || plan.FeatureAreaID != a.FeatureAreaID || plan.TaskDefinitionRevisionID != a.RevisionID || plan.PreparationRequestID != a.AdmissionRequestID || plan.PreparationPayloadHash != a.AdmissionPayloadHash || a.PreparationAuthorityID != plan.ID || plan.BackendIdentity != a.Backend.ConfigID || plan.BackendConfigFingerprint != a.Backend.ConfigFingerprint || plan.BackendSource != a.Backend.Source || plan.BackendKind != "CODEX" || plan.RuntimeAdapterIdentity != string(adapter) || plan.RuntimeProtocolIdentity != string(protocol) || (a.PriorRuntimeApproval == nil && (plan.SourceCommand != "EXECUTION_ADMISSION" || plan.SourceReceiptID != a.AdmissionRequestID)) || plan.Model != a.Runtime.Model || plan.ReasoningEffort != a.Runtime.ReasoningEffort || plan.CodexFastMode != a.Runtime.CodexFastMode || plan.ServiceTier != a.Runtime.ServiceTier || plan.AgentID != a.Runtime.AgentID || plan.AgentConfigFingerprint != a.Runtime.AgentConfigFingerprint || plan.ExecutionPolicy != a.Runtime.ExecutionPolicy {
		return ErrCorrupt
	}
	if !reflect.DeepEqual(plan.RepositoryTargets, d.RepositoryTargets) || plan.PrimaryMemberID != d.PrimaryMemberID || len(plan.DevelopmentRefs) != len(d.RepositoryTargets) || len(plan.ExpectedSourceHEADs) != len(d.RepositoryTargets) || len(plan.WriteScopes) != len(d.RepositoryTargets) {
		return ErrCorrupt
	}
	for _, target := range d.RepositoryTargets {
		ref, hasRef := plan.DevelopmentRefs[target.MemberID]
		head, hasHead := plan.ExpectedSourceHEADs[target.MemberID]
		scope, hasScope := plan.WriteScopes[target.MemberID]
		if !hasRef || !hasHead || !hasScope || ref != target.OriginRef || head != target.OriginCommit || !reflect.DeepEqual(scope, target.Write) {
			return ErrCorrupt
		}
	}
	return nil
}

func NewAdoptionReceipt(a AdoptionArchive) (AdoptionReceipt, error) {
	if err := a.Validate(); err != nil {
		return AdoptionReceipt{}, err
	}
	return AdoptionReceipt{Archive: a, BusinessHash: Hash(a), ReceiptIdentity: runtimeselection.ReceiptIdentity(a.Identity.AISessionID, a.Identity.RequestID), Role: CurrentReceiptRole, ProducerKind: "FORMAL_CONFIRMATION_COMMIT", TransitionID: "confirmation.commit", BusinessOutcome: "COMMITTED", ConsumptionDisposition: "CONSUMED"}, nil
}
func (r AdoptionReceipt) Validate() error {
	want, err := NewAdoptionReceipt(r.Archive)
	if err != nil || !reflect.DeepEqual(want, r) {
		return ErrCorrupt
	}
	return nil
}
func (id ReadIdentity) MatchesAdoption(r AdoptionReceipt) error {
	if r.Validate() != nil {
		return ErrCorrupt
	}
	if id.Validate() != nil || id != r.Archive.Identity {
		return ErrConflict
	}
	return nil
}

// ErrQualificationRequired is returned only for a fresh definition-only
// admission. Complete replay and locked admission never replace their platform.
var ErrQualificationRequired = errors.New("FORMAL_ADOPTION_QUALIFICATION_REQUIRED")

func (r AdoptionReceipt) SourceID() string {
	return runtimeselection.SourceIdentity(r.Archive.Identity.AISessionID, r.Archive.Identity.RequestID, r.Archive.ExecutionID)
}
