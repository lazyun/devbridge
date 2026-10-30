package runtimeplatform

import (
	"context"
	"errors"
	"fmt"
)

var ErrCollectionUnavailable = errors.New("RUNTIME_PLATFORM_COLLECTION_UNAVAILABLE")

// DescriptorProvider is a trusted Core construction dependency. Its implementation
// must resolve registered platform artifacts; transport input, environment values,
// static assertions and runtime adapter callbacks are not registration evidence.
type DescriptorProvider interface {
	ResolveDescriptor(context.Context, string) (Descriptor, error)
}

// IndependentCollector must independently measure the supplied platform artifacts
// under a supported compatibility policy. Implementations belong to application
// construction, never to a request payload or an observed runtime callback.
// No production collector is provided by this package.
type IndependentCollector interface {
	CollectCompatibility(context.Context, Descriptor) (CollectedCompatibility, error)
}

// CollectedCompatibility contains measured facts. The service supplies provenance
// and computes the record digest, neither of which authenticates the collector.
// Trust comes from the application-wired collector and its measurement policy.
type CollectedCompatibility struct {
	DescriptorID, PlatformLineageID, RuntimeRole                          string
	AdapterImplementationDigest, ProtocolSchemaDigest                     string
	CompatibilityPolicyVersion, CollectorIdentity, CollectionArtifactHash string
	MeasuredCompatibility                                                 bool
}

// Service is the internal Core registration/collection boundary. It has no
// transport handler, default provider, automatic qualification or runtime effects.
type Service struct {
	store     Store
	provider  DescriptorProvider
	collector IndependentCollector
}

func NewService(store Store, provider DescriptorProvider, collector IndependentCollector) *Service {
	return &Service{store: store, provider: provider, collector: collector}
}

func (s *Service) RegisterDescriptor(ctx context.Context, descriptorID string) (Descriptor, error) {
	if !exactText(descriptorID) {
		return Descriptor{}, ErrIdentityMismatch
	}
	if s == nil || s.store == nil || s.provider == nil {
		return Descriptor{}, ErrCollectionUnavailable
	}
	descriptor, err := s.provider.ResolveDescriptor(ctx, descriptorID)
	if err != nil {
		return Descriptor{}, fmt.Errorf("resolve runtime platform descriptor: %w", err)
	}
	if descriptor.DescriptorID != descriptorID {
		return Descriptor{}, ErrIdentityMismatch
	}
	if err := descriptor.Validate(); err != nil {
		return Descriptor{}, err
	}
	return s.store.RegisterDescriptor(ctx, descriptor)
}

func (s *Service) CollectCompatibilityEvidence(ctx context.Context, descriptorID string) (CompatibilityEvidence, error) {
	if !exactText(descriptorID) {
		return CompatibilityEvidence{}, ErrIdentityMismatch
	}
	if s == nil || s.store == nil || s.collector == nil {
		return CompatibilityEvidence{}, ErrCollectionUnavailable
	}
	descriptor, err := s.store.GetDescriptor(ctx, descriptorID)
	if err != nil {
		return CompatibilityEvidence{}, err
	}
	if descriptor.DescriptorID != descriptorID {
		return CompatibilityEvidence{}, ErrIdentityMismatch
	}
	if err := descriptor.Validate(); err != nil {
		return CompatibilityEvidence{}, err
	}
	collected, err := s.collector.CollectCompatibility(ctx, descriptor)
	if err != nil {
		return CompatibilityEvidence{}, fmt.Errorf("collect runtime platform compatibility: %w", err)
	}
	evidence := CompatibilityEvidence{
		DescriptorID:                collected.DescriptorID,
		PlatformLineageID:           collected.PlatformLineageID,
		RuntimeRole:                 collected.RuntimeRole,
		AdapterImplementationDigest: collected.AdapterImplementationDigest,
		ProtocolSchemaDigest:        collected.ProtocolSchemaDigest,
		CompatibilityPolicyVersion:  collected.CompatibilityPolicyVersion,
		CollectorIdentity:           collected.CollectorIdentity,
		CollectionArtifactHash:      collected.CollectionArtifactHash,
		MeasuredCompatibility:       collected.MeasuredCompatibility,
		Source:                      SourceCoreCompatibilityCollection,
	}
	evidence.EvidenceHash = evidence.ComputeHash()
	if err := evidence.ValidateDescriptor(descriptor); err != nil {
		return CompatibilityEvidence{}, err
	}
	return s.store.MaterializeCompatibilityEvidence(ctx, evidence)
}
