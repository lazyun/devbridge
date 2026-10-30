// Package formalexecution describes the three durable approval transactions.
// Its immutable values are evidence, never caller supplied authorization.
package formalexecution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/lazyun/devbridge/internal/agentpolicy"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/formalapproval"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/task"
)

var (
	ErrMissing       = errors.New("FORMAL_EXECUTION_MISSING")
	ErrConflict      = errors.New("FORMAL_EXECUTION_IDENTITY_CONFLICT")
	ErrCorrupt       = errors.New("FORMAL_EXECUTION_CORRUPT")
	ErrParentMissing = errors.New("FORMAL_EXECUTION_PARENT_REVISION_MISSING")
)

const (
	T1Transaction      = "APPROVAL_REVISION_COMMIT"
	T2Transaction      = "EXECUTION_ADMISSION_SCOPE_COMMIT"
	T3Transaction      = "PREPARATION_READY_COMMIT"
	CurrentReceiptRole = "CURRENT_FORMAL_COMMIT_RECEIPT"
	ProducerKind       = "FORMAL_APPROVAL_COMMIT"
	TransitionID       = "decision_revision.commit_approve_and_start"
)

type PlatformReference = runtimeselection.PlatformReference

type ReadIdentity struct {
	AISessionID      string `json:"aiSessionId"`
	IntentID         string `json:"intentId"`
	RequestID        string `json:"requestId"`
	ContentHash      string `json:"contentHash"`
	TypedPayloadHash string `json:"typedPayloadHash"`
	SubmittedHash    string `json:"submittedHash"`
	IntentVersion    int    `json:"intentVersion"`
}

// PrepareFence captures the opaque Decision version before user commitment.
// The platform reference is supplied only by the Core artifact resolver.
type PrepareFence struct {
	AISessionID          string                          `json:"aiSessionId"`
	IntentID             string                          `json:"intentId"`
	ProjectID            string                          `json:"projectId"`
	TaskID               string                          `json:"taskId"`
	ReviewID             string                          `json:"reviewId"`
	DecisionID           string                          `json:"decisionId"`
	DecisionVersion      string                          `json:"decisionVersion"`
	ParentRevisionID     string                          `json:"parentRevisionId"`
	ParentRevisionHash   string                          `json:"parentRevisionHash"`
	ContentHash          string                          `json:"contentHash"`
	IntentVersion        int                             `json:"intentVersion"`
	FrozenPrepareJSON    json.RawMessage                 `json:"frozenPrepareJson"`
	ParentDefinitionJSON json.RawMessage                 `json:"parentDefinitionJson"`
	Schema               formalapproval.SchemaReference  `json:"schema"`
	ParentSchema         *formalapproval.SchemaReference `json:"parentSchema,omitempty"`
}

// All 32 fields belong to the original Core-authenticated T1 identity.
type PartialOperationIdentity struct {
	SessionID                 string `json:"sessionId"`
	RequestID                 string `json:"requestId"`
	ProcedureKind             string `json:"procedureKind"`
	Purpose                   string `json:"purpose"`
	Action                    string `json:"action"`
	NormalizedPayloadIdentity string `json:"normalizedPayloadIdentity"`
	SubmittedPayloadIdentity  string `json:"submittedPayloadIdentity"`
	ProjectID                 string `json:"projectId"`
	TaskID                    string `json:"taskId"`
	RuntimeRole               string `json:"runtimeRole"`
	ReviewID                  string `json:"reviewId"`
	DecisionID                string `json:"decisionId"`
	DecisionVersion           string `json:"decisionVersion"`
	IntentID                  string `json:"intentId"`
	IntentVersion             int    `json:"intentVersion"`
	VariantID                 string `json:"variantId"`
	VariantPayloadIdentity    string `json:"variantPayloadIdentity"`
	ParentRevisionID          string `json:"parentRevisionId"`
	OutputRevisionID          string `json:"outputRevisionId"`
	OutputRevisionHash        string `json:"outputRevisionHash"`
	QualificationID           string `json:"qualificationId"`
	QualificationGeneration   int    `json:"qualificationGeneration"`
	PlatformLineageID         string `json:"platformLineageId"`
	DescriptorIdentity        string `json:"descriptorIdentity"`
	BackendIdentity           string `json:"backendIdentity"`
	RuntimeIdentity           string `json:"runtimeIdentity"`
	OperationGeneration       string `json:"operationGeneration"`
	BusinessResultID          string `json:"businessResultId"`
	SourceOriginID            string `json:"sourceOriginId"`
	ImmutableBinding          string `json:"immutableBinding"`
	FrozenLineageHash         string `json:"frozenLineageHash"`
	TransactionStage          string `json:"transactionStage"`
}
type T1CommittedBusinessResult struct {
	Identity                   PartialOperationIdentity `json:"identity"`
	Producer                   string                   `json:"producer"`
	Transaction                string                   `json:"transaction"`
	DecisionConsumptionBinding string                   `json:"decisionConsumptionBinding"`
	OutputRevisionBinding      string                   `json:"outputRevisionBinding"`
	SourceOriginBinding        string                   `json:"sourceOriginBinding"`
	CommitState                string                   `json:"commitState"`
}
type T2DurableReadback struct {
	T1ResultID               string `json:"t1ResultId"`
	OperationGeneration      string `json:"operationGeneration"`
	OperationIdentityBinding string `json:"operationIdentityBinding"`
	ExecutionID              string `json:"executionId"`
	ExecutionBinding         string `json:"executionBinding"`
	ScopePlanID              string `json:"scopePlanId"`
	ScopePlanBinding         string `json:"scopePlanBinding"`
	Producer                 string `json:"producer"`
	Transaction              string `json:"transaction"`
	CommitState              string `json:"commitState"`
}
type Archive struct {
	Fence                 PrepareFence                          `json:"fence"`
	Identity              ReadIdentity                          `json:"identity"`
	Commit                confirmation.CommitInput              `json:"commit"`
	OriginalRequestJSON   json.RawMessage                       `json:"originalRequestJson"`
	ApprovalStepRequestID string                                `json:"approvalStepRequestId"`
	RevisionID            string                                `json:"revisionId"`
	RevisionHash          string                                `json:"revisionHash"`
	DefinitionJSON        json.RawMessage                       `json:"definitionJson"`
	FrozenLineage         formalapproval.Lineage                `json:"frozenLineage"`
	ResultLineage         formalapproval.Lineage                `json:"resultLineage"`
	Core                  formalapproval.CoreSnapshot           `json:"core"`
	PlatformReference     PlatformReference                     `json:"platformReference"`
	Descriptor            runtimeplatform.Descriptor            `json:"descriptor"`
	Evidence              runtimeplatform.CompatibilityEvidence `json:"evidence"`
	Qualification         runtimeplatform.Qualification         `json:"qualification"`
	Runtime               codexruntime.NextRunRuntimeConfig     `json:"runtime"`
	Backend               codexconfig.BackendSelection          `json:"backend"`
}
type Progress struct {
	T1              T1CommittedBusinessResult `json:"t1"`
	T2              *T2DurableReadback        `json:"t2,omitempty"`
	Archive         Archive                   `json:"archive"`
	Plan            *runtimepreparation.Plan  `json:"plan,omitempty"`
	NextTransaction string                    `json:"nextTransaction"`
}
type Receipt struct {
	T1                     T1CommittedBusinessResult `json:"t1"`
	T2                     T2DurableReadback         `json:"t2"`
	Archive                Archive                   `json:"archive"`
	Plan                   runtimepreparation.Plan   `json:"plan"`
	PreparationAuthorityID string                    `json:"preparationAuthorityId"`
	ReceiptIdentity        string                    `json:"receiptIdentity"`
	Role                   string                    `json:"role"`
	ProducerKind           string                    `json:"producerKind"`
	TransitionID           string                    `json:"transitionId"`
	BusinessOutcome        string                    `json:"businessOutcome"`
	ConsumptionDisposition string                    `json:"consumptionDisposition"`
}
type Store interface {
	ReadApproveAndStart(context.Context, ReadIdentity) (Receipt, error)
	ReadApproveAndStartRequired(context.Context, ReadIdentity) (bool, error)
	ReadApproveAndStartProgress(context.Context, ReadIdentity) (Progress, error)
	CommitApproveAndStart(context.Context, ReadIdentity, PlatformReference) (Receipt, error)
}

func HashBytes(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func Hash(v any) string {
	b, e := json.Marshal(v)
	if e != nil {
		return ""
	}
	return HashBytes(b)
}

// CanonicalHash is CANONICAL_JSON_OBJECT_UTF8_SORTED_KEYS_V1 without float loss.
func CanonicalHash(v any) string {
	raw, e := json.Marshal(v)
	if e != nil {
		return ""
	}
	var o any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&o) != nil {
		return ""
	}
	return Hash(o)
}
func HashCommit(v confirmation.CommitInput) string { return Hash(v) }
func exact(s string) bool                          { return s != "" && utf8.ValidString(s) && s == strings.TrimSpace(s) }
func (id ReadIdentity) Validate() error {
	if !exact(id.AISessionID) || !exact(id.IntentID) || !exact(id.RequestID) || id.IntentVersion < 1 || !runtimeplatform.ValidDigest(id.ContentHash) || !runtimeplatform.ValidDigest(id.TypedPayloadHash) || !runtimeplatform.ValidDigest(id.SubmittedHash) {
		return ErrConflict
	}
	return nil
}
func Decode(raw []byte, hash string, out any) error {
	if HashBytes(raw) != hash || !utf8.Valid(raw) || json.Unmarshal(raw, out) != nil {
		return ErrCorrupt
	}
	b, e := json.Marshal(out)
	if e != nil || !bytes.Equal(b, raw) {
		return ErrCorrupt
	}
	return nil
}
func (f PrepareFence) Validate() error {
	if !exact(f.AISessionID) || !exact(f.IntentID) || !exact(f.ProjectID) || !exact(f.TaskID) || !exact(f.ReviewID) || !exact(f.DecisionID) || !exact(f.ParentRevisionID) || !runtimeplatform.ValidDigest(f.DecisionVersion) || f.IntentVersion < 1 || f.ContentHash != HashBytes(f.FrozenPrepareJSON) {
		return ErrCorrupt
	}
	d, e := formalapproval.DecodeParentDefinition(f.ParentDefinitionJSON, f.ParentRevisionHash, f.ParentSchema, f.Schema)
	if e != nil || d.ProjectID != f.ProjectID {
		return ErrCorrupt
	}
	var p confirmation.PrepareInput
	if Decode(f.FrozenPrepareJSON, f.ContentHash, &p) != nil {
		return ErrCorrupt
	}
	b := p.Execution
	if p.AISessionID != f.AISessionID || p.Purpose != confirmation.Execution || p.Action != "APPROVE_AND_START" || b == nil || b.Action != p.Action || b.TaskID != f.TaskID || b.ReviewID != f.ReviewID || b.DecisionID != f.DecisionID || b.TaskDefinitionRevisionID != f.ParentRevisionID || b.FeatureAreaID != d.FeatureAreaID {
		return ErrCorrupt
	}
	return nil
}
func (a Archive) Validate() error {
	f, c, id := a.Fence, a.Commit, a.Identity
	if f.Validate() != nil || id.Validate() != nil || id.AISessionID != f.AISessionID || id.IntentID != f.IntentID || id.IntentVersion != f.IntentVersion || id.ContentHash != f.ContentHash || id.TypedPayloadHash != HashCommit(c) || id.RequestID != c.RequestID || c.AISessionID != f.AISessionID || c.IntentID != f.IntentID || c.Version != f.IntentVersion || c.ContentHash != f.ContentHash || c.Purpose != confirmation.Execution || c.Action != "APPROVE_AND_START" || !exact(c.VariantID) || c.Selection.RuntimeConfig == nil || c.Selection.Backend == nil || c.BackendSelection == nil || *c.Selection.RuntimeConfig != a.Runtime || *c.BackendSelection != a.Backend || c.Selection.Backend.ConfigID != a.Backend.ConfigID || c.Selection.Backend.ConfigFingerprint != a.Backend.ConfigFingerprint {
		return ErrCorrupt
	}
	var original confirmation.CommitInput
	if Decode(a.OriginalRequestJSON, HashBytes(a.OriginalRequestJSON), &original) != nil || original.AISessionID != c.AISessionID || original.IntentID != c.IntentID || original.RequestID != c.RequestID || original.Version != c.Version || original.ContentHash != c.ContentHash || original.Purpose != c.Purpose || original.Action != c.Action || original.VariantID != c.VariantID {
		return ErrCorrupt
	}
	if a.ApprovalStepRequestID != "confirmation_"+Hash([]string{f.IntentID, "APPROVE"})[:32] || !exact(a.RevisionID) || a.RevisionID == f.ParentRevisionID {
		return ErrCorrupt
	}
	d, e := formalapproval.DecodeDefinition(a.DefinitionJSON, a.RevisionHash, f.Schema)
	if e != nil || d.ProjectID != f.ProjectID || d.ReviewApprovalID != f.DecisionID || d.WorkSpec.ImplementationMode != task.ImplementationCodingAgent {
		return ErrCorrupt
	}
	var prepared confirmation.PrepareInput
	if Decode(f.FrozenPrepareJSON, f.ContentHash, &prepared) != nil {
		return ErrCorrupt
	}
	n := 0
	for _, v := range prepared.Variants {
		if v.ID == c.VariantID {
			n++
			if v.ImplementationMode != task.ImplementationCodingAgent || v.WorkSpec == nil || v.VerificationSpec == nil || v.PrimaryMemberID != d.PrimaryMemberID || !reflect.DeepEqual(v.RepositoryTargets, d.RepositoryTargets) || !reflect.DeepEqual(*v.WorkSpec, d.WorkSpec) || !reflect.DeepEqual(*v.VerificationSpec, d.VerificationSpec) {
				return ErrCorrupt
			}
		}
	}
	if n != 1 {
		return ErrCorrupt
	}
	l := formalapproval.Lineage{ProjectID: f.ProjectID, TaskID: f.TaskID, ReviewID: f.ReviewID, DecisionID: f.DecisionID, DecisionVersion: f.DecisionVersion, IntentID: f.IntentID, IntentVersion: f.IntentVersion, VariantID: c.VariantID, CommittedResultID: f.DecisionID, TaskDefinitionRevisionID: a.RevisionID}
	if a.FrozenLineage != l || a.ResultLineage != l {
		return ErrCorrupt
	}
	r, q := a.PlatformReference, a.Qualification
	qp := runtimeplatform.QualificationInput{QualificationID: q.QualificationID, PlatformLineageID: q.PlatformLineageID, DescriptorID: q.DescriptorID, CompatibilityPolicyVersion: q.CompatibilityPolicyVersion, CompatibilityEvidenceHash: q.CompatibilityEvidenceHash, Generation: q.Generation}
	if r.Validate() != nil || qp.ComputePayloadHash() != q.IssuancePayloadHash || a.Descriptor.Validate() != nil || a.Evidence.ValidateDescriptor(a.Descriptor) != nil || r.DescriptorID != a.Descriptor.DescriptorID || r.DescriptorHash != a.Descriptor.ComputeHash() || r.PlatformLineageID != a.Descriptor.PlatformLineageID || a.Descriptor.RuntimeRole != "IMPLEMENTATION" || r.QualificationID != q.QualificationID || r.QualificationGeneration != q.Generation || r.CompatibilityEvidenceHash != a.Evidence.EvidenceHash || q.CompatibilityEvidenceHash != r.CompatibilityEvidenceHash || q.DescriptorID != r.DescriptorID || q.PlatformLineageID != r.PlatformLineageID || q.State != runtimeplatform.StateCurrent || q.Outcome != runtimeplatform.OutcomeQualified || q.IssuancePayloadHash != r.QualificationPayloadHash || q.CompatibilityPolicyVersion != r.CompatibilityPolicyVersion || a.Evidence.CompatibilityPolicyVersion != r.CompatibilityPolicyVersion || r.LineageVersion < 1 {
		return ErrCorrupt
	}
	core := a.Core
	if core.ProjectID != f.ProjectID || core.TaskID != f.TaskID || core.TaskStatus != "ACTIVE" || core.TaskPhase != "REVIEW_APPROVAL_PENDING" || core.CurrentRevisionID != f.ParentRevisionID || core.CurrentReviewID != f.ReviewID || core.ReviewID != f.ReviewID || core.ReviewProjectID != f.ProjectID || core.ReviewTaskID != f.TaskID || core.ReviewRevisionID != f.ParentRevisionID || core.ReviewOutcome != "VALID" || core.DecisionID != f.DecisionID || core.DecisionState != "PENDING" || core.CurrentConfirmationIntentID != f.IntentID || core.CurrentConfirmationSessionID != f.AISessionID || core.CurrentConfirmationVersionHash != Hash([]any{f.IntentVersion, f.ContentHash}) {
		return ErrCorrupt
	}
	return nil
}
func NewT1(a Archive, generation, source string) (T1CommittedBusinessResult, error) {
	if a.Validate() != nil || !exact(generation) || source != runtimeselection.SourceIdentity(a.Fence.AISessionID, a.Commit.RequestID, a.Fence.DecisionID) {
		return T1CommittedBusinessResult{}, ErrCorrupt
	}
	var p confirmation.PrepareInput
	_ = json.Unmarshal(a.Fence.FrozenPrepareJSON, &p)
	var v confirmation.Variant
	for _, x := range p.Variants {
		if x.ID == a.Commit.VariantID {
			v = x
		}
	}
	f := a.Fence
	i := PartialOperationIdentity{SessionID: f.AISessionID, RequestID: a.Commit.RequestID, ProcedureKind: "COMPOSITE_DURABLE_PROCEDURE", Purpose: "EXECUTION", Action: "APPROVE_AND_START", NormalizedPayloadIdentity: a.Identity.TypedPayloadHash, SubmittedPayloadIdentity: a.Identity.SubmittedHash, ProjectID: f.ProjectID, TaskID: f.TaskID, RuntimeRole: "IMPLEMENTATION", ReviewID: f.ReviewID, DecisionID: f.DecisionID, DecisionVersion: f.DecisionVersion, IntentID: f.IntentID, IntentVersion: f.IntentVersion, VariantID: a.Commit.VariantID, VariantPayloadIdentity: CanonicalHash(v), ParentRevisionID: f.ParentRevisionID, OutputRevisionID: a.RevisionID, OutputRevisionHash: a.RevisionHash, QualificationID: a.PlatformReference.QualificationID, QualificationGeneration: a.PlatformReference.QualificationGeneration, PlatformLineageID: a.PlatformReference.PlatformLineageID, DescriptorIdentity: a.PlatformReference.DescriptorHash, BackendIdentity: CanonicalHash(a.Backend), RuntimeIdentity: CanonicalHash(a.Runtime), OperationGeneration: generation, BusinessResultID: f.DecisionID, SourceOriginID: source, FrozenLineageHash: CanonicalHash(a.FrozenLineage), TransactionStage: T1Transaction}
	i.ImmutableBinding = CanonicalHash(i)
	return T1CommittedBusinessResult{Identity: i, Producer: "CORE", Transaction: T1Transaction, DecisionConsumptionBinding: CanonicalHash([]string{f.DecisionID, a.ApprovalStepRequestID, f.AISessionID}), OutputRevisionBinding: CanonicalHash([]string{a.RevisionID, a.RevisionHash}), SourceOriginBinding: CanonicalHash([]string{source, i.ImmutableBinding}), CommitState: "T1_COMMITTED"}, nil
}
func (p Progress) Validate() error {
	t, e := NewT1(p.Archive, p.T1.Identity.OperationGeneration, p.T1.Identity.SourceOriginID)
	if e != nil || t != p.T1 {
		return ErrCorrupt
	}
	if p.T2 == nil {
		if p.Plan != nil || p.NextTransaction != T2Transaction {
			return ErrCorrupt
		}
		return nil
	}
	if p.Plan == nil || p.NextTransaction != T3Transaction {
		return ErrCorrupt
	}
	if err := ValidateT2(p.T1, *p.T2, *p.Plan); err != nil {
		return err
	}
	a, plan := p.Archive, *p.Plan
	adapter, _ := json.Marshal(map[string]string{"adapterId": a.Descriptor.AdapterID, "adapterImplementationDigest": a.Descriptor.AdapterImplementationDigest})
	protocol, _ := json.Marshal(map[string]string{"protocolId": a.Descriptor.ProtocolID, "protocolSchemaDigest": a.Descriptor.ProtocolSchemaDigest})
	if a.Runtime.ExecutionPolicy == agentpolicy.PolicyCooperativeTrustedV1 {
		adapter = []byte(agentpolicy.CooperativeAdapterIdentity)
		protocol = []byte(agentpolicy.CooperativeProtocolIdentity)
	}
	d, err := formalapproval.DecodeDefinition(a.DefinitionJSON, a.RevisionHash, a.Fence.Schema)
	if err != nil {
		return ErrCorrupt
	}
	if plan.SourceCommand != "APPROVE_AND_START" || plan.SourceReceiptID != p.T1.Identity.SourceOriginID || plan.SelectedVariantID != a.Commit.VariantID || plan.ProposalHash != p.T1.Identity.VariantPayloadIdentity || plan.PreparationRequestID != "formal_execution_admission_"+p.T1.Identity.ImmutableBinding || plan.PreparationPayloadHash != p.T1.Identity.ImmutableBinding || plan.FeatureAreaID != d.FeatureAreaID || plan.SourceReviewID != a.Fence.ReviewID || plan.SourceExecutionID != "" || plan.SuccessorExecutionID != "" || plan.SuccessorReviewID != "" || plan.Generation != 1 || plan.BackendIdentity != a.Backend.ConfigID || plan.BackendConfigFingerprint != a.Backend.ConfigFingerprint || plan.BackendSource != a.Backend.Source || plan.BackendKind != "CODEX" || plan.Model != a.Runtime.Model || plan.ReasoningEffort != a.Runtime.ReasoningEffort || plan.CodexFastMode != a.Runtime.CodexFastMode || plan.ServiceTier != a.Runtime.ServiceTier || plan.AgentID != a.Runtime.AgentID || plan.AgentConfigFingerprint != a.Runtime.AgentConfigFingerprint || plan.ExecutionPolicy != a.Runtime.ExecutionPolicy || plan.RuntimeAdapterIdentity != string(adapter) || plan.RuntimeProtocolIdentity != string(protocol) || plan.PrimaryMemberID != d.PrimaryMemberID || !reflect.DeepEqual(plan.RepositoryTargets, d.RepositoryTargets) {
		return ErrCorrupt
	}
	if len(plan.ExpectedSourceHEADs) != len(d.RepositoryTargets) || len(plan.DevelopmentRefs) != len(d.RepositoryTargets) || len(plan.WriteScopes) != len(d.RepositoryTargets) {
		return ErrCorrupt
	}
	for _, target := range d.RepositoryTargets {
		if plan.ExpectedSourceHEADs[target.MemberID] != target.OriginCommit || plan.DevelopmentRefs[target.MemberID] != target.OriginRef || !reflect.DeepEqual(plan.WriteScopes[target.MemberID], target.Write) {
			return ErrCorrupt
		}
	}
	return nil
}
func ValidateT2(t T1CommittedBusinessResult, r T2DurableReadback, p runtimepreparation.Plan) error {
	if p.Validate() != nil || r.T1ResultID != t.Identity.BusinessResultID || r.OperationGeneration != t.Identity.OperationGeneration || r.OperationIdentityBinding != t.Identity.ImmutableBinding || !exact(r.ExecutionID) || r.ExecutionBinding != CanonicalHash([]string{r.ExecutionID, t.Identity.OutputRevisionID, t.Identity.ImmutableBinding}) || r.ScopePlanID != p.ID || r.ScopePlanBinding != p.ContentHash || r.Producer != "CORE" || r.Transaction != T2Transaction || r.CommitState != "T2_COMMITTED" || p.OwnerID != r.ExecutionID || p.OwnerKind != runtimepreparation.OwnerImplementation || p.TaskID != t.Identity.TaskID || p.ProjectID != t.Identity.ProjectID || p.TaskDefinitionRevisionID != t.Identity.OutputRevisionID {
		return ErrCorrupt
	}
	return nil
}
func NewReceipt(p Progress) (Receipt, error) {
	if p.Validate() != nil || p.T2 == nil || p.Plan == nil {
		return Receipt{}, ErrCorrupt
	}
	return Receipt{T1: p.T1, T2: *p.T2, Archive: p.Archive, Plan: *p.Plan, PreparationAuthorityID: p.Plan.ID, ReceiptIdentity: Hash([]string{p.Archive.Identity.AISessionID, p.Archive.Identity.RequestID}), Role: CurrentReceiptRole, ProducerKind: ProducerKind, TransitionID: TransitionID, BusinessOutcome: "COMMITTED", ConsumptionDisposition: "CONSUMED"}, nil
}
func (r Receipt) Validate() error {
	p := Progress{T1: r.T1, T2: &r.T2, Archive: r.Archive, Plan: &r.Plan, NextTransaction: T3Transaction}
	want, e := NewReceipt(p)
	if e != nil || !reflect.DeepEqual(want, r) {
		return ErrCorrupt
	}
	return nil
}
func (id ReadIdentity) MatchesReceipt(r Receipt) error {
	if r.Validate() != nil {
		return ErrCorrupt
	}
	if id != r.Archive.Identity {
		return ErrConflict
	}
	return id.Validate()
}
