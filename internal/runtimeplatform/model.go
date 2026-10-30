// Package runtimeplatform describes Core-owned platform facts. Its Store is an
// internal persistence port, not a public authority-issuance API. A trusted Core
// registration and independent compatibility collection pipeline must supply
// these records; provenance strings alone never establish that trust.
package runtimeplatform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrDescriptorMissing        = errors.New("RUNTIME_PLATFORM_DESCRIPTOR_MISSING")
	ErrQualificationMissing     = errors.New("RUNTIME_PLATFORM_QUALIFICATION_MISSING")
	ErrQualificationAmbiguous   = errors.New("RUNTIME_PLATFORM_QUALIFICATION_AMBIGUOUS")
	ErrQualificationInvalidated = errors.New("RUNTIME_PLATFORM_QUALIFICATION_INVALIDATED")
	ErrQualificationNotCurrent  = errors.New("RUNTIME_PLATFORM_QUALIFICATION_NOT_CURRENT_FOR_NEW_BINDING")
	ErrIdentityMismatch         = errors.New("RUNTIME_PLATFORM_IDENTITY_MISMATCH")
	ErrSourceConflict           = errors.New("RUNTIME_PLATFORM_SOURCE_IDENTITY_CONFLICT")
	ErrBindingCorrupt           = errors.New("TASK_RUNTIME_SELECTION_BINDING_CORRUPT")
)

const (
	SourceCoreCompatibilityCollection = "CORE_COMPATIBILITY_COLLECTION"
	StateCurrent                      = "CURRENT"
	StateSuperseded                   = "SUPERSEDED"
	StateInvalidated                  = "INVALIDATED"
	OutcomeQualified                  = "QUALIFIED"
)

type Descriptor struct {
	DescriptorID                string `json:"descriptorId"`
	PlatformLineageID           string `json:"platformLineageId"`
	RuntimeRole                 string `json:"runtimeRole"`
	AdapterID                   string `json:"adapterId"`
	AdapterImplementationDigest string `json:"adapterImplementationDigest"`
	ProtocolID                  string `json:"protocolId"`
	ProtocolSchemaDigest        string `json:"protocolSchemaDigest"`
	CapabilityDigest            string `json:"capabilityDigest"`
	DescriptorVersion           int    `json:"descriptorVersion"`
}

type CompatibilityEvidence struct {
	EvidenceHash                string `json:"evidenceHash"`
	DescriptorID                string `json:"descriptorId"`
	PlatformLineageID           string `json:"platformLineageId"`
	RuntimeRole                 string `json:"runtimeRole"`
	AdapterImplementationDigest string `json:"adapterImplementationDigest"`
	ProtocolSchemaDigest        string `json:"protocolSchemaDigest"`
	CompatibilityPolicyVersion  string `json:"compatibilityPolicyVersion"`
	MeasuredCompatibility       bool   `json:"measuredCompatibility"`
	CollectorIdentity           string `json:"collectorIdentity"`
	CollectionArtifactHash      string `json:"collectionArtifactHash"`
	Source                      string `json:"source"`
}

type Qualification struct {
	QualificationID            string    `json:"qualificationId"`
	PlatformLineageID          string    `json:"platformLineageId"`
	Generation                 int       `json:"generation"`
	State                      string    `json:"state"`
	DescriptorID               string    `json:"descriptorId"`
	CompatibilityPolicyVersion string    `json:"compatibilityPolicyVersion"`
	CompatibilityEvidenceHash  string    `json:"compatibilityEvidenceHash"`
	Outcome                    string    `json:"outcome"`
	IssuanceRequestID          string    `json:"issuanceRequestId"`
	IssuancePayloadHash        string    `json:"issuancePayloadHash"`
	CreatedAt                  time.Time `json:"createdAt"`
	SupersededAt               time.Time `json:"supersededAt,omitempty"`
	InvalidatedAt              time.Time `json:"invalidatedAt,omitempty"`
	InvalidationReason         string    `json:"invalidationReason,omitempty"`
	LineageVersion             int       `json:"lineageVersion"`
	InvalidationRequestID      string    `json:"invalidationRequestId,omitempty"`
	InvalidationPayloadHash    string    `json:"invalidationPayloadHash,omitempty"`
}

type Lineage struct {
	PlatformLineageID string `json:"platformLineageId"`
	RuntimeRole       string `json:"runtimeRole"`
	HighWater         int    `json:"highWater"`
	Version           int    `json:"version"`
}

// Expected values are transaction fences, not qualification identity. Exact
// request replay precedes these fences and returns the historical generation.
type QualificationInput struct {
	QualificationID, PlatformLineageID, DescriptorID                         string
	CompatibilityPolicyVersion, CompatibilityEvidenceHash, IssuanceRequestID string
	Generation, ExpectedLineageVersion                                       int
	ExpectedQualificationID                                                  string
	ExpectedGeneration                                                       int
}

type InvalidationInput struct {
	QualificationID, RequestID, Reason string
	Generation, ExpectedLineageVersion int
}

type Store interface {
	RegisterDescriptor(context.Context, Descriptor) (Descriptor, error)
	MaterializeCompatibilityEvidence(context.Context, CompatibilityEvidence) (CompatibilityEvidence, error)
	Qualify(context.Context, QualificationInput) (Qualification, error)
	Supersede(context.Context, QualificationInput) (Qualification, error)
	Invalidate(context.Context, InvalidationInput) (Qualification, error)
	GetDescriptor(context.Context, string) (Descriptor, error)
	GetEvidence(context.Context, string) (CompatibilityEvidence, error)
	GetQualification(context.Context, string, int) (Qualification, error)
	GetLineage(context.Context, string) (Lineage, error)
}

func exactText(s string) bool { return s != "" && utf8.ValidString(s) && s == strings.TrimSpace(s) }
func ValidDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size && s == strings.ToLower(s)
}
func ValidRole(s string) bool {
	return s == "IMPLEMENTATION" || s == "VERIFICATION" || s == "REVIEW" || s == "INSPECTION"
}

// canonicalHash uses sorted object keys even for typed structs, and retains
// integer precision. No arbitrary caller-supplied hash is accepted as proof.
func canonicalHash(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	var object any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err = decoder.Decode(&object); err != nil {
		return ""
	}
	raw, err = json.Marshal(object)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (d Descriptor) Validate() error {
	if !exactText(d.DescriptorID) || !exactText(d.PlatformLineageID) || !ValidRole(d.RuntimeRole) || !exactText(d.AdapterID) || !exactText(d.ProtocolID) || !ValidDigest(d.AdapterImplementationDigest) || !ValidDigest(d.ProtocolSchemaDigest) || !ValidDigest(d.CapabilityDigest) || d.DescriptorVersion < 1 {
		return ErrIdentityMismatch
	}
	return nil
}
func (d Descriptor) ComputeHash() string { return canonicalHash(d) }
func (e CompatibilityEvidence) ComputeHash() string {
	e.EvidenceHash = ""
	return canonicalHash(e)
}
func (e CompatibilityEvidence) Validate() error {
	if !ValidDigest(e.EvidenceHash) || e.EvidenceHash != e.ComputeHash() || !exactText(e.DescriptorID) || !exactText(e.PlatformLineageID) || !ValidRole(e.RuntimeRole) || !ValidDigest(e.AdapterImplementationDigest) || !ValidDigest(e.ProtocolSchemaDigest) || !exactText(e.CompatibilityPolicyVersion) || !exactText(e.CollectorIdentity) || !ValidDigest(e.CollectionArtifactHash) || !e.MeasuredCompatibility || e.Source != SourceCoreCompatibilityCollection {
		return ErrBindingCorrupt
	}
	return nil
}
func (e CompatibilityEvidence) ValidateDescriptor(d Descriptor) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if e.DescriptorID != d.DescriptorID || e.PlatformLineageID != d.PlatformLineageID || e.RuntimeRole != d.RuntimeRole || e.AdapterImplementationDigest != d.AdapterImplementationDigest || e.ProtocolSchemaDigest != d.ProtocolSchemaDigest {
		return ErrBindingCorrupt
	}
	return d.Validate()
}
func (in QualificationInput) Validate() error {
	if !exactText(in.QualificationID) || !exactText(in.PlatformLineageID) || !exactText(in.DescriptorID) || !exactText(in.CompatibilityPolicyVersion) || !ValidDigest(in.CompatibilityEvidenceHash) || !exactText(in.IssuanceRequestID) || in.Generation < 1 || in.ExpectedLineageVersion < 0 || in.ExpectedGeneration < 0 {
		return ErrSourceConflict
	}
	return nil
}
func (in QualificationInput) ComputePayloadHash() string {
	return canonicalHash(map[string]any{"qualificationId": in.QualificationID, "lineage": in.PlatformLineageID, "generation": in.Generation, "descriptorId": in.DescriptorID, "evidenceHash": in.CompatibilityEvidenceHash, "policyVersion": in.CompatibilityPolicyVersion, "outcome": OutcomeQualified})
}
func (in InvalidationInput) Validate() error {
	if !exactText(in.QualificationID) || !exactText(in.RequestID) || !exactText(in.Reason) || in.Generation < 1 || in.ExpectedLineageVersion < 0 {
		return ErrSourceConflict
	}
	return nil
}
func (in InvalidationInput) ComputePayloadHash() string {
	return canonicalHash(map[string]any{"id": in.QualificationID, "generation": in.Generation, "reason": in.Reason})
}
