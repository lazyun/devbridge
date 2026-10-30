// Package runtimequalification connects independently measured Core artifacts
// to an explicit backend selection. Registration is an operator action; opening
// a project or observing a runtime never grants qualification.
package runtimequalification

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/platformcollector"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/taskgoal"
)

type Mapping struct {
	BackendID          string                             `json:"backendId"`
	BackendFingerprint string                             `json:"backendFingerprint"`
	RuntimeRole        string                             `json:"runtimeRole"`
	RequestID          string                             `json:"requestId"`
	Platform           runtimeselection.PlatformReference `json:"platform"`
}
type RequestIdentity struct {
	BackendID, BackendFingerprint, RuntimeRole, RequestID, DescriptorID, DescriptorHash string
}

// Qualification is an infrequent operator action. Serialize the multi-stage
// collection/issuance in this process; the DataRoot writer lock supplies the
// cross-process boundary, and durable reservations survive process restart.
var qualificationMu sync.Mutex

type Store interface {
	runtimeplatform.Store
	ReadBackendMapping(context.Context, string, string, string, string) (Mapping, error)
	ReadBackendMappingForRequest(context.Context, string) (Mapping, error)
	ReadQualificationRequest(context.Context, string) (RequestIdentity, error)
	ReserveQualificationRequest(context.Context, RequestIdentity) error
	SaveBackendMapping(context.Context, Mapping) error
	QualificationForRequest(context.Context, string) (runtimeplatform.Qualification, error)
	CurrentQualification(context.Context, string) (runtimeplatform.Qualification, error)
}
type Configurations interface {
	Get(context.Context, string) (codexconfig.Config, error)
}
type Service struct {
	Store                             Store
	Selections                        runtimeselection.Store
	Configurations                    Configurations
	EvidenceRoot, AdapterArtifactPath string
	// BaseCodexPath is the Core factory's fallback for an empty backend path.
	// An empty base keeps codexlaunch's normal Desktop/PATH selection semantics.
	BaseCodexPath string
}

// VerifyExecution runs immediately before handing a qualified execution to
// the adapter. A same-path binary replacement cannot inherit qualification.
func (s *Service) VerifyExecution(ctx context.Context, execution taskgoal.Execution) error {
	_, err := s.VerifiedExecutionLaunch(ctx, execution)
	return err
}

func (s *Service) provider(ctx context.Context, backend codexconfig.Config) (*platformcollector.Provider, *platformcollector.Collector, error) {
	if !backend.Enabled {
		return nil, nil, errors.New("selected backend is disabled")
	}
	path := backend.CodexPath
	if path == "" {
		path = s.BaseCodexPath
	}
	return platformcollector.New(ctx, platformcollector.Config{CodexPath: path, AdapterArtifactPath: s.AdapterArtifactPath, EvidenceRoot: filepath.Join(s.EvidenceRoot, "compatibility")})
}

// QualifyBackend accepts selection and idempotency identity only. No caller may
// submit a descriptor, compatibility assertion or qualification identity.
func (s *Service) QualifyBackend(ctx context.Context, backendID, role, request string) (Mapping, error) {
	var zero Mapping
	if request == "" || !runtimeplatform.ValidRole(role) {
		return zero, runtimeplatform.ErrIdentityMismatch
	}
	qualificationMu.Lock()
	defer qualificationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	backend, err := s.Configurations.Get(ctx, backendID)
	if err != nil {
		return zero, err
	}
	// A request belongs to its original selection, including the independently
	// measured artifact. Check that identity before any descriptor, evidence or
	// qualification can be persisted.
	reserved, lookupErr := s.Store.ReadQualificationRequest(ctx, request)
	if lookupErr != nil && !errors.Is(lookupErr, runtimeplatform.ErrQualificationMissing) {
		return zero, lookupErr
	}
	if lookupErr == nil && (reserved.BackendID != backend.ID || reserved.BackendFingerprint != backend.Fingerprint() || reserved.RuntimeRole != role) {
		return zero, runtimeplatform.ErrSourceConflict
	}
	provider, collector, err := s.provider(ctx, backend)
	if err != nil {
		return zero, err
	}
	d, err := provider.Descriptor(role)
	if err != nil {
		return zero, err
	}
	identity := RequestIdentity{BackendID: backend.ID, BackendFingerprint: backend.Fingerprint(), RuntimeRole: role, RequestID: request, DescriptorID: d.DescriptorID, DescriptorHash: d.ComputeHash()}
	if lookupErr == nil && reserved != identity {
		return zero, runtimeplatform.ErrSourceConflict
	}
	if bound, e := s.Store.ReadBackendMappingForRequest(ctx, request); e == nil {
		if bound.BackendID != identity.BackendID || bound.BackendFingerprint != identity.BackendFingerprint || bound.RuntimeRole != identity.RuntimeRole || bound.Platform.DescriptorID != identity.DescriptorID || bound.Platform.DescriptorHash != identity.DescriptorHash {
			return zero, runtimeplatform.ErrSourceConflict
		}
		return bound, nil
	} else if !errors.Is(e, runtimeplatform.ErrQualificationMissing) {
		return zero, e
	}
	if old, e := s.Store.ReadBackendMapping(ctx, backend.ID, backend.Fingerprint(), role, d.DescriptorID); e == nil {
		prior, e := s.Store.GetQualification(ctx, old.Platform.QualificationID, old.Platform.QualificationGeneration)
		if e != nil {
			return zero, e
		}
		if old.RequestID != request && prior.State != runtimeplatform.StateSuperseded && prior.State != runtimeplatform.StateInvalidated {
			return zero, runtimeplatform.ErrSourceConflict
		}
		if old.RequestID == request {
			return old, nil
		}
	} else if !errors.Is(e, runtimeplatform.ErrQualificationMissing) {
		return zero, e
	}
	if err = s.Store.ReserveQualificationRequest(ctx, identity); err != nil {
		return zero, err
	}
	// The issuance request is tied to actual artifacts, never a mutable path.
	issuance := "backend_qualification_" + runtimeselection.HashValue([]string{request, backend.ID, backend.Fingerprint(), role, d.DescriptorID})
	q, err := s.Store.QualificationForRequest(ctx, issuance)
	if errors.Is(err, runtimeplatform.ErrQualificationMissing) {
		// Explicit mapping of another backend to the same measured descriptor
		// shares that exact CURRENT qualification. It must not retire the first
		// backend's identical platform merely because its ConfigID differs.
		current, currentErr := s.Store.CurrentQualification(ctx, d.PlatformLineageID)
		if currentErr == nil && current.DescriptorID == d.DescriptorID {
			evidence, e := s.Store.GetEvidence(ctx, current.CompatibilityEvidenceHash)
			if e != nil {
				return zero, e
			}
			if current.State != runtimeplatform.StateCurrent || current.CompatibilityPolicyVersion != platformcollector.PolicyVersion || evidence.CompatibilityPolicyVersion != platformcollector.PolicyVersion || evidence.ValidateDescriptor(d) != nil {
				return zero, runtimeplatform.ErrIdentityMismatch
			}
			q, err = current, nil
		} else if currentErr != nil && !errors.Is(currentErr, runtimeplatform.ErrQualificationMissing) {
			return zero, currentErr
		}
	}
	if errors.Is(err, runtimeplatform.ErrQualificationMissing) {
		service := runtimeplatform.NewService(s.Store, provider, collector)
		if _, err = service.RegisterDescriptor(ctx, d.DescriptorID); err != nil {
			return zero, err
		}
		evidence, e := service.CollectCompatibilityEvidence(ctx, d.DescriptorID)
		if e != nil {
			return zero, e
		}
		lineage, e := s.Store.GetLineage(ctx, d.PlatformLineageID)
		if e != nil {
			return zero, e
		}
		input := runtimeplatform.QualificationInput{QualificationID: issuance, PlatformLineageID: d.PlatformLineageID, DescriptorID: d.DescriptorID, CompatibilityPolicyVersion: evidence.CompatibilityPolicyVersion, CompatibilityEvidenceHash: evidence.EvidenceHash, IssuanceRequestID: issuance, Generation: lineage.HighWater + 1, ExpectedLineageVersion: lineage.Version}
		prior, e := s.Store.CurrentQualification(ctx, d.PlatformLineageID)
		if e == nil {
			input.ExpectedQualificationID = prior.QualificationID
			input.ExpectedGeneration = prior.Generation
			q, err = s.Store.Supersede(ctx, input)
		} else if errors.Is(e, runtimeplatform.ErrQualificationMissing) {
			q, err = s.Store.Qualify(ctx, input)
		} else {
			return zero, e
		}
	}
	if err != nil {
		return zero, err
	}
	if q.DescriptorID != d.DescriptorID || q.State != "CURRENT" {
		return zero, runtimeplatform.ErrSourceConflict
	}
	lineage, err := s.Store.GetLineage(ctx, d.PlatformLineageID)
	if err != nil {
		return zero, err
	}
	m := Mapping{BackendID: backend.ID, BackendFingerprint: backend.Fingerprint(), RuntimeRole: role, RequestID: request, Platform: runtimeselection.PlatformReference{PlatformLineageID: d.PlatformLineageID, QualificationID: q.QualificationID, QualificationGeneration: q.Generation, DescriptorID: d.DescriptorID, DescriptorHash: d.ComputeHash(), CompatibilityEvidenceHash: q.CompatibilityEvidenceHash, CompatibilityPolicyVersion: q.CompatibilityPolicyVersion, QualificationPayloadHash: q.IssuancePayloadHash, LineageVersion: lineage.Version}}
	if err = s.Store.SaveBackendMapping(ctx, m); err != nil {
		return zero, err
	}
	return m, nil
}

func (s *Service) ResolvePlatformReference(ctx context.Context, backend codexconfig.BackendSelection, role string) (runtimeselection.PlatformReference, error) {
	var zero runtimeselection.PlatformReference
	config, err := s.Configurations.Get(ctx, backend.ConfigID)
	if err != nil {
		return zero, err
	}
	if config.Fingerprint() != backend.ConfigFingerprint {
		return zero, runtimeplatform.ErrIdentityMismatch
	}
	provider, _, err := s.provider(ctx, config)
	if err != nil {
		return zero, err
	}
	d, err := provider.Descriptor(role)
	if err != nil {
		return zero, err
	}
	m, err := s.Store.ReadBackendMapping(ctx, backend.ConfigID, backend.ConfigFingerprint, role, d.DescriptorID)
	if err != nil {
		return zero, fmt.Errorf("explicit runtime qualification required: %w", err)
	}
	if m.Platform.DescriptorHash != d.ComputeHash() {
		return zero, runtimeplatform.ErrIdentityMismatch
	}
	lineage, err := s.Store.GetLineage(ctx, m.Platform.PlatformLineageID)
	if err != nil {
		return zero, err
	}
	if lineage.RuntimeRole != role {
		return zero, runtimeplatform.ErrIdentityMismatch
	}
	// Refresh only the CAS token. No qualification/generation is selected
	// from live inventory, and no immutable mapping is rewritten.
	m.Platform.LineageVersion = lineage.Version
	return m.Platform, nil
}
