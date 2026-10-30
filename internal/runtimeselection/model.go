// Package runtimeselection describes immutable Core-approved runtime sources
// and their exact Task revision bindings. Hashes establish integrity, never
// provenance: only the formal business producer may persist an ApprovedSource.
package runtimeselection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
)

var (
	ErrSourceMissing      = errors.New("TASK_RUNTIME_SELECTION_SOURCE_MISSING")
	ErrBindingMissing     = errors.New("TASK_RUNTIME_SELECTION_BINDING_MISSING")
	ErrBindingConflict    = errors.New("TASK_RUNTIME_SELECTION_BINDING_CONFLICT")
	ErrNotCurrentRevision = errors.New("TASK_RUNTIME_SELECTION_BINDING_NOT_CURRENT_REVISION")
	ErrCorrupt            = runtimeplatform.ErrBindingCorrupt
)

type Entry string

const (
	EntryRuntimeAuthorized      Entry = "RUNTIME_AUTHORIZED_REVIEW_APPROVAL_OR_CONFIRMATION"
	EntryDefinitionOnlyAdoption Entry = "DEFINITION_ONLY_THEN_EXECUTION_CONFIRMATION"
	SnapshotSchemaID                  = "NO_RUN_CURRENT_RUNTIME_AUTHORITY_V1"
)

type Owner struct {
	ProjectID                string `json:"projectId"`
	TaskID                   string `json:"taskId"`
	TaskDefinitionRevisionID string `json:"taskDefinitionRevisionId"`
	RuntimeRole              string `json:"runtimeRole"`
}

// PlatformReference is captured from independent Core qualification facts in
// the producer transaction. LineageVersion fences that observation; it is never
// replaced by the newest qualification during replay or source materialization.
type PlatformReference struct {
	PlatformLineageID          string `json:"platformLineageId"`
	QualificationID            string `json:"qualificationId"`
	QualificationGeneration    int    `json:"qualificationGeneration"`
	DescriptorID               string `json:"descriptorId"`
	DescriptorHash             string `json:"descriptorHash"`
	CompatibilityEvidenceHash  string `json:"compatibilityEvidenceHash"`
	CompatibilityPolicyVersion string `json:"compatibilityPolicyVersion"`
	QualificationPayloadHash   string `json:"qualificationPayloadHash"`
	LineageVersion             int    `json:"lineageVersion"`
}

type BackendConfiguration struct {
	Identity          string `json:"identity"`
	Fingerprint       string `json:"fingerprint"`
	Name              string `json:"name"`
	Source            string `json:"source"`
	Kind              string `json:"kind"`
	PlatformLineageID string `json:"platformLineageId"`
}

// ApprovedSource is an immutable projection of one independently verified
// formal completion archive. There is intentionally no public PutSource port.
type ApprovedSource struct {
	SourceID                     string                            `json:"sourceId"`
	Owner                        Owner                             `json:"owner"`
	Platform                     PlatformReference                 `json:"platform"`
	Backend                      BackendConfiguration              `json:"backendConfiguration"`
	Runtime                      codexruntime.NextRunRuntimeConfig `json:"runtimeConfiguration"`
	Current                      bool                              `json:"current"`
	Entry                        Entry                             `json:"entry"`
	SourceReceiptIdentity        string                            `json:"sourceReceiptIdentity"`
	SourceReceiptHash            string                            `json:"sourceReceiptHash"`
	SourceReceiptPayloadHash     string                            `json:"sourceReceiptPayloadHash"`
	SourceReceiptSubmittedHash   string                            `json:"sourceReceiptSubmittedHash"`
	SourceFormalContextDigest    string                            `json:"sourceFormalContextDigest"`
	SourcePayloadHash            string                            `json:"sourcePayloadHash"`
	AISessionID                  string                            `json:"aiSessionId"`
	RequestID                    string                            `json:"requestId"`
	IntentID                     string                            `json:"intentId"`
	IntentVersion                int                               `json:"intentVersion"`
	ContentHash                  string                            `json:"contentHash"`
	ResultID                     string                            `json:"resultId"`
	ResultHash                   string                            `json:"resultHash"`
	RevisionHash                 string                            `json:"revisionHash"`
	ProducerKind                 string                            `json:"producerKind"`
	OriginalPurpose              string                            `json:"originalPurpose"`
	OriginalAction               string                            `json:"originalAction"`
	VariantID                    string                            `json:"variantId"`
	NormalizedIssuanceClass      string                            `json:"normalizedIssuanceClass"`
	PriorApprovalReceiptIdentity string                            `json:"priorApprovalReceiptIdentity"`
	PriorApprovalProofHash       string                            `json:"priorApprovalProofHash"`
}

type AuthorityValues struct {
	BackendIdentity          string `json:"backendIdentity"`
	BackendConfigFingerprint string `json:"backendConfigFingerprint"`
	BackendSource            string `json:"backendSource"`
	BackendKind              string `json:"backendKind"`
	Model                    string `json:"model"`
	ReasoningEffort          string `json:"reasoningEffort"`
	CodexFastMode            bool   `json:"codexFastMode"`
	ServiceTier              string `json:"serviceTier"`
	RuntimeAdapterIdentity   string `json:"runtimeAdapterIdentity"`
	RuntimeProtocolIdentity  string `json:"runtimeProtocolIdentity"`
}

type Binding struct {
	BindingID             string            `json:"bindingId"`
	Owner                 Owner             `json:"owner"`
	Platform              PlatformReference `json:"platform"`
	SourceID              string            `json:"sourceId"`
	SourcePayloadHash     string            `json:"sourcePayloadHash"`
	Entry                 Entry             `json:"entry"`
	IssuanceRequestID     string            `json:"issuanceRequestId"`
	IssuancePayloadHash   string            `json:"issuancePayloadHash"`
	SnapshotSchemaID      string            `json:"snapshotSchemaId"`
	AuthoritySnapshotHash string            `json:"authoritySnapshotHash"`
	Values                AuthorityValues   `json:"values"`
	CreatedAt             time.Time         `json:"createdAt"`
}

type BindingInput struct {
	RequestID              string
	Owner                  Owner
	SourceID               string
	SourcePayloadHash      string
	Entry                  Entry
	ExpectedLineageVersion int
}

type CurrentAuthority struct {
	Owner     Owner  `json:"owner"`
	BindingID string `json:"bindingId"`
	// Recovery consumes a purpose receipt for one exact Execution. It is not
	// a third formal selection binding and leaves BindingID empty.
	PurposeConsumptionID  string            `json:"purposeConsumptionId,omitempty"`
	Platform              PlatformReference `json:"platform"`
	Values                AuthorityValues   `json:"values"`
	AuthoritySnapshotHash string            `json:"authoritySnapshotHash"`
}

type Store interface {
	ReadSource(context.Context, string) (ApprovedSource, error)
	MaterializeBinding(context.Context, BindingInput) (Binding, error)
	ReplayBinding(context.Context, BindingInput) (Binding, error)
	ReadCurrent(context.Context, Owner) (CurrentAuthority, error)
}

// ExecutionAuthorityReader is an optional exact-execution authority port. A
// reader may verify a durable successor chain without manufacturing a formal
// revision binding. Failure never permits fallback to an unscoped authority.
type ExecutionAuthorityReader interface {
	ReadExecutionAuthority(context.Context, Owner, string) (CurrentAuthority, error)
}

func exactText(s string) bool { return s != "" && utf8.ValidString(s) && strings.TrimSpace(s) == s }
func ValidEntry(e Entry) bool { return e == EntryRuntimeAuthorized || e == EntryDefinitionOnlyAdoption }
func (o Owner) Validate() error {
	if !exactText(o.ProjectID) || !exactText(o.TaskID) || !exactText(o.TaskDefinitionRevisionID) || !runtimeplatform.ValidRole(o.RuntimeRole) {
		return ErrCorrupt
	}
	return nil
}
func (p PlatformReference) Validate() error {
	if !exactText(p.PlatformLineageID) || !exactText(p.QualificationID) || p.QualificationGeneration < 1 || !exactText(p.DescriptorID) || !runtimeplatform.ValidDigest(p.DescriptorHash) || !runtimeplatform.ValidDigest(p.CompatibilityEvidenceHash) || !exactText(p.CompatibilityPolicyVersion) || !runtimeplatform.ValidDigest(p.QualificationPayloadHash) || p.LineageVersion < 1 {
		return ErrCorrupt
	}
	return nil
}
func HashValue(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	var decoded any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&decoded) != nil {
		return ""
	}
	raw, err = json.Marshal(decoded)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func ReceiptIdentity(session, request string) string { return HashValue([]string{session, request}) }
func SourceIdentity(session, request, result string) string {
	return HashValue([]string{session, request, result})
}
func (s ApprovedSource) ComputeHash() string { s.SourcePayloadHash = ""; return HashValue(s) }
func (s ApprovedSource) Validate() error {
	if s.Owner.Validate() != nil || s.Owner.RuntimeRole != "IMPLEMENTATION" || s.Platform.Validate() != nil || !s.Current || !ValidEntry(s.Entry) || s.OriginalPurpose != "EXECUTION" || s.IntentVersion < 1 {
		return ErrCorrupt
	}
	for _, v := range []string{s.AISessionID, s.RequestID, s.IntentID, s.ResultID, s.VariantID, s.Backend.Identity, s.Backend.Name, s.Backend.Source, s.Runtime.Model, s.Runtime.ReasoningEffort} {
		if !exactText(v) {
			return ErrCorrupt
		}
	}
	for _, v := range []string{s.SourceReceiptHash, s.SourceReceiptPayloadHash, s.SourceReceiptSubmittedHash, s.SourceFormalContextDigest, s.ContentHash, s.ResultHash, s.RevisionHash, s.Backend.Fingerprint} {
		if !runtimeplatform.ValidDigest(v) {
			return ErrCorrupt
		}
	}
	if s.SourceID != SourceIdentity(s.AISessionID, s.RequestID, s.ResultID) || s.SourceReceiptIdentity != ReceiptIdentity(s.AISessionID, s.RequestID) || s.SourcePayloadHash != s.ComputeHash() || s.Backend.PlatformLineageID != s.Platform.PlatformLineageID || s.Backend.Kind != "CODEX" || (!s.Runtime.CodexFastMode && s.Runtime.ServiceTier != "") || (s.Runtime.CodexFastMode && s.Runtime.ServiceTier != "priority") {
		return ErrCorrupt
	}
	switch s.OriginalAction {
	case "APPROVE_AND_START":
		if s.ProducerKind != "FORMAL_APPROVAL_COMMIT" || s.Entry != EntryRuntimeAuthorized || s.NormalizedIssuanceClass != "RUNTIME_AUTHORIZED_REVIEW_APPROVAL" || s.PriorApprovalReceiptIdentity != "" || s.PriorApprovalProofHash != "" {
			return ErrCorrupt
		}
	case "START_APPROVED":
		if s.ProducerKind != "FORMAL_CONFIRMATION_COMMIT" || !runtimeplatform.ValidDigest(s.PriorApprovalReceiptIdentity) || !runtimeplatform.ValidDigest(s.PriorApprovalProofHash) || s.PriorApprovalReceiptIdentity == s.SourceReceiptIdentity {
			return ErrCorrupt
		}
		want := "RUNTIME_AUTHORIZED_EXECUTION_CONFIRMATION"
		if s.Entry == EntryDefinitionOnlyAdoption {
			want = "DEFINITION_ONLY_THEN_EXECUTION_CONFIRMATION"
		}
		if s.NormalizedIssuanceClass != want {
			return ErrCorrupt
		}
	default:
		return ErrCorrupt
	}
	return nil
}
func (s ApprovedSource) BindingPayload() string {
	return HashValue(map[string]any{"owner": s.Owner, "sourceId": s.SourceID, "sourceReceiptIdentity": s.SourceReceiptIdentity, "sourcePayloadHash": s.SourcePayloadHash, "entry": s.Entry, "lineage": s.Platform.PlatformLineageID, "qualificationId": s.Platform.QualificationID, "generation": s.Platform.QualificationGeneration, "backend": s.Backend, "runtime": s.Runtime})
}
func (s ApprovedSource) Values(d runtimeplatform.Descriptor) (AuthorityValues, error) {
	if err := s.Validate(); err != nil {
		return AuthorityValues{}, err
	}
	if d.Validate() != nil || d.DescriptorID != s.Platform.DescriptorID || d.ComputeHash() != s.Platform.DescriptorHash || d.PlatformLineageID != s.Platform.PlatformLineageID || d.RuntimeRole != s.Owner.RuntimeRole {
		return AuthorityValues{}, runtimeplatform.ErrIdentityMismatch
	}
	adapter, _ := json.Marshal(map[string]string{"adapterId": d.AdapterID, "adapterImplementationDigest": d.AdapterImplementationDigest})
	protocol, _ := json.Marshal(map[string]string{"protocolId": d.ProtocolID, "protocolSchemaDigest": d.ProtocolSchemaDigest})
	return AuthorityValues{BackendIdentity: s.Backend.Identity, BackendConfigFingerprint: s.Backend.Fingerprint, BackendSource: s.Backend.Source, BackendKind: s.Backend.Kind, Model: s.Runtime.Model, ReasoningEffort: s.Runtime.ReasoningEffort, CodexFastMode: s.Runtime.CodexFastMode, ServiceTier: s.Runtime.ServiceTier, RuntimeAdapterIdentity: string(adapter), RuntimeProtocolIdentity: string(protocol)}, nil
}
func (in BindingInput) Validate() error {
	if !exactText(in.RequestID) || in.Owner.Validate() != nil || !runtimeplatform.ValidDigest(in.SourceID) || !runtimeplatform.ValidDigest(in.SourcePayloadHash) || !ValidEntry(in.Entry) || in.ExpectedLineageVersion < 0 {
		return ErrBindingConflict
	}
	return nil
}
func (b Binding) Validate(source ApprovedSource, d runtimeplatform.Descriptor) error {
	values, err := source.Values(d)
	if err != nil {
		return err
	}
	if !exactText(b.BindingID) || b.Owner != source.Owner || b.Platform != source.Platform || b.SourceID != source.SourceID || b.SourcePayloadHash != source.SourcePayloadHash || b.Entry != source.Entry || !exactText(b.IssuanceRequestID) || b.IssuancePayloadHash != source.BindingPayload() || b.SnapshotSchemaID != SnapshotSchemaID || b.AuthoritySnapshotHash != HashValue(values) || !reflect.DeepEqual(b.Values, values) || b.CreatedAt.IsZero() {
		return ErrCorrupt
	}
	return nil
}
func DecodeSource(raw []byte) (ApprovedSource, error) {
	var s ApprovedSource
	if err := exactJSON(raw, &s); err != nil {
		return ApprovedSource{}, err
	}
	return s, s.Validate()
}
func DecodeBinding(raw []byte) (Binding, error) {
	var b Binding
	if err := exactJSON(raw, &b); err != nil {
		return Binding{}, err
	}
	return b, nil
}
func exactJSON(raw []byte, out any) error {
	if !utf8.Valid(raw) || json.Unmarshal(raw, out) != nil {
		return ErrCorrupt
	}
	canonical, err := json.Marshal(out)
	if err != nil || !bytes.Equal(raw, canonical) {
		return ErrCorrupt
	}
	return nil
}
