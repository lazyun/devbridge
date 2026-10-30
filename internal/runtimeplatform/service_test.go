package runtimeplatform

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type descriptorProviderFunc func(context.Context, string) (Descriptor, error)

func (f descriptorProviderFunc) ResolveDescriptor(ctx context.Context, id string) (Descriptor, error) {
	return f(ctx, id)
}

type independentCollectorFunc func(context.Context, Descriptor) (CollectedCompatibility, error)

func (f independentCollectorFunc) CollectCompatibility(ctx context.Context, d Descriptor) (CollectedCompatibility, error) {
	return f(ctx, d)
}

type collectionStore struct {
	Store
	descriptor        Descriptor
	evidence          CompatibilityEvidence
	reads, writes     int
	readErr, writeErr error
}

func (s *collectionStore) GetDescriptor(context.Context, string) (Descriptor, error) {
	s.reads++
	return s.descriptor, s.readErr
}

func (s *collectionStore) RegisterDescriptor(_ context.Context, d Descriptor) (Descriptor, error) {
	s.writes++
	s.descriptor = d
	return d, s.writeErr
}

func (s *collectionStore) MaterializeCompatibilityEvidence(_ context.Context, e CompatibilityEvidence) (CompatibilityEvidence, error) {
	s.writes++
	s.evidence = e
	return e, s.writeErr
}

func collectionDescriptor() Descriptor {
	return Descriptor{
		DescriptorID: "descriptor-1", PlatformLineageID: "lineage-1", RuntimeRole: "IMPLEMENTATION",
		AdapterID: "adapter-1", AdapterImplementationDigest: strings.Repeat("a", 64),
		ProtocolID: "protocol-1", ProtocolSchemaDigest: strings.Repeat("b", 64),
		CapabilityDigest: strings.Repeat("c", 64), DescriptorVersion: 1,
	}
}

func measuredCompatibility(d Descriptor) CollectedCompatibility {
	return CollectedCompatibility{
		DescriptorID: d.DescriptorID, PlatformLineageID: d.PlatformLineageID, RuntimeRole: d.RuntimeRole,
		AdapterImplementationDigest: d.AdapterImplementationDigest, ProtocolSchemaDigest: d.ProtocolSchemaDigest,
		CompatibilityPolicyVersion: "policy-1", CollectorIdentity: "independent-test-collector",
		CollectionArtifactHash: strings.Repeat("d", 64), MeasuredCompatibility: true,
	}
}

func TestServiceCollectsExactPersistedDescriptor(t *testing.T) {
	ctx := context.Background()
	descriptor := collectionDescriptor()
	store := &collectionStore{}
	provider := descriptorProviderFunc(func(_ context.Context, id string) (Descriptor, error) {
		if id != descriptor.DescriptorID {
			t.Fatalf("provider received %q", id)
		}
		return descriptor, nil
	})
	collector := independentCollectorFunc(func(_ context.Context, got Descriptor) (CollectedCompatibility, error) {
		if got != descriptor || store.reads != 1 {
			t.Fatal("collector did not receive the persisted descriptor")
		}
		return measuredCompatibility(got), nil
	})
	service := NewService(store, provider, collector)
	registered, err := service.RegisterDescriptor(ctx, descriptor.DescriptorID)
	if err != nil || registered != descriptor {
		t.Fatalf("register: %+v, %v", registered, err)
	}
	evidence, err := service.CollectCompatibilityEvidence(ctx, descriptor.DescriptorID)
	if err != nil {
		t.Fatal(err)
	}
	if store.writes != 2 || evidence != store.evidence || evidence.Source != SourceCoreCompatibilityCollection {
		t.Fatalf("unexpected persisted evidence: %+v, writes=%d", evidence, store.writes)
	}
	if err := evidence.ValidateDescriptor(descriptor); err != nil {
		t.Fatal(err)
	}
}

func TestServiceRequiresTrustedConstructionDependencies(t *testing.T) {
	ctx := context.Background()
	store := &collectionStore{descriptor: collectionDescriptor()}
	for _, service := range []*Service{nil, NewService(nil, nil, nil), NewService(store, nil, nil)} {
		if _, err := service.RegisterDescriptor(ctx, "descriptor-1"); !errors.Is(err, ErrCollectionUnavailable) {
			t.Fatalf("register: %v", err)
		}
		if _, err := service.CollectCompatibilityEvidence(ctx, "descriptor-1"); !errors.Is(err, ErrCollectionUnavailable) {
			t.Fatalf("collect: %v", err)
		}
	}
	if store.reads != 0 || store.writes != 0 {
		t.Fatalf("unconfigured service touched storage: %+v", store)
	}
}

func TestServiceRejectsUnregisteredOrCorruptDescriptorBeforeCollection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*collectionStore)
		want   error
	}{
		{"missing", func(s *collectionStore) { s.readErr = ErrDescriptorMissing }, ErrDescriptorMissing},
		{"wrong identity", func(s *collectionStore) { s.descriptor.DescriptorID = "other" }, ErrIdentityMismatch},
		{"corrupt digest", func(s *collectionStore) { s.descriptor.CapabilityDigest = "invalid" }, ErrIdentityMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &collectionStore{descriptor: collectionDescriptor()}
			tc.mutate(store)
			collector := independentCollectorFunc(func(context.Context, Descriptor) (CollectedCompatibility, error) {
				t.Fatal("collector called before descriptor validation")
				return CollectedCompatibility{}, nil
			})
			_, err := NewService(store, nil, collector).CollectCompatibilityEvidence(context.Background(), "descriptor-1")
			if !errors.Is(err, tc.want) || store.writes != 0 {
				t.Fatalf("err=%v writes=%d", err, store.writes)
			}
		})
	}
}

func TestServiceRejectsMismatchedOrUnmeasuredEvidenceWithoutWrites(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*CollectedCompatibility)
	}{
		{"descriptor", func(e *CollectedCompatibility) { e.DescriptorID = "other" }},
		{"lineage", func(e *CollectedCompatibility) { e.PlatformLineageID = "other" }},
		{"role", func(e *CollectedCompatibility) { e.RuntimeRole = "REVIEW" }},
		{"adapter", func(e *CollectedCompatibility) { e.AdapterImplementationDigest = strings.Repeat("e", 64) }},
		{"protocol", func(e *CollectedCompatibility) { e.ProtocolSchemaDigest = strings.Repeat("e", 64) }},
		{"incompatible", func(e *CollectedCompatibility) { e.MeasuredCompatibility = false }},
		{"missing policy", func(e *CollectedCompatibility) { e.CompatibilityPolicyVersion = "" }},
		{"missing collector", func(e *CollectedCompatibility) { e.CollectorIdentity = "" }},
		{"invalid artifact", func(e *CollectedCompatibility) { e.CollectionArtifactHash = "not-a-digest" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &collectionStore{descriptor: collectionDescriptor()}
			collector := independentCollectorFunc(func(_ context.Context, d Descriptor) (CollectedCompatibility, error) {
				evidence := measuredCompatibility(d)
				tc.mutate(&evidence)
				return evidence, nil
			})
			_, err := NewService(store, nil, collector).CollectCompatibilityEvidence(context.Background(), "descriptor-1")
			if !errors.Is(err, ErrBindingCorrupt) || store.writes != 0 {
				t.Fatalf("err=%v writes=%d", err, store.writes)
			}
		})
	}
}

func TestServiceRejectsProviderIdentityAndArtifactsWithoutWrites(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Descriptor)
	}{
		{"identity", func(d *Descriptor) { d.DescriptorID = "other" }},
		{"artifact", func(d *Descriptor) { d.AdapterImplementationDigest = "invalid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &collectionStore{}
			provider := descriptorProviderFunc(func(context.Context, string) (Descriptor, error) {
				d := collectionDescriptor()
				tc.mutate(&d)
				return d, nil
			})
			_, err := NewService(store, provider, nil).RegisterDescriptor(context.Background(), "descriptor-1")
			if !errors.Is(err, ErrIdentityMismatch) || store.writes != 0 {
				t.Fatalf("err=%v writes=%d", err, store.writes)
			}
		})
	}
}

func TestServicePreservesDependencyErrors(t *testing.T) {
	sentinel := errors.New("collector canceled")
	store := &collectionStore{descriptor: collectionDescriptor()}
	provider := descriptorProviderFunc(func(context.Context, string) (Descriptor, error) { return Descriptor{}, sentinel })
	collector := independentCollectorFunc(func(context.Context, Descriptor) (CollectedCompatibility, error) {
		return CollectedCompatibility{}, sentinel
	})
	service := NewService(store, provider, collector)
	if _, err := service.RegisterDescriptor(context.Background(), "descriptor-1"); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if _, err := service.CollectCompatibilityEvidence(context.Background(), "descriptor-1"); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if store.writes != 0 {
		t.Fatalf("failed dependencies caused %d writes", store.writes)
	}
}
