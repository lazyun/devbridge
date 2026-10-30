package localmcp_test

import (
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/evidence"
	"github.com/lazyun/devbridge/internal/executionproposal"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	rp "github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimequalification"
	rs "github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/workspace"
)

// Successor tests need current formal runtime authority, rather than a legacy
// scalar approval. All effects below go through the canonical Core writers;
// the qualification is an explicit persistence-port fixture, not measurement.
func localMCPQualifiedAdmission(t *testing.T) (*integrationFixture, string, executionproposal.Proposal) {
	t.Helper()
	f := newIntegrationFixture(t)
	f.plane.Retirement.Close()
	sid := callToolMap(t, f.ctx, connectGPT(t, f.gptURL), "project_open", map[string]any{"projectId": f.projectID})["aiSessionId"].(string)
	area := closureArea(t, f, connectGPT(t, f.gptURL), sid, "Qualified no-Run continuation")
	owner, err := f.plane.Tasks.Create(f.ctx, task.CreateInput{AISessionID: sid, RequestID: "qualified-task", ProjectID: f.projectID, FeatureAreaID: area, ModuleID: f.moduleID, Goal: "Update onboarding", ProblemStatement: "Keep the bounded task across cancellation", Planning: true})
	if err != nil {
		t.Fatal(err)
	}
	base := fixtureGit(t, f.repoPath, "rev-parse", "HEAD")
	cycle, err := sqlite.NewDevelopmentCycleStore(f.plane.DB).Select(f.ctx, developmentcycle.Cycle{ID: "qualified-cycle", ProjectID: f.projectID, RepositoryID: f.repositoryID, DevelopmentRef: "refs/heads/main", SelectedHeadSHA: base, RequestID: "qualified-development"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	member := workspace.Member{ID: "qualified-member", WorkspaceID: "qualified-workspace", RepositoryID: f.repositoryID, Version: 1, Alias: "fixture", OriginCommit: base, OriginRef: cycle.DevelopmentRef, DevelopmentCycleID: cycle.ID, Preparation: "PENDING", CreatedAt: now, UpdatedAt: now}
	if err = sqlite.NewWorkspaceStore(f.plane.DB).Create(f.ctx, workspace.Workspace{ID: member.WorkspaceID, ProjectID: f.projectID, FeatureAreaID: area, Members: []workspace.Member{member}, Status: workspace.StatusReady, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	target := workset.Target{RepositoryID: f.repositoryID, MemberID: member.ID, MemberVersion: 1, OriginCommit: base, OriginRef: cycle.DevelopmentRef, DevelopmentCycleID: cycle.ID, Read: true, Write: workset.WriteScope{Kind: "NONE"}}
	config, err := f.plane.CodexConfigs.Get(f.ctx, codexconfig.DefaultConfigID)
	if err != nil {
		t.Fatal(err)
	}
	backend := config.Selection("qualified-fixture")
	runtime := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
	candidate := review.Review{TaskID: owner.ID, ProjectID: f.projectID, FeatureAreaID: area, CreatedByAISessionID: sid, ReviewDepth: task.ReviewDepthDeep, ExecutorKind: review.ExecutorCodingAgent, CodexConfigID: config.ID, BackendSelection: &backend, RuntimeConfig: runtime, Model: runtime.Model, ReasoningEffort: runtime.ReasoningEffort, WorkspaceID: member.WorkspaceID, RepositoryTargets: []workset.Target{target}, PrimaryMemberID: member.ID, Focus: review.Focus{Kind: review.FocusRepository}, EngineeringGoal: owner.ProblemStatement, Instruction: "Read the exact fixture target"}
	scope := run.ReviewScope{Kind: run.ReviewScopeRepository}
	admission, err := f.plane.Reviews.AdmitStart(f.ctx, candidate, "qualified-review", strings.Repeat("a", 64), candidate.Instruction, scope, run.ReviewPlan{Mode: run.ReviewModeRepository, InitialScope: scope})
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"version":2,"reviewDisposition":"APPROVAL_READY","repositoryTargets":[{"targetAlias":"target-1","proposedWriteScope":{"kind":"PATHS","paths":["tracked.txt"]}}],"primaryTargetAlias":"target-1","summary":"Update exact onboarding file","workSpec":{"implementationMode":"CODING_AGENT","goal":"Update onboarding","constraints":["Preserve existing behavior"],"codexInstruction":"Update tracked.txt","executionScope":{"kind":"REPOSITORY_TARGETS","paths":[]}},"verificationSpec":{"mode":"RUN","scope":"target-1","checks":[{"targetAlias":"target-1","targetAliases":["target-1"],"workingDirectory":".","kind":"TEST_COMMAND","command":"test -f tracked.txt","instruction":"","paths":[],"packages":[],"required":true}],"acceptanceCriteria":["Onboarding remains readable"],"codexInstruction":"Verify tracked.txt read-only"}}`
	if _, err = sqlite.NewRunStore(f.plane.DB).Update(f.ctx, admission.Run.ID, func(r *run.Run) error {
		r.Status = run.StatusSucceeded
		r.Result = &run.Result{AgentClaim: evidence.AgentClaim{Message: raw}}
		r.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var canonical string
	if err = f.plane.DB.SQL().QueryRow(`SELECT result_text FROM review_approval_decisions WHERE review_id=?`, admission.Review.ID).Scan(&canonical); err != nil {
		t.Fatal(err)
	}
	result, err := review.ParseResult(canonical)
	if err != nil {
		t.Fatal(err)
	}
	owner, err = f.plane.Tasks.Get(f.ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := f.control.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: sid, RequestID: "qualified-prepare", Purpose: c.Execution, Action: "APPROVE_AND_START", Execution: &c.ExecutionBody{Action: "APPROVE_AND_START", TaskID: owner.ID, FeatureAreaID: area, ReviewID: admission.Review.ID, DecisionID: wp.DecisionID(admission.Review.ID), TaskDefinitionRevisionID: owner.CurrentDefinitionRevisionID}, Variants: []c.Variant{{ID: "approve", Summary: "Approve qualified fixture", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: result.RepositoryTargets, PrimaryMemberID: member.ID, WorkSpec: &result.WorkSpec, VerificationSpec: &result.VerificationSpec}}})
	if err != nil {
		t.Fatal(err)
	}
	platform := localQualifiedPlatform(t, f, backend)
	backendInput := backend.Input()
	commit := c.CommitInput{AISessionID: sid, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, Action: intent.Action, RequestID: "qualified-commit", ContentHash: intent.ContentHash, VariantID: "approve", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &backendInput}, BackendSelection: &backend}
	commit.SubmittedHash = c.ValueHash(commit)
	if _, _, err = sqlite.NewConfirmationStore(f.plane.DB).BeginCommit(f.ctx, commit, []c.Step{{Kind: "APPROVE", RequestID: "confirmation_" + c.ValueHash([]string{intent.IntentID, "APPROVE"})[:32], State: "UNKNOWN"}}); err != nil {
		t.Fatal(err)
	}
	store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
	id, err := store.FormalExecutionIdentity(f.ctx, sid, intent.IntentID, commit.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := store.CommitApproveAndStart(f.ctx, id, platform)
	if err != nil {
		t.Fatal(err)
	}
	selections := sqlite.NewRuntimeSelectionStore(f.plane.DB)
	source, err := selections.ReadSource(f.ctx, receipt.T1.Identity.SourceOriginID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = selections.MaterializeBinding(f.ctx, rs.BindingInput{RequestID: "runtime_binding_" + source.SourceID, Owner: source.Owner, SourceID: source.SourceID, SourcePayloadHash: source.SourcePayloadHash, Entry: source.Entry, ExpectedLineageVersion: platform.LineageVersion}); err != nil {
		t.Fatal(err)
	}
	proposal, err := sqlite.NewExecutionProposalStore(f.plane.DB).Get(f.ctx, receipt.T2.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	return f, sid, proposal
}

func localQualifiedPlatform(t *testing.T, f *integrationFixture, backend codexconfig.BackendSelection) fe.PlatformReference {
	t.Helper()
	store := sqlite.NewRuntimePlatformStore(f.plane.DB)
	digest := func(s string) string { return fe.HashBytes([]byte(s)) }
	d := rp.Descriptor{DescriptorID: "qualified-descriptor", PlatformLineageID: "qualified-platform", RuntimeRole: "IMPLEMENTATION", AdapterID: "codex-app-server-v1", AdapterImplementationDigest: digest("fixture adapter"), ProtocolID: "codex-app-server-v1", ProtocolSchemaDigest: digest("fixture protocol"), CapabilityDigest: digest("fixture capabilities"), DescriptorVersion: 1}
	if _, err := store.RegisterDescriptor(f.ctx, d); err != nil {
		t.Fatal(err)
	}
	e := rp.CompatibilityEvidence{DescriptorID: d.DescriptorID, PlatformLineageID: d.PlatformLineageID, RuntimeRole: d.RuntimeRole, AdapterImplementationDigest: d.AdapterImplementationDigest, ProtocolSchemaDigest: d.ProtocolSchemaDigest, CompatibilityPolicyVersion: "test-policy-v1", MeasuredCompatibility: true, CollectorIdentity: "test-core-collector", CollectionArtifactHash: digest("fixture report"), Source: rp.SourceCoreCompatibilityCollection}
	e.EvidenceHash = e.ComputeHash()
	if _, err := store.MaterializeCompatibilityEvidence(f.ctx, e); err != nil {
		t.Fatal(err)
	}
	lineage, err := store.GetLineage(f.ctx, d.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	q, err := store.Qualify(f.ctx, rp.QualificationInput{QualificationID: "qualified-qualification", PlatformLineageID: d.PlatformLineageID, DescriptorID: d.DescriptorID, CompatibilityEvidenceHash: e.EvidenceHash, CompatibilityPolicyVersion: e.CompatibilityPolicyVersion, IssuanceRequestID: "qualified-issue", Generation: 1, ExpectedLineageVersion: lineage.Version})
	if err != nil {
		t.Fatal(err)
	}
	lineage, err = store.GetLineage(f.ctx, d.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	ref := fe.PlatformReference{PlatformLineageID: d.PlatformLineageID, QualificationID: q.QualificationID, QualificationGeneration: q.Generation, LineageVersion: lineage.Version, DescriptorID: d.DescriptorID, DescriptorHash: d.ComputeHash(), CompatibilityEvidenceHash: e.EvidenceHash, CompatibilityPolicyVersion: e.CompatibilityPolicyVersion, QualificationPayloadHash: q.IssuancePayloadHash}
	if err = store.ReserveQualificationRequest(f.ctx, runtimequalification.RequestIdentity{BackendID: backend.ConfigID, BackendFingerprint: backend.ConfigFingerprint, RuntimeRole: d.RuntimeRole, RequestID: "qualified-mapping", DescriptorID: d.DescriptorID, DescriptorHash: d.ComputeHash()}); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveBackendMapping(f.ctx, runtimequalification.Mapping{BackendID: backend.ConfigID, BackendFingerprint: backend.ConfigFingerprint, RuntimeRole: d.RuntimeRole, RequestID: "qualified-mapping", Platform: ref}); err != nil {
		t.Fatal(err)
	}
	return ref
}
