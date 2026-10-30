package codexruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// EvidenceDimension names one independently auditable runtime property.  A
// profile or client supplied boolean is deliberately not an evidence dimension.
type EvidenceDimension string

const (
	EvidenceResolvedLauncher EvidenceDimension = "RESOLVED_LAUNCHER"
	EvidenceNativeBinary     EvidenceDimension = "NATIVE_BINARY"
	EvidenceProtocolSchema   EvidenceDimension = "PROTOCOL_SCHEMA"
	EvidenceSandboxPolicy    EvidenceDimension = "SANDBOX_POLICY"
	EvidenceExactWorkset     EvidenceDimension = "EXACT_WORKSET"
	EvidenceMultiRootRead    EvidenceDimension = "MULTI_ROOT_READ"
	EvidenceMultiRootWrite   EvidenceDimension = "MULTI_ROOT_WRITE"
	EvidenceMixedPathScope   EvidenceDimension = "MIXED_PATH_SCOPE"
	EvidenceExcludedReads    EvidenceDimension = "EXCLUDED_PATH_READS"
	EvidenceResumeNarrowing  EvidenceDimension = "RESUME_NARROWING"
	EvidenceMetadata         EvidenceDimension = "METADATA_ISOLATION"
	EvidenceToolChannels     EvidenceDimension = "TOOL_CHANNELS"
	EvidenceApprovals        EvidenceDimension = "APPROVAL_CHANNELS"
	EvidenceStructuredResult EvidenceDimension = "STRUCTURED_RESULTS"
)

// EvidenceStatus is an observed state, not a capability declaration.
type EvidenceStatus string

const (
	EvidenceProven  EvidenceStatus = "PROVEN"
	EvidenceDenied  EvidenceStatus = "DENIED"
	EvidenceUnknown EvidenceStatus = "UNKNOWN"
)

var (
	ErrEvidenceInvalid  = errors.New("coding runtime evidence is invalid")
	ErrEvidenceMismatch = errors.New("coding runtime evidence identity mismatch")
	ErrEvidenceUnknown  = errors.New("coding runtime evidence is unknown")
	ErrEvidenceDenied   = errors.New("coding runtime evidence is denied")
)

// EvidenceFact binds one observation to its collection source and the digest
// of its non-secret detail. It intentionally contains no mutable supported
// flag: consumers must ask for the exact dimensions they need.
type EvidenceFact struct {
	Dimension    EvidenceDimension `json:"dimension"`
	Status       EvidenceStatus    `json:"status"`
	Source       string            `json:"source"`
	DetailDigest string            `json:"detailDigest"`
}

// RuntimeEvidence is a portable, immutable identity for an observed Coding
// runtime configuration. NewRuntimeEvidence and Verify reject incomplete
// identity data. This type only records evidence; it never launches Codex,
// probes the host, changes a model setting, or enables a runtime path.
type RuntimeEvidence struct {
	Version              int            `json:"version"`
	ConfigFingerprint    string         `json:"configFingerprint"`
	ConfigDigest         string         `json:"configDigest"`
	ResolvedLauncher     string         `json:"resolvedLauncher"`
	LauncherDigest       string         `json:"launcherDigest"`
	ResolvedNative       string         `json:"resolvedNative"`
	NativeDigest         string         `json:"nativeDigest"`
	NativeVersion        string         `json:"nativeVersion"`
	ProtocolSchemaDigest string         `json:"protocolSchemaDigest"`
	Platform             string         `json:"platform"`
	ProfileContentDigest string         `json:"profileContentDigest"`
	Facts                []EvidenceFact `json:"facts"`
	IdentityDigest       string         `json:"identityDigest"`
}

// EvidenceExpectation is the immutable execution selection against which an
// evidence record is checked. RequiredDimensions must all be PROVEN.
type EvidenceExpectation struct {
	// IdentityDigest is frozen by an authorization path. A checksum only detects
	// corruption; it cannot make a caller-created PROVEN claim authoritative.
	IdentityDigest     string
	ConfigFingerprint  string
	ConfigDigest       string
	RequiredDimensions []EvidenceDimension
}

// NewRuntimeEvidence validates and canonicalizes the record, returning a
// detached immutable value with its identity digest populated.
func NewRuntimeEvidence(value RuntimeEvidence) (RuntimeEvidence, error) {
	value = cloneRuntimeEvidence(value)
	value.IdentityDigest = ""
	if err := validateEvidenceShape(value); err != nil {
		return RuntimeEvidence{}, err
	}
	canonical, err := evidenceCanonicalJSON(value)
	if err != nil {
		return RuntimeEvidence{}, fmt.Errorf("%w: canonicalize: %v", ErrEvidenceInvalid, err)
	}
	sum := sha256.Sum256(canonical)
	value.IdentityDigest = hex.EncodeToString(sum[:])
	return value, nil
}

// Verify proves that a persisted record is intact and matches the frozen
// execution configuration. It refuses UNKNOWN and DENIED required dimensions.
func (value RuntimeEvidence) Verify(expected EvidenceExpectation) error {
	if expected.IdentityDigest == "" || expected.ConfigFingerprint == "" || expected.ConfigDigest == "" {
		return fmt.Errorf("%w: expected config identity is required", ErrEvidenceInvalid)
	}
	if value.ConfigFingerprint != expected.ConfigFingerprint || value.ConfigDigest != expected.ConfigDigest {
		return fmt.Errorf("%w: config identity", ErrEvidenceMismatch)
	}
	providedDigest := value.IdentityDigest
	canonical, err := NewRuntimeEvidence(value)
	if err != nil {
		return err
	}
	if providedDigest == "" || providedDigest != canonical.IdentityDigest {
		return fmt.Errorf("%w: identity digest", ErrEvidenceMismatch)
	}
	if canonical.IdentityDigest != expected.IdentityDigest {
		return fmt.Errorf("%w: frozen runtime identity", ErrEvidenceMismatch)
	}
	byDimension := make(map[EvidenceDimension]EvidenceFact, len(canonical.Facts))
	for _, fact := range canonical.Facts {
		byDimension[fact.Dimension] = fact
	}
	for _, dimension := range expected.RequiredDimensions {
		if !knownEvidenceDimension(dimension) {
			return fmt.Errorf("%w: unknown required evidence dimension %s", ErrEvidenceInvalid, dimension)
		}
		fact, ok := byDimension[dimension]
		if !ok || fact.Status == EvidenceUnknown {
			return fmt.Errorf("%w: %s", ErrEvidenceUnknown, dimension)
		}
		if fact.Status != EvidenceProven {
			return fmt.Errorf("%w: %s", ErrEvidenceDenied, dimension)
		}
	}
	return nil
}

func cloneRuntimeEvidence(value RuntimeEvidence) RuntimeEvidence {
	value.Facts = append([]EvidenceFact(nil), value.Facts...)
	return value
}

func validateEvidenceShape(value RuntimeEvidence) error {
	if value.Version != 1 {
		return fmt.Errorf("%w: version must be 1", ErrEvidenceInvalid)
	}
	if strings.TrimSpace(value.ConfigFingerprint) == "" || strings.TrimSpace(value.ResolvedLauncher) == "" || strings.TrimSpace(value.ResolvedNative) == "" || strings.TrimSpace(value.NativeVersion) == "" || strings.TrimSpace(value.Platform) == "" {
		return fmt.Errorf("%w: config, resolved binaries, version, and platform are required", ErrEvidenceInvalid)
	}
	if !filepath.IsAbs(value.ResolvedLauncher) || !filepath.IsAbs(value.ResolvedNative) {
		return fmt.Errorf("%w: resolved binary paths must be absolute", ErrEvidenceInvalid)
	}
	for _, field := range []struct{ name, value string }{
		{"config digest", value.ConfigDigest}, {"launcher digest", value.LauncherDigest}, {"native digest", value.NativeDigest}, {"protocol schema digest", value.ProtocolSchemaDigest}, {"profile content digest", value.ProfileContentDigest},
	} {
		if !sha256Hex(field.value) {
			return fmt.Errorf("%w: invalid %s", ErrEvidenceInvalid, field.name)
		}
	}
	if len(value.Facts) == 0 {
		return fmt.Errorf("%w: at least one evidence fact is required", ErrEvidenceInvalid)
	}
	slices.SortFunc(value.Facts, func(a, b EvidenceFact) int { return strings.Compare(string(a.Dimension), string(b.Dimension)) })
	for index, fact := range value.Facts {
		if !knownEvidenceDimension(fact.Dimension) || strings.TrimSpace(fact.Source) == "" || !sha256Hex(fact.DetailDigest) {
			return fmt.Errorf("%w: incomplete evidence fact", ErrEvidenceInvalid)
		}
		if fact.Status != EvidenceProven && fact.Status != EvidenceDenied && fact.Status != EvidenceUnknown {
			return fmt.Errorf("%w: invalid evidence status", ErrEvidenceInvalid)
		}
		if index > 0 && value.Facts[index-1].Dimension == fact.Dimension {
			return fmt.Errorf("%w: duplicate evidence dimension %s", ErrEvidenceInvalid, fact.Dimension)
		}
	}
	return nil
}

func knownEvidenceDimension(value EvidenceDimension) bool {
	switch value {
	case EvidenceResolvedLauncher, EvidenceNativeBinary, EvidenceProtocolSchema,
		EvidenceSandboxPolicy, EvidenceExactWorkset, EvidenceMultiRootRead,
		EvidenceMultiRootWrite, EvidenceMixedPathScope, EvidenceExcludedReads,
		EvidenceResumeNarrowing, EvidenceMetadata, EvidenceToolChannels,
		EvidenceApprovals, EvidenceStructuredResult:
		return true
	default:
		return false
	}
}

func evidenceCanonicalJSON(value RuntimeEvidence) ([]byte, error) {
	// IdentityDigest must never contribute to the bytes it authenticates.
	value.IdentityDigest = ""
	return json.Marshal(value)
}

func sha256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}
