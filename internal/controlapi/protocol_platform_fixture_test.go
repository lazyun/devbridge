package controlapi_test

import (
	"context"
	"fmt"
	"github.com/lazyun/devbridge/internal/runanalysis"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexlaunch"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimequalification"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/taskgoal"
)

// These dependencies exercise protocol fixtures, not native compatibility.
// They exist only in a _test.go file and are installed explicitly in tests
// whose AppServer is a deterministic subprocess. Native qualification and
// artifact replacement are covered separately by the installed-native gates.
type protocolFixturePlatform struct {
	store             *sqlite.RuntimePlatformStore
	selections        *sqlite.RuntimeSelectionStore
	configs           *codexconfig.Service
	fixtureExecutable func(codexconfig.Config) (string, error)
}

func protocolFixtureDescriptor(config codexconfig.Config, role string) runtimeplatform.Descriptor {
	id := runtimeselection.HashValue([]string{"TEST_ONLY_PROTOCOL_DEPENDENCY", config.ID, config.Fingerprint(), role})
	return runtimeplatform.Descriptor{DescriptorID: "test-protocol-descriptor-" + id, PlatformLineageID: "test-protocol-lineage-" + id, RuntimeRole: role, AdapterID: "devbridge-codex", AdapterImplementationDigest: runtimeselection.HashValue("test-only Go protocol subprocess"), ProtocolID: "codex-app-server/codex-cli 0.149.0", ProtocolSchemaDigest: runtimeselection.HashValue("test-only protocol schema"), CapabilityDigest: runtimeselection.HashValue("test-only deterministic protocol dependency"), DescriptorVersion: 1}
}

// No request may ask this resolver to qualify a backend. Installation below
// registers the explicit fixture inventory first; the resolver is pure read.
func (p *protocolFixturePlatform) ResolvePlatformReference(ctx context.Context, selected codexconfig.BackendSelection, role string) (runtimeselection.PlatformReference, error) {
	config, err := p.configs.Get(ctx, selected.ConfigID)
	if err != nil {
		return runtimeselection.PlatformReference{}, err
	}
	if !config.Enabled || config.Fingerprint() != selected.ConfigFingerprint {
		return runtimeselection.PlatformReference{}, runtimeplatform.ErrIdentityMismatch
	}
	d := protocolFixtureDescriptor(config, role)
	m, err := p.store.ReadBackendMapping(ctx, config.ID, config.Fingerprint(), role, d.DescriptorID)
	if err != nil {
		return m.Platform, err
	}
	lineage, err := p.store.GetLineage(ctx, m.Platform.PlatformLineageID)
	m.Platform.LineageVersion = lineage.Version
	return m.Platform, err
}

func (p *protocolFixturePlatform) VerifyExecution(ctx context.Context, execution taskgoal.Execution) error {
	if execution.BackendSelection == nil {
		return runtimeselection.ErrBindingMissing
	}
	owner := runtimeselection.Owner{ProjectID: execution.ProjectID, TaskID: execution.TaskID, TaskDefinitionRevisionID: execution.TaskDefinitionRevisionID, RuntimeRole: "IMPLEMENTATION"}
	authority, err := p.selections.ReadCurrent(ctx, owner)
	if err != nil {
		return err
	}
	b, r, v := execution.BackendSelection, execution.RuntimeConfig, authority.Values
	if b.ConfigID != v.BackendIdentity || b.ConfigFingerprint != v.BackendConfigFingerprint || b.Source != v.BackendSource || r.Model != v.Model || r.ReasoningEffort != v.ReasoningEffort || r.CodexFastMode != v.CodexFastMode || r.ServiceTier != v.ServiceTier || execution.RuntimeAdapterIdentity != v.RuntimeAdapterIdentity || execution.RuntimeProtocolIdentity != v.RuntimeProtocolIdentity {
		return runtimeplatform.ErrIdentityMismatch
	}
	platform, err := p.ResolvePlatformReference(ctx, *b, "IMPLEMENTATION")
	if err != nil {
		return err
	}
	platform.LineageVersion = authority.Platform.LineageVersion
	if platform != authority.Platform {
		return runtimeplatform.ErrIdentityMismatch
	}
	return nil
}

// This test-only capability starts the deterministic protocol subprocess. It
// does not claim that a shell fixture is a native qualified artifact.
type protocolFixtureExecutable string

func (p protocolFixtureExecutable) Executable(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	path := string(p)
	return path, codexlaunch.Validate(path)
}

func (p *protocolFixturePlatform) VerifiedExecutionLaunch(ctx context.Context, value taskgoal.Execution) (codexlaunch.VerifiedExecutable, error) {
	if err := p.VerifyExecution(ctx, value); err != nil {
		return nil, err
	}
	selected, err := p.configs.Get(ctx, value.BackendSelection.ConfigID)
	if err != nil {
		return nil, err
	}
	path, err := p.fixtureExecutable(selected)
	if err != nil {
		return nil, err
	}
	path, err = filepath.EvalSymlinks(codexlaunch.Resolve(path))
	if err != nil {
		return nil, err
	}
	return protocolFixtureExecutable(path), nil
}

// installProtocolFixturePlatform preserves real SQLite issuance, immutable
// mapping, formal business producers and revision binding. Only artifact
// measurement is replaced, explicitly, for subprocess protocol fault tests.
// Call again after a cold Open or adding a new backend to that fixture.
func installProtocolFixturePlatform(t *testing.T, f *upperLifecycleFixture) {
	t.Helper()
	plane := f.plane
	p := &protocolFixturePlatform{store: sqlite.NewRuntimePlatformStore(plane.DB), selections: sqlite.NewRuntimeSelectionStore(plane.DB), configs: plane.CodexConfigs}
	p.fixtureExecutable = func(selected codexconfig.Config) (string, error) {
		if selected.CodexPath != "" {
			return selected.CodexPath, nil
		}
		factory, ok := plane.Coordinator.Codex.(execution.DefaultCodexFactory)
		if !ok {
			return "", fmt.Errorf("protocol fixture requires its explicit DefaultCodexFactory")
		}
		if factory.Config.CodexPath == "" {
			return "", fmt.Errorf("protocol fixture executable is not configured")
		}
		return factory.Config.CodexPath, nil
	}

	configs, err := plane.CodexConfigs.List(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, config := range configs {
		if !config.Enabled {
			continue
		}
		d := protocolFixtureDescriptor(config, "IMPLEMENTATION")
		if _, err = p.store.RegisterDescriptor(f.ctx, d); err != nil {
			t.Fatal(err)
		}
		e := runtimeplatform.CompatibilityEvidence{DescriptorID: d.DescriptorID, PlatformLineageID: d.PlatformLineageID, RuntimeRole: d.RuntimeRole, AdapterImplementationDigest: d.AdapterImplementationDigest, ProtocolSchemaDigest: d.ProtocolSchemaDigest, CompatibilityPolicyVersion: "TEST_ONLY_PROTOCOL_FIXTURE_V1", MeasuredCompatibility: true, CollectorIdentity: "test-only-substitute-not-native-collector", CollectionArtifactHash: runtimeselection.HashValue("test-only protocol contract"), Source: runtimeplatform.SourceCoreCompatibilityCollection}
		e.EvidenceHash = e.ComputeHash()
		if _, err = p.store.MaterializeCompatibilityEvidence(f.ctx, e); err != nil {
			t.Fatal(err)
		}
		lineage, err := p.store.GetLineage(f.ctx, d.PlatformLineageID)
		if err != nil {
			t.Fatal(err)
		}
		request := "test-protocol-issuance-" + d.DescriptorID
		q, err := p.store.Qualify(f.ctx, runtimeplatform.QualificationInput{QualificationID: request, PlatformLineageID: d.PlatformLineageID, DescriptorID: d.DescriptorID, CompatibilityPolicyVersion: e.CompatibilityPolicyVersion, CompatibilityEvidenceHash: e.EvidenceHash, IssuanceRequestID: request, Generation: 1, ExpectedLineageVersion: lineage.Version})
		if err != nil {
			t.Fatal(err)
		}
		identity := runtimequalification.RequestIdentity{BackendID: config.ID, BackendFingerprint: config.Fingerprint(), RuntimeRole: d.RuntimeRole, RequestID: request, DescriptorID: d.DescriptorID, DescriptorHash: d.ComputeHash()}
		if err = p.store.ReserveQualificationRequest(f.ctx, identity); err != nil {
			t.Fatal(err)
		}
		m := runtimequalification.Mapping{BackendID: config.ID, BackendFingerprint: config.Fingerprint(), RuntimeRole: d.RuntimeRole, RequestID: request, Platform: runtimeselection.PlatformReference{PlatformLineageID: d.PlatformLineageID, QualificationID: q.QualificationID, QualificationGeneration: q.Generation, DescriptorID: d.DescriptorID, DescriptorHash: d.ComputeHash(), CompatibilityEvidenceHash: e.EvidenceHash, CompatibilityPolicyVersion: e.CompatibilityPolicyVersion, QualificationPayloadHash: q.IssuancePayloadHash, LineageVersion: q.LineageVersion}}
		if err = p.store.SaveBackendMapping(f.ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	plane.Coordinator.QualifiedRuntime = p
	f.facade, err = controlapi.New(controlapi.Dependencies{RunAnalyses: &runanalysis.Service{Store: sqlite.NewRunAnalysisStore(plane.DB), Analyze: execution.AnalyzeRunReport}, RuntimeQualification: p, RuntimeSelections: p.selections, RecoveryStatus: plane.RecoveryStatus, RecoveryPurposeConsumers: plane.RecoveryPurposeConsumers, RepositoryUnlink: plane.RepositoryUnlink, Confirmations: plane.Confirmations, ConfirmationSessions: plane.AIContext, UpperReviewAccess: plane.UpperReviewAccess, UpperReviewPreparer: plane.Coordinator, UpperReviewResults: plane.UpperReviewResults, StartupContext: plane.AIContext, CodexConfigs: plane.CodexConfigs, WorktreeInventory: plane.WorktreeInventory, DirectFiles: plane.DirectFiles, AgentExecutions: plane.AgentExecutions, WorkflowProgression: plane.WorkflowProgression, DevelopmentCycles: plane.DevelopmentCycles, Projects: plane.Projects, ProjectRegistration: plane.ProjectRegistration, ProjectSelection: plane.ProjectSelection, AISessions: plane.AISessions, Repositories: plane.Repositories, Modules: plane.Modules, ProjectContexts: plane.ProjectContexts, ContextProbes: plane.ContextProbes, ContextProbeRunner: plane.ContextProbeRunner, Classifications: plane.Classifications, FeatureAreas: plane.FeatureAreas, AgentConversations: plane.AgentConversations, Documents: plane.Documents, DecisionResolutions: plane.DecisionResolutions, Reviews: plane.Reviews, Tasks: plane.Tasks, TaskPlans: plane.TaskPlans, ScopeReviews: plane.ScopeReviews, ScopeExpansions: plane.ScopeExpansions, ExecutionProposals: plane.ExecutionProposals, ExecutionCancellations: plane.ExecutionCancellations, ExecutionGoals: plane.ExecutionGoals, Runs: plane.Runs, Workspaces: plane.Workspaces, Evidence: plane.Evidence, Artifacts: plane.Artifacts, Events: plane.Events, Approvals: plane.Approvals, Runner: plane.Supervisor, ExecutionAllowlist: plane.CodexRuntime, CodexRuntimeInventory: plane.CodexRuntime, ReviewDrift: plane.ReviewDrift})
	if err != nil {
		t.Fatal(fmt.Errorf("install test-only protocol platform dependency: %w", err))
	}
}
