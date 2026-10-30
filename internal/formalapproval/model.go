// Package formalapproval describes the immutable evidence of a definition-only
// approval. These values never grant permission to create or dispatch a Run.
// Only a durable producer may issue a receipt; hashes validate identity, not
// provenance. The production store owns that boundary.
package formalapproval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/task"
)

var (
	ErrNotApplicable = errors.New("FORMAL_APPROVAL_NOT_APPLICABLE")
	ErrMissing       = errors.New("FORMAL_APPROVAL_MISSING")
	ErrConflict      = errors.New("FORMAL_APPROVAL_IDENTITY_CONFLICT")
	ErrCorrupt       = errors.New("FORMAL_APPROVAL_CORRUPT")
)

const (
	CurrentReceiptRole = "CURRENT_FORMAL_COMMIT_RECEIPT"
	PriorReceiptRole   = "PRIOR_APPROVAL_RECEIPT"
	ProducerKind       = "DEFINITION_ONLY_COMMIT"
	TransitionID       = "confirmation.commit"
)

type SchemaReference struct {
	SchemaID          string `json:"schemaId"`
	DefinitionVersion int    `json:"definitionVersion"`
	FactSchemaID      string `json:"factSchemaId"`
}

type SchemaEntry struct {
	SchemaReference
	State             string `json:"state"`
	ApplicablePurpose string `json:"applicablePurpose"`
	ApplicableAction  string `json:"applicableAction"`
}

// LookupSchema is a finite Core catalog, coupled to the decoder below. It does
// not register a runtime capability or accept client-provided schema versions.
func LookupSchema(ref SchemaReference, purpose, action string) (SchemaEntry, error) {
	entry := SchemaEntry{SchemaReference: SchemaReference{"TASK_DEFINITION", 2, "FORMAL_TASK_DEFINITION"}, State: "ACTIVE", ApplicablePurpose: "EXECUTION", ApplicableAction: "APPROVE"}
	if entry.SchemaReference != ref || purpose != entry.ApplicablePurpose || action != entry.ApplicableAction {
		return SchemaEntry{}, ErrConflict
	}
	return entry, nil
}

func DecodeDefinition(raw json.RawMessage, hash string, ref SchemaReference) (task.Definition, error) {
	if ref != (SchemaReference{"TASK_DEFINITION", 2, "FORMAL_TASK_DEFINITION"}) {
		return task.Definition{}, ErrCorrupt
	}
	var exact task.Definition
	if err := exactJSON(raw, &exact); err != nil {
		return exact, err
	}
	value, err := task.DecodeDefinition(string(raw), hash)
	if err != nil || value.Version != ref.DefinitionVersion {
		return task.Definition{}, ErrCorrupt
	}
	return value, nil
}

// DraftParentSchema identifies an authored requirement snapshot only. It never
// satisfies the approved output definition catalog or authorizes an Execution.
func DraftParentSchema() SchemaReference {
	return SchemaReference{"TASK_DEFINITION_DRAFT", 1, "FORMAL_TASK_DEFINITION_DRAFT"}
}

func DecodeParentDefinition(raw json.RawMessage, hash string, parent *SchemaReference, output SchemaReference) (task.Definition, error) {
	if parent == nil {
		return DecodeDefinition(raw, hash, output)
	}
	if *parent != DraftParentSchema() || output != (SchemaReference{"TASK_DEFINITION", 2, "FORMAL_TASK_DEFINITION"}) {
		return task.Definition{}, ErrCorrupt
	}
	var d task.Definition
	if exactJSON(raw, &d) != nil {
		return task.Definition{}, ErrCorrupt
	}
	decoded, err := task.DecodeDefinition(string(raw), hash)
	if err != nil || decoded.Version != 1 || d.ReviewApprovalID != "" || d.ContinuationNormalizationVersion != 0 || len(d.RepositoryTargets) != 0 || d.PrimaryMemberID != "" || d.WorkSpec.ImplementationMode != "" || d.WorkSpec.CodexInstruction != "" || d.WorkSpec.ExecutionScope.Kind != "" || len(d.WorkSpec.ExecutionScope.Paths) != 0 || !reflect.DeepEqual(d.VerificationSpec, task.VerificationSpec{}) {
		return task.Definition{}, ErrCorrupt
	}
	return decoded, nil
}

// PrepareFence is written with Prepare, before the user submits a selection.
// Parent and prepare bytes are archived, not reconstructed from current state.
type PrepareFence struct {
	AISessionID          string           `json:"aiSessionId"`
	IntentID             string           `json:"intentId"`
	ProjectID            string           `json:"projectId"`
	TaskID               string           `json:"taskId"`
	ReviewID             string           `json:"reviewId"`
	DecisionID           string           `json:"decisionId"`
	DecisionVersion      string           `json:"decisionVersion"`
	ParentRevisionID     string           `json:"parentRevisionId"`
	ParentRevisionHash   string           `json:"parentRevisionHash"`
	ContentHash          string           `json:"contentHash"`
	IntentVersion        int              `json:"intentVersion"`
	FrozenPrepareJSON    json.RawMessage  `json:"frozenPrepareJson"`
	ParentDefinitionJSON json.RawMessage  `json:"parentDefinitionJson"`
	Schema               SchemaReference  `json:"schema"`
	ParentSchema         *SchemaReference `json:"parentSchema,omitempty"`
}

type Lineage struct {
	ProjectID                string `json:"projectId"`
	TaskID                   string `json:"taskId"`
	ReviewID                 string `json:"reviewId"`
	DecisionID               string `json:"decisionId"`
	DecisionVersion          string `json:"decisionVersion"`
	IntentID                 string `json:"intentId"`
	IntentVersion            int    `json:"intentVersion"`
	VariantID                string `json:"variantId"`
	CommittedResultID        string `json:"committedResultId"`
	TaskDefinitionRevisionID string `json:"taskDefinitionRevisionId"`
}

// CoreSnapshot contains the independently read pre-approval facts. Historical
// readback compares this snapshot, never a newer live eligibility projection.
type CoreSnapshot struct {
	ProjectID                      string `json:"projectId"`
	TaskID                         string `json:"taskId"`
	TaskStatus                     string `json:"taskStatus"`
	TaskPhase                      string `json:"taskPhase"`
	CurrentRevisionID              string `json:"currentRevisionId"`
	CurrentExecutionID             string `json:"currentExecutionId"`
	CurrentReviewID                string `json:"currentReviewId"`
	ReviewID                       string `json:"reviewId"`
	ReviewProjectID                string `json:"reviewProjectId"`
	ReviewTaskID                   string `json:"reviewTaskId"`
	ReviewRevisionID               string `json:"reviewRevisionId"`
	ReviewOutcome                  string `json:"reviewOutcome"`
	DecisionID                     string `json:"decisionId"`
	DecisionState                  string `json:"decisionState"`
	CurrentConfirmationIntentID    string `json:"currentConfirmationIntentId"`
	CurrentConfirmationSessionID   string `json:"currentConfirmationSessionId"`
	CurrentConfirmationVersionHash string `json:"currentConfirmationVersionHash"`
}

// BusinessSnapshot is committed by REVIEW_APPROVAL_TRANSACTION. The separate
// CONFIRMATION_RECEIPT_TRANSACTION seals it; an interrupted first transaction
// can never be inferred from CONSUMED or a completed confirmation step.
type BusinessSnapshot struct {
	Fence                 PrepareFence             `json:"fence"`
	Commit                confirmation.CommitInput `json:"commit"`
	TypedPayloadHash      string                   `json:"typedPayloadHash"`
	SubmittedHash         string                   `json:"submittedHash"`
	OriginalRequestJSON   json.RawMessage          `json:"originalRequestJson"`
	ApprovalStepRequestID string                   `json:"approvalStepRequestId"`
	ResultID              string                   `json:"resultId"`
	RevisionID            string                   `json:"revisionId"`
	RevisionHash          string                   `json:"revisionHash"`
	DefinitionJSON        json.RawMessage          `json:"definitionJson"`
	FrozenLineage         Lineage                  `json:"frozenLineage"`
	ResultLineage         Lineage                  `json:"resultLineage"`
	Core                  CoreSnapshot             `json:"core"`
}

// No runtime/platform/source/binding fields exist on this type. Inherited
// model/backend fields inside DefinitionJSON are historical authored data.
type Receipt struct {
	Snapshot               BusinessSnapshot `json:"snapshot"`
	SnapshotHash           string           `json:"snapshotHash"`
	ReceiptIdentity        string           `json:"receiptIdentity"`
	Role                   string           `json:"role"`
	ProducerKind           string           `json:"producerKind"`
	TransitionID           string           `json:"transitionId"`
	BusinessOutcome        string           `json:"businessOutcome"`
	ConsumptionDisposition string           `json:"consumptionDisposition"`
}

type ReadIdentity struct {
	AISessionID, IntentID, RequestID, ContentHash, TypedPayloadHash, SubmittedHash string
	IntentVersion                                                                  int
}

type Store interface {
	ReadDefinitionApproval(context.Context, ReadIdentity) (Receipt, error)
	FinalizeDefinitionApproval(context.Context, ReadIdentity) (Receipt, error)
}

func HashBytes(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func hashValue(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return HashBytes(raw)
}
func HashCommit(value confirmation.CommitInput) string { return hashValue(value) }
func (f PrepareFence) ComputeHash() string             { return hashValue(f) }
func (s BusinessSnapshot) ComputeHash() string         { return hashValue(s) }
func (r Receipt) ComputeHash() string                  { return hashValue(r) }
func ValidHash(hash string) bool {
	raw, err := hex.DecodeString(hash)
	return err == nil && len(raw) == sha256.Size && hash == strings.ToLower(hash)
}
func exactText(value string) bool {
	return value != "" && utf8.ValidString(value) && value == strings.TrimSpace(value)
}

// Core writers use encoding/json for these typed records. Byte-exact roundtrip
// prevents duplicate keys, unknown fields and case-folded aliases from being
// interpreted differently by SQL and Go. Raw child documents are checked again
// against their own actual typed decoders below.
func exactJSON(raw []byte, out any) error {
	if !utf8.Valid(raw) || json.Unmarshal(raw, out) != nil {
		return ErrCorrupt
	}
	encoded, err := json.Marshal(out)
	if err != nil || !bytes.Equal(raw, encoded) {
		return ErrCorrupt
	}
	return nil
}

func (f PrepareFence) Validate() error {
	for _, value := range []string{f.AISessionID, f.IntentID, f.ProjectID, f.TaskID, f.ReviewID, f.DecisionID, f.ParentRevisionID} {
		if !exactText(value) {
			return ErrCorrupt
		}
	}
	if f.IntentVersion < 1 || !ValidHash(f.DecisionVersion) || !ValidHash(f.ParentRevisionHash) || f.ContentHash != HashBytes(f.FrozenPrepareJSON) {
		return ErrCorrupt
	}
	// Decoder recognition is immutable. Fresh issuance additionally calls the
	// active catalog; replay does not re-run live catalog eligibility.
	parent, err := DecodeParentDefinition(f.ParentDefinitionJSON, f.ParentRevisionHash, f.ParentSchema, f.Schema)
	if err != nil || parent.ProjectID != f.ProjectID {
		return ErrCorrupt
	}
	var prepared confirmation.PrepareInput
	if exactJSON(f.FrozenPrepareJSON, &prepared) != nil {
		return ErrCorrupt
	}
	b := prepared.Execution
	if prepared.AISessionID != f.AISessionID || prepared.Purpose != confirmation.Execution || prepared.Action != "APPROVE" || b == nil || b.Action != "APPROVE" || b.TaskID != f.TaskID || b.ReviewID != f.ReviewID || b.DecisionID != f.DecisionID || b.TaskDefinitionRevisionID != f.ParentRevisionID || !exactText(b.FeatureAreaID) || parent.FeatureAreaID != b.FeatureAreaID {
		return ErrCorrupt
	}
	return nil
}

func (s BusinessSnapshot) Validate() error {
	f, c := s.Fence, s.Commit
	if err := f.Validate(); err != nil {
		return err
	}
	if c.AISessionID != f.AISessionID || c.IntentID != f.IntentID || c.Version != f.IntentVersion || c.ContentHash != f.ContentHash || c.Purpose != confirmation.Execution || c.Action != "APPROVE" || !exactText(c.RequestID) || !exactText(c.VariantID) || !emptySelection(c.Selection) || c.BackendSelection != nil || s.TypedPayloadHash != HashCommit(c) || !ValidHash(s.SubmittedHash) {
		return ErrCorrupt
	}
	// SubmittedHash comes from the original submission ledger. The typed
	// projection below has lost raw field presence, so it must not rehash it.
	var original confirmation.CommitInput
	if exactJSON(s.OriginalRequestJSON, &original) != nil || original.AISessionID != c.AISessionID || original.IntentID != c.IntentID || original.RequestID != c.RequestID || original.Version != c.Version || original.ContentHash != c.ContentHash || original.Purpose != c.Purpose || original.Action != c.Action || original.VariantID != c.VariantID || !emptySelection(original.Selection) || original.BackendSelection != nil {
		return ErrCorrupt
	}
	if s.ResultID != f.DecisionID || !exactText(s.RevisionID) || s.RevisionID == f.ParentRevisionID || s.ApprovalStepRequestID != "confirmation_"+hashValue([]string{f.IntentID, "APPROVE"})[:32] {
		return ErrCorrupt
	}
	definition, err := DecodeDefinition(s.DefinitionJSON, s.RevisionHash, f.Schema)
	if err != nil || definition.ProjectID != f.ProjectID || definition.ReviewApprovalID != f.DecisionID || definition.WorkSpec.ImplementationMode != task.ImplementationCodingAgent {
		return ErrCorrupt
	}
	var prepared confirmation.PrepareInput
	if err := exactJSON(f.FrozenPrepareJSON, &prepared); err != nil {
		return err
	}
	if definition.FeatureAreaID != prepared.Execution.FeatureAreaID || s.Core.CurrentExecutionID != prepared.Execution.ExecutionID {
		return ErrCorrupt
	}
	var chosen confirmation.Variant
	count := 0
	for _, v := range prepared.Variants {
		if v.ID == c.VariantID {
			chosen = v
			count++
		}
	}
	if count != 1 || chosen.ImplementationMode != task.ImplementationCodingAgent || chosen.WorkSpec == nil || chosen.VerificationSpec == nil || chosen.PrimaryMemberID != definition.PrimaryMemberID || !reflect.DeepEqual(chosen.RepositoryTargets, definition.RepositoryTargets) || !reflect.DeepEqual(*chosen.WorkSpec, definition.WorkSpec) || !reflect.DeepEqual(*chosen.VerificationSpec, definition.VerificationSpec) {
		return ErrCorrupt
	}
	want := Lineage{ProjectID: f.ProjectID, TaskID: f.TaskID, ReviewID: f.ReviewID, DecisionID: f.DecisionID, DecisionVersion: f.DecisionVersion, IntentID: f.IntentID, IntentVersion: f.IntentVersion, VariantID: c.VariantID, CommittedResultID: s.ResultID, TaskDefinitionRevisionID: s.RevisionID}
	if s.FrozenLineage != want || s.ResultLineage != want {
		return ErrCorrupt
	}
	core := s.Core
	if core.ProjectID != f.ProjectID || core.TaskID != f.TaskID || core.TaskStatus != "ACTIVE" || core.TaskPhase != string(task.PhaseReviewApprovalPending) || core.CurrentRevisionID != f.ParentRevisionID || core.CurrentReviewID != f.ReviewID || core.ReviewID != f.ReviewID || core.ReviewProjectID != f.ProjectID || core.ReviewTaskID != f.TaskID || core.ReviewRevisionID != f.ParentRevisionID || core.ReviewOutcome != "VALID" || core.DecisionID != f.DecisionID || core.DecisionState != "PENDING" || core.CurrentConfirmationIntentID != f.IntentID || core.CurrentConfirmationSessionID != f.AISessionID || core.CurrentConfirmationVersionHash != hashValue([]any{f.IntentVersion, f.ContentHash}) {
		return ErrCorrupt
	}
	return nil
}

func emptySelection(s confirmation.Selection) bool {
	return s.ProjectID == "" && s.RepositoryID == "" && s.DevelopmentRef == "" && len(s.DevelopmentContexts) == 0 && s.VerificationWaiver == nil && s.RuntimeConfig == nil && s.Backend == nil
}

func NewReceipt(snapshot BusinessSnapshot) (Receipt, error) {
	if err := snapshot.Validate(); err != nil {
		return Receipt{}, err
	}
	return Receipt{Snapshot: snapshot, SnapshotHash: snapshot.ComputeHash(), ReceiptIdentity: hashValue([]string{snapshot.Fence.AISessionID, snapshot.Commit.RequestID}), Role: CurrentReceiptRole, ProducerKind: ProducerKind, TransitionID: TransitionID, BusinessOutcome: "COMMITTED", ConsumptionDisposition: "CONSUMED"}, nil
}
func (r Receipt) Validate() error {
	want, err := NewReceipt(r.Snapshot)
	if err != nil || !reflect.DeepEqual(r, want) {
		return ErrCorrupt
	}
	return nil
}
func (in ReadIdentity) Validate() error {
	if !exactText(in.AISessionID) || !exactText(in.IntentID) || !exactText(in.RequestID) || in.IntentVersion < 1 || !ValidHash(in.ContentHash) || !ValidHash(in.TypedPayloadHash) || !ValidHash(in.SubmittedHash) {
		return ErrConflict
	}
	return nil
}
func (in ReadIdentity) MatchesReceipt(r Receipt) error {
	if err := r.Validate(); err != nil {
		return err
	}
	return in.MatchesSnapshot(r.Snapshot)
}
func (in ReadIdentity) MatchesSnapshot(s BusinessSnapshot) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if err := in.Validate(); err != nil {
		return err
	}
	if in.AISessionID != s.Fence.AISessionID || in.IntentID != s.Fence.IntentID || in.IntentVersion != s.Fence.IntentVersion || in.RequestID != s.Commit.RequestID || in.ContentHash != s.Fence.ContentHash || in.TypedPayloadHash != s.TypedPayloadHash || in.SubmittedHash != s.SubmittedHash {
		return ErrConflict
	}
	return nil
}

func DecodeSnapshot(raw []byte, hash string) (BusinessSnapshot, error) {
	var out BusinessSnapshot
	if HashBytes(raw) != hash || exactJSON(raw, &out) != nil {
		return out, ErrCorrupt
	}
	return out, out.Validate()
}
func DecodeReceipt(raw []byte, hash string) (Receipt, error) {
	var out Receipt
	if HashBytes(raw) != hash || exactJSON(raw, &out) != nil {
		return out, ErrCorrupt
	}
	return out, out.Validate()
}
func DecodeFence(raw []byte, hash string) (PrepareFence, error) {
	var out PrepareFence
	if HashBytes(raw) != hash || exactJSON(raw, &out) != nil {
		return out, ErrCorrupt
	}
	return out, out.Validate()
}

// PriorApproval is a read projection with a distinct role. It does not mutate
// the original current receipt, nor claim that a later start has been approved.
type PriorApproval struct {
	Role    string
	Receipt Receipt
}

func (r Receipt) AsPriorApproval() (PriorApproval, error) {
	if err := r.Validate(); err != nil {
		return PriorApproval{}, fmt.Errorf("prior approval: %w", err)
	}
	// Return a deep value so editing a caller's read projection cannot mutate
	// the original byte slices and nested selection pointers.
	raw, _ := json.Marshal(r)
	var copy Receipt
	if err := json.Unmarshal(raw, &copy); err != nil {
		return PriorApproval{}, err
	}
	return PriorApproval{Role: PriorReceiptRole, Receipt: copy}, nil
}
