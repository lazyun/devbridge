package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/executionproposal"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/run"
	rp "github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimequalification"
	rs "github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
	"github.com/lazyun/devbridge/internal/workset"
	"github.com/lazyun/devbridge/internal/workspace"
)

// No-Run continuation requires a formal runtime source bound to the exact
// revision. A legacy scalar approval is deliberately insufficient. Build these
// facts through the real confirmation, qualification and binding writers.
func qualifiedExecutionFixture(t *testing.T, suffix string) (*reviewApprovalFixture, task.Task, executionproposal.Proposal) {
	t.Helper()
	f, owner := newTaskOwnedFixture(t, true)
	ctx := retryCallerCtx(t, f, context.Background())
	cycle, err := NewDevelopmentCycleStore(f.db).Select(ctx, developmentcycle.Cycle{ID: "qualified-cycle", ProjectID: f.project, RepositoryID: "owned-repo", DevelopmentRef: "refs/heads/dev", SelectedHeadSHA: strings.Repeat("a", 40), RequestID: "qualified-development"})
	if err != nil {
		t.Fatal(err)
	}
	member := workspace.Member{ID: "qualified-member", WorkspaceID: "qualified-workspace", RepositoryID: "owned-repo", Version: 1, Alias: "Source", OriginCommit: strings.Repeat("a", 40), OriginRef: cycle.DevelopmentRef, DevelopmentCycleID: cycle.ID, Preparation: "PENDING", CreatedAt: testNow, UpdatedAt: testNow}
	if err = NewWorkspaceStore(f.db).Create(ctx, workspace.Workspace{ID: member.WorkspaceID, ProjectID: f.project, FeatureAreaID: f.area, Members: []workspace.Member{member}, Status: workspace.StatusReady, CreatedAt: testNow, UpdatedAt: testNow}); err != nil {
		t.Fatal(err)
	}
	target := workset.Target{RepositoryID: member.RepositoryID, MemberID: member.ID, MemberVersion: 1, OriginCommit: member.OriginCommit, OriginRef: member.OriginRef, DevelopmentCycleID: cycle.ID, Read: true, Write: workset.WriteScope{Kind: "NONE"}}
	f.candidate.RepositoryID, f.candidate.BaseCommit, f.candidate.RepositoryProvenance = "", "", nil
	f.candidate.CodexConfigID = codexconfig.DefaultConfigID
	f.candidate.WorkspaceID, f.candidate.RepositoryTargets, f.candidate.PrimaryMemberID = member.WorkspaceID, []workset.Target{target}, member.ID
	f.candidate.EngineeringGoal += suffix
	a := f.start(t)
	raw := `{"version":2,"reviewDisposition":"APPROVAL_READY","repositoryTargets":[{"targetAlias":"target-1","proposedWriteScope":{"kind":"PATHS","paths":["internal/config"]}}],"primaryTargetAlias":"target-1","summary":"Update bounded configuration","workSpec":{"implementationMode":"CODING_AGENT","goal":"Update configuration","constraints":["Preserve existing behavior"],"codexInstruction":"Update only internal/config","executionScope":{"kind":"REPOSITORY_TARGETS","paths":[]}},"verificationSpec":{"mode":"RUN","scope":"target-1","checks":[{"targetAlias":"target-1","targetAliases":["target-1"],"workingDirectory":".","kind":"TEST_COMMAND","command":"go test ./internal/config","instruction":"","paths":[],"packages":[],"required":true}],"acceptanceCriteria":["Configuration tests pass"],"codexInstruction":"Run the confirmed tests read-only"}}`
	f.complete(t, a, raw)
	var formal string
	if err = f.db.sql.QueryRow(`SELECT result_text FROM review_approval_decisions WHERE review_id=?`, a.Review.ID).Scan(&formal); err != nil {
		t.Fatal(err)
	}
	result, err := parsePersistedApprovalResult(formal)
	if err != nil {
		t.Fatal(err)
	}
	current := ownedTask(t, f)
	tx, err := f.db.beginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	topology, development, err := purposeProjectContextTx(ctx, tx, f.project)
	tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	prepare := c.PrepareInput{AISessionID: f.session, RequestID: "qualified-prepare", Purpose: c.Execution, Action: "APPROVE_AND_START", Execution: &c.ExecutionBody{Action: "APPROVE_AND_START", TaskID: owner.ID, FeatureAreaID: f.area, ReviewID: a.Review.ID, DecisionID: wp.DecisionID(a.Review.ID), TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, PreparedTopologyRevision: topology, PreparedDevelopmentContextHash: development}, Variants: []c.Variant{{ID: "approve", Summary: "Approve bounded fixture", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: result.RepositoryTargets, PrimaryMemberID: member.ID, WorkSpec: &result.WorkSpec, VerificationSpec: &result.VerificationSpec}}}
	prepare.SubmittedHash = c.ValueHash(prepare)
	confirmations := NewConfirmationStore(f.db)
	intent, err := confirmations.Prepare(ctx, f.project, prepare)
	if err != nil {
		t.Fatal(err)
	}
	backend := *f.candidate.BackendSelection
	platform := qualifiedFixturePlatform(t, f, backend)
	runtime := codexruntime.NextRunRuntimeConfig{Model: "gpt-5.6-terra", ReasoningEffort: "high"}
	backendInput := backend.Input()
	commit := c.CommitInput{AISessionID: f.session, IntentID: intent.IntentID, Version: intent.Version, Purpose: intent.Purpose, Action: intent.Action, RequestID: "qualified-commit", ContentHash: intent.ContentHash, VariantID: "approve", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &backendInput}, BackendSelection: &backend}
	commit.SubmittedHash = c.ValueHash(commit)
	if _, _, err = confirmations.BeginCommit(ctx, commit, []c.Step{{Kind: "APPROVE", RequestID: "confirmation_" + c.ValueHash([]string{intent.IntentID, "APPROVE"})[:32], State: "UNKNOWN"}}); err != nil {
		t.Fatal(err)
	}
	id, err := f.store.FormalExecutionIdentity(ctx, f.session, intent.IntentID, commit.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := f.store.CommitApproveAndStart(ctx, id, platform)
	if err != nil {
		t.Fatal(err)
	}
	selections := NewRuntimeSelectionStore(f.db)
	source, err := selections.ReadSource(ctx, receipt.T1.Identity.SourceOriginID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = selections.MaterializeBinding(ctx, rs.BindingInput{RequestID: "runtime_binding_" + source.SourceID, Owner: source.Owner, SourceID: source.SourceID, SourcePayloadHash: source.SourcePayloadHash, Entry: source.Entry, ExpectedLineageVersion: platform.LineageVersion}); err != nil {
		t.Fatal(err)
	}
	proposal, err := NewExecutionProposalStore(f.db).Get(ctx, receipt.T2.ExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	owner = ownedTask(t, f)
	return f, owner, proposal
}

// Explicit Core persistence-port fixture, not a production capability measurement.
func qualifiedFixturePlatform(t *testing.T, f *reviewApprovalFixture, backend codexconfig.BackendSelection) fe.PlatformReference {
	t.Helper()
	ctx := context.Background()
	store := NewRuntimePlatformStore(f.db)
	d := rp.Descriptor{DescriptorID: "qualified-descriptor", PlatformLineageID: "qualified-platform", RuntimeRole: "IMPLEMENTATION", AdapterID: "codex-app-server-v1", AdapterImplementationDigest: platformTestDigest("fixture adapter"), ProtocolID: "codex-app-server-v1", ProtocolSchemaDigest: platformTestDigest("fixture protocol"), CapabilityDigest: platformTestDigest("fixture capabilities"), DescriptorVersion: 1}
	if _, err := store.RegisterDescriptor(ctx, d); err != nil {
		t.Fatal(err)
	}
	e := rp.CompatibilityEvidence{DescriptorID: d.DescriptorID, PlatformLineageID: d.PlatformLineageID, RuntimeRole: d.RuntimeRole, AdapterImplementationDigest: d.AdapterImplementationDigest, ProtocolSchemaDigest: d.ProtocolSchemaDigest, CompatibilityPolicyVersion: "test-policy-v1", MeasuredCompatibility: true, CollectorIdentity: "test-core-collector", CollectionArtifactHash: platformTestDigest("fixture report"), Source: rp.SourceCoreCompatibilityCollection}
	e.EvidenceHash = e.ComputeHash()
	if _, err := store.MaterializeCompatibilityEvidence(ctx, e); err != nil {
		t.Fatal(err)
	}
	lineage, err := store.GetLineage(ctx, d.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	q, err := store.Qualify(ctx, rp.QualificationInput{QualificationID: "qualified-qualification", PlatformLineageID: d.PlatformLineageID, DescriptorID: d.DescriptorID, CompatibilityEvidenceHash: e.EvidenceHash, CompatibilityPolicyVersion: e.CompatibilityPolicyVersion, IssuanceRequestID: "qualified-issue", Generation: 1, ExpectedLineageVersion: lineage.Version})
	if err != nil {
		t.Fatal(err)
	}
	lineage, err = store.GetLineage(ctx, d.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	ref := fe.PlatformReference{PlatformLineageID: d.PlatformLineageID, QualificationID: q.QualificationID, QualificationGeneration: q.Generation, LineageVersion: lineage.Version, DescriptorID: d.DescriptorID, DescriptorHash: d.ComputeHash(), CompatibilityEvidenceHash: e.EvidenceHash, CompatibilityPolicyVersion: e.CompatibilityPolicyVersion, QualificationPayloadHash: q.IssuancePayloadHash}
	if err = store.ReserveQualificationRequest(ctx, runtimequalification.RequestIdentity{BackendID: backend.ConfigID, BackendFingerprint: backend.ConfigFingerprint, RuntimeRole: d.RuntimeRole, RequestID: "qualified-mapping", DescriptorID: d.DescriptorID, DescriptorHash: d.ComputeHash()}); err != nil {
		t.Fatal(err)
	}
	if err = store.SaveBackendMapping(ctx, runtimequalification.Mapping{BackendID: backend.ConfigID, BackendFingerprint: backend.ConfigFingerprint, RuntimeRole: d.RuntimeRole, RequestID: "qualified-mapping", Platform: ref}); err != nil {
		t.Fatal(err)
	}
	return ref
}

// Formal approval already commits the no-Run admission (T2). Scalar archives
// still pass through their separate reservation writer.
func reserveNoRunFixture(ctx context.Context, f *reviewApprovalFixture, taskID string) (executionproposal.Proposal, run.Run, error) {
	owner, err := NewTaskStore(f.db).Get(ctx, taskID)
	if err != nil {
		return executionproposal.Proposal{}, run.Run{}, err
	}
	proposal, err := NewExecutionProposalStore(f.db).Get(ctx, owner.CurrentExecutionID)
	if err == nil && proposal.Status == executionproposal.StatusAdmitted {
		return proposal, run.Run{}, nil
	}
	return f.store.ReserveTaskExecution(ctx, f.session, taskID)
}
