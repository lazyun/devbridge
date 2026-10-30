package runtimeselection

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimepreparation"
)

// This fixture tests the domain decoder only; no fixture can issue a stored
// source. The persistence port accepts only independently verified archives.
func selectionDecoderFixture() (ApprovedSource, runtimeplatform.Descriptor) {
	h := HashValue("fixture")
	d := runtimeplatform.Descriptor{DescriptorID: "descriptor", PlatformLineageID: "lineage", RuntimeRole: "IMPLEMENTATION", AdapterID: "adapter", AdapterImplementationDigest: h, ProtocolID: "protocol", ProtocolSchemaDigest: h, CapabilityDigest: h, DescriptorVersion: 1}
	s := ApprovedSource{SourceID: SourceIdentity("session", "request", "decision"), Owner: Owner{"project", "task", "revision", "IMPLEMENTATION"}, Platform: PlatformReference{PlatformLineageID: "lineage", QualificationID: "qualification", QualificationGeneration: 1, DescriptorID: "descriptor", DescriptorHash: d.ComputeHash(), CompatibilityEvidenceHash: h, CompatibilityPolicyVersion: "policy", QualificationPayloadHash: h, LineageVersion: 1}, Backend: BackendConfiguration{Identity: "backend", Fingerprint: h, Name: "backend", Source: "CONFIG", Kind: "CODEX", PlatformLineageID: "lineage"}, Runtime: codexruntime.NextRunRuntimeConfig{Model: "model", ReasoningEffort: "high"}, Current: true, Entry: EntryRuntimeAuthorized, SourceReceiptIdentity: ReceiptIdentity("session", "request"), SourceReceiptHash: h, SourceReceiptPayloadHash: h, SourceReceiptSubmittedHash: h, SourceFormalContextDigest: h, AISessionID: "session", RequestID: "request", IntentID: "intent", IntentVersion: 1, ContentHash: h, ResultID: "decision", ResultHash: h, RevisionHash: h, ProducerKind: "FORMAL_APPROVAL_COMMIT", OriginalPurpose: "EXECUTION", OriginalAction: "APPROVE_AND_START", VariantID: "variant", NormalizedIssuanceClass: "RUNTIME_AUTHORIZED_REVIEW_APPROVAL"}
	s.SourcePayloadHash = s.ComputeHash()
	return s, d
}
func TestRuntimeSelectionRejectsRehashedMissingOrSubstitutedAuthority(t *testing.T) {
	s, _ := selectionDecoderFixture()
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*ApprovedSource){"cooperative missing launch fingerprint": func(s *ApprovedSource) {
		s.Runtime.AgentID = "pi"
		s.Runtime.ExecutionPolicy = "COOPERATIVE_TRUSTED_V1"
	}, "missing qualification": func(s *ApprovedSource) { s.Platform.QualificationID = "" }, "operator source": func(s *ApprovedSource) { s.ProducerKind = "LOCAL_OPERATOR" }, "consumption as source": func(s *ApprovedSource) { s.ProducerKind = "CONSUMED" }, "unrelated lineage": func(s *ApprovedSource) { s.Backend.PlatformLineageID = "other" }, "receipt identity": func(s *ApprovedSource) { s.SourceReceiptIdentity = HashValue("other") }, "adoption without prior": func(s *ApprovedSource) {
		s.OriginalAction = "START_APPROVED"
		s.ProducerKind = "FORMAL_CONFIRMATION_COMMIT"
		s.Entry = EntryDefinitionOnlyAdoption
		s.NormalizedIssuanceClass = "DEFINITION_ONLY_THEN_EXECUTION_CONFIRMATION"
	}}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			v := s
			mutate(&v)
			v.SourcePayloadHash = v.ComputeHash()
			if v.Validate() == nil {
				t.Fatal("invalid authority accepted after rehash")
			}
		})
	}
	raw, _ := json.Marshal(s)
	for _, raw := range []string{strings.Replace(string(raw), `"sourceId":`, `"SourceId":`, 1), strings.Replace(string(raw), `"sourceId":`, `"sourceId":"shadow","sourceId":`, 1), strings.TrimSuffix(string(raw), "}") + `,"unknown":true}`} {
		if _, err := DecodeSource([]byte(raw)); err == nil {
			t.Fatal("ambiguous source decoded")
		}
	}
}
func TestRuntimeSelectionAuthoritySnapshotMatchesPlanAndRejectsDrift(t *testing.T) {
	s, d := selectionDecoderFixture()
	v, err := s.Values(d)
	if err != nil {
		t.Fatal(err)
	}
	p := runtimepreparation.Plan{BackendIdentity: v.BackendIdentity, BackendConfigFingerprint: v.BackendConfigFingerprint, BackendSource: v.BackendSource, BackendKind: v.BackendKind, Model: v.Model, ReasoningEffort: v.ReasoningEffort, CodexFastMode: v.CodexFastMode, ServiceTier: v.ServiceTier, RuntimeAdapterIdentity: v.RuntimeAdapterIdentity, RuntimeProtocolIdentity: v.RuntimeProtocolIdentity}
	got, err := p.ComputeAuthoritySnapshotHash()
	if err != nil || got != HashValue(v) {
		t.Fatalf("snapshot domains differ %s %s %v", got, HashValue(v), err)
	}
	b := Binding{BindingID: "binding", Owner: s.Owner, Platform: s.Platform, SourceID: s.SourceID, SourcePayloadHash: s.SourcePayloadHash, Entry: s.Entry, IssuanceRequestID: "issue", IssuancePayloadHash: s.BindingPayload(), SnapshotSchemaID: SnapshotSchemaID, AuthoritySnapshotHash: HashValue(v), Values: v, CreatedAt: time.Now()}
	if err = b.Validate(s, d); err != nil {
		t.Fatal(err)
	}
	b.Values.Model = "different"
	b.AuthoritySnapshotHash = HashValue(b.Values)
	if b.Validate(s, d) == nil {
		t.Fatal("rehashed binding substituted selection")
	}
	d.AdapterImplementationDigest = HashValue("new binary")
	if _, err = s.Values(d); err == nil {
		t.Fatal("new binary substituted exact descriptor")
	}
}

func TestCooperativePolicyAuthoritySnapshotMatchesPlanAndRejectsDrift(t *testing.T) {
	s, d := selectionDecoderFixture()
	s.Runtime.AgentID = "pi"
	s.Runtime.ExecutionPolicy = "COOPERATIVE_TRUSTED_V1"
	s.Runtime.AgentConfigFingerprint = strings.Repeat("c", 64)
	s.SourcePayloadHash = s.ComputeHash()
	v, err := s.Values(d)
	if err != nil {
		t.Fatal(err)
	}
	p := runtimepreparation.Plan{BackendIdentity: v.BackendIdentity, BackendConfigFingerprint: v.BackendConfigFingerprint, BackendSource: v.BackendSource, BackendKind: v.BackendKind, Model: v.Model, ReasoningEffort: v.ReasoningEffort, CodexFastMode: v.CodexFastMode, ServiceTier: v.ServiceTier, AgentID: v.AgentID, AgentConfigFingerprint: v.AgentConfigFingerprint, ExecutionPolicy: v.ExecutionPolicy, RuntimeAdapterIdentity: v.RuntimeAdapterIdentity, RuntimeProtocolIdentity: v.RuntimeProtocolIdentity}
	got, err := p.ComputeAuthoritySnapshotHash()
	if err != nil || got != HashValue(v) {
		t.Fatalf("snapshot domains differ %s %s %v", got, HashValue(v), err)
	}
	b := Binding{BindingID: "binding", Owner: s.Owner, Platform: s.Platform, SourceID: s.SourceID, SourcePayloadHash: s.SourcePayloadHash, Entry: s.Entry, IssuanceRequestID: "issue", IssuancePayloadHash: s.BindingPayload(), SnapshotSchemaID: SnapshotSchemaID, AuthoritySnapshotHash: HashValue(v), Values: v, CreatedAt: time.Now()}
	if err = b.Validate(s, d); err != nil {
		t.Fatal(err)
	}
	b.Values.AgentConfigFingerprint = strings.Repeat("d", 64)
	b.AuthoritySnapshotHash = HashValue(b.Values)
	if b.Validate(s, d) == nil {
		t.Fatal("rehashed binding substituted selection")
	}
	d.AdapterImplementationDigest = HashValue("new binary")
	if _, err = s.Values(d); err == nil {
		t.Fatal("new binary substituted exact descriptor")
	}
}
