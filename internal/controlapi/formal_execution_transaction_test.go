package controlapi_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/runtimeepoch"
	rp "github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimequalification"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func formalExecutionFixture(t *testing.T) (*upperLifecycleFixture, fe.ReadIdentity, fe.PlatformReference) {
	t.Helper()
	return formalExecutionFixtureForUpper(t, newUpperLifecycleFixture(t))
}

func formalExecutionFixtureForUpper(t *testing.T, f *upperLifecycleFixture) (*upperLifecycleFixture, fe.ReadIdentity, fe.PlatformReference) {
	t.Helper()
	targets, work, verification, _ := submitLifecycleApprovalReady(t, f, "formal-execution-result")
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "formal-execution-prepare", Purpose: c.Execution, Action: "APPROVE_AND_START", Execution: &c.ExecutionBody{Action: "APPROVE_AND_START", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: current.CurrentExecutionID}, Variants: []c.Variant{{ID: "approve", Summary: "Approve exact scope and freeze qualified admission", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: targets, PrimaryMemberID: targets[0].MemberID, WorkSpec: &work, VerificationSpec: &verification}}})
	if err != nil {
		t.Fatalf("prepare formal: %v", err)
	}
	opts, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := differentDefinitionRuntime(t, opts, codexruntime.NextRunRuntimeConfig{})
	if len(opts.BackendCandidates) == 0 {
		t.Fatal("missing backend")
	}
	backend := opts.BackendCandidates[0]
	ref := formalExecutionPlatform(t, f, backend)
	commit := c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "formal-execution-commit", ContentHash: prepared.ContentHash, VariantID: "approve", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}}
	commit.SubmittedHash = c.ValueHash(commit)
	commit.BackendSelection = &backend
	step := c.Step{Kind: "APPROVE", RequestID: "confirmation_" + c.ValueHash([]string{prepared.IntentID, "APPROVE"})[:32], State: "UNKNOWN"}
	_, fresh, err := sqlite.NewConfirmationStore(f.plane.DB).BeginCommit(f.ctx, commit, []c.Step{step})
	if err != nil || !fresh {
		t.Fatalf("begin formal fresh=%t: %v", fresh, err)
	}
	id, err := sqlite.NewWorkflowProgressionStore(f.plane.DB).FormalExecutionIdentity(f.ctx, f.session, prepared.IntentID, commit.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	return f, id, ref
}

func formalExecutionPlatform(t *testing.T, f *upperLifecycleFixture, backend codexconfig.BackendSelection) fe.PlatformReference {
	t.Helper()
	// Explicit Core persistence-port fixture. This measures no production
	// artifact and never substitutes for the production independent collector.
	s := sqlite.NewRuntimePlatformStore(f.plane.DB)
	d := rp.Descriptor{DescriptorID: "formal-test-descriptor", PlatformLineageID: "formal-test-platform", RuntimeRole: "IMPLEMENTATION", AdapterID: "codex-app-server-v1", AdapterImplementationDigest: fe.HashBytes([]byte("test adapter")), ProtocolID: "codex-app-server-v1", ProtocolSchemaDigest: fe.HashBytes([]byte("test protocol")), CapabilityDigest: fe.HashBytes([]byte("test capabilities")), DescriptorVersion: 1}
	if _, err := s.RegisterDescriptor(f.ctx, d); err != nil {
		t.Fatal(err)
	}
	e := rp.CompatibilityEvidence{DescriptorID: d.DescriptorID, PlatformLineageID: d.PlatformLineageID, RuntimeRole: d.RuntimeRole, AdapterImplementationDigest: d.AdapterImplementationDigest, ProtocolSchemaDigest: d.ProtocolSchemaDigest, CompatibilityPolicyVersion: "test-policy-v1", MeasuredCompatibility: true, CollectorIdentity: "test-core-collector", CollectionArtifactHash: fe.HashBytes([]byte("independent test report")), Source: rp.SourceCoreCompatibilityCollection}
	e.EvidenceHash = e.ComputeHash()
	if _, err := s.MaterializeCompatibilityEvidence(f.ctx, e); err != nil {
		t.Fatal(err)
	}
	l, err := s.GetLineage(f.ctx, d.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.Qualify(f.ctx, rp.QualificationInput{QualificationID: "formal-test-qualification", PlatformLineageID: d.PlatformLineageID, DescriptorID: d.DescriptorID, CompatibilityEvidenceHash: e.EvidenceHash, CompatibilityPolicyVersion: e.CompatibilityPolicyVersion, IssuanceRequestID: "formal-test-issue", Generation: 1, ExpectedLineageVersion: l.Version})
	if err != nil {
		t.Fatal(err)
	}
	l, err = s.GetLineage(f.ctx, d.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	ref := fe.PlatformReference{PlatformLineageID: d.PlatformLineageID, QualificationID: q.QualificationID, QualificationGeneration: q.Generation, LineageVersion: l.Version, DescriptorID: d.DescriptorID, DescriptorHash: d.ComputeHash(), CompatibilityEvidenceHash: e.EvidenceHash, CompatibilityPolicyVersion: e.CompatibilityPolicyVersion, QualificationPayloadHash: q.IssuancePayloadHash}
	if err = s.ReserveQualificationRequest(f.ctx, runtimequalification.RequestIdentity{BackendID: backend.ConfigID, BackendFingerprint: backend.ConfigFingerprint, RuntimeRole: "IMPLEMENTATION", RequestID: "formal-test-mapping", DescriptorID: d.DescriptorID, DescriptorHash: d.ComputeHash()}); err != nil {
		t.Fatal(err)
	}
	if err = s.SaveBackendMapping(f.ctx, runtimequalification.Mapping{BackendID: backend.ConfigID, BackendFingerprint: backend.ConfigFingerprint, RuntimeRole: "IMPLEMENTATION", RequestID: "formal-test-mapping", Platform: ref}); err != nil {
		t.Fatal(err)
	}
	return ref
}

func formalExecutionTables() []string {
	return append(definitionFormalAllTables(), "formal_execution_fences", "formal_execution_t1", "formal_execution_t2", "formal_execution_receipts", "runtime_selection_sources", "runtime_selection_bindings")
}
func formalExecutionCounts(t *testing.T, f *upperLifecycleFixture) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, table := range []string{"formal_execution_t1", "formal_execution_t2", "formal_execution_receipts", "execution_proposals", "execution_proposal_admissions", "runtime_scope_plans", "runtime_preparation_authorities", "execution_start_effects", "runs", "runtime_selection_sources", "runtime_selection_bindings"} {
		var count int
		if err := f.plane.DB.SQL().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		out[table] = count
	}
	return out
}

func TestFormalExecutionThreeTransactionsColdRecovery(t *testing.T) {
	for _, stage := range []struct {
		table  string
		t1, t2 int
	}{{"formal_execution_t1", 0, 0}, {"formal_execution_t2", 1, 0}, {"formal_execution_receipts", 1, 1}} {
		t.Run(stage.table, func(t *testing.T) {
			f, id, ref := formalExecutionFixture(t)
			initial := formalExecutionCounts(t, f)
			before := definitionFormalSnapshot(t, f, formalExecutionTables())
			_, err := f.plane.DB.SQL().Exec(`CREATE TRIGGER formal_execution_test_fault BEFORE INSERT ON ` + stage.table + ` BEGIN SELECT RAISE(ABORT,'formal-stage-fault'); END`)
			if err != nil {
				t.Fatal(err)
			}
			store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
			if _, err = store.CommitApproveAndStart(f.ctx, id, ref); err == nil || !strings.Contains(err.Error(), "formal-stage-fault") {
				t.Fatalf("expected real stage commit fault: %v", err)
			}
			counts := formalExecutionCounts(t, f)
			if counts["formal_execution_t1"] != stage.t1 || counts["formal_execution_t2"] != stage.t2 || counts["formal_execution_receipts"] != 0 {
				t.Fatalf("prefix counts=%v", counts)
			}
			for _, table := range []string{"runs", "execution_start_effects", "runtime_preparation_authorities", "runtime_selection_sources", "runtime_selection_bindings"} {
				if counts[table] != initial[table] {
					t.Fatalf("%s escaped incomplete stage: %v -> %v", table, initial, counts)
				}
			}
			if stage.t1 == 0 {
				assertDefinitionFormalSnapshot(t, f, formalExecutionTables(), before, "T1 rollback")
			} else {
				p, e := store.ReadApproveAndStartProgress(f.ctx, id)
				if e != nil {
					t.Fatal(e)
				}
				if (p.T2 != nil) != (stage.t2 == 1) {
					t.Fatalf("progress=%+v", p)
				}
				current, e := f.facade.GetTask(f.ctx, f.task)
				if e != nil || current.Phase != task.PhaseImplementationPreparing {
					t.Fatalf("prefix task=%+v: %v", current, e)
				}
			}
			if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER formal_execution_test_fault`); err != nil {
				t.Fatal(err)
			}
			reopenDirectDeliveryFixture(t, f)
			store = sqlite.NewWorkflowProgressionStore(f.plane.DB)
			receipt, err := store.CommitApproveAndStart(f.ctx, id, ref)
			if err != nil {
				t.Fatalf("recover original prefix: %v", err)
			}
			if err = receipt.Validate(); err != nil {
				t.Fatal(err)
			}
			// A self-consistent rehash cannot replace the runtime frozen by T1.
			changedPlan := receipt.Plan
			changedPlan.Model = "foreign-model"
			changedPlan.AuthoritySnapshotHash, _ = changedPlan.ComputeAuthoritySnapshotHash()
			changedPlan.ContentHash, _ = changedPlan.ComputeContentHash()
			changedReadback := receipt.T2
			changedReadback.ScopePlanBinding = changedPlan.ContentHash
			changedProgress := fe.Progress{T1: receipt.T1, T2: &changedReadback, Archive: receipt.Archive, Plan: &changedPlan, NextTransaction: fe.T3Transaction}
			if err = changedProgress.Validate(); !errors.Is(err, fe.ErrCorrupt) {
				t.Fatalf("rehashed foreign runtime partial=%v", err)
			}
			counts = formalExecutionCounts(t, f)
			if counts["formal_execution_t1"] != 1 || counts["formal_execution_t2"] != 1 || counts["formal_execution_receipts"] != 1 || counts["runtime_selection_sources"] != 1 {
				t.Fatalf("completed facts=%v", counts)
			}
			for _, table := range []string{"runs", "execution_start_effects", "runtime_selection_bindings"} {
				if counts[table] != initial[table] {
					t.Fatalf("T3 created forbidden %s: %v", table, counts)
				}
			}
			current, err := f.facade.GetTask(f.ctx, f.task)
			if err != nil || current.Phase != task.PhaseImplementationReady || current.CurrentExecutionID != receipt.T2.ExecutionID {
				t.Fatalf("final task=%+v: %v", current, err)
			}
			before = definitionFormalSnapshot(t, f, formalExecutionTables())
			expected := wp.TaskExecutionExpectation{FeatureAreaID: current.FeatureAreaID, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: current.CurrentExecutionID, Phase: current.Phase, PrimaryMemberID: receipt.Plan.PrimaryMemberID}
			var worksetHash string
			if err = f.plane.DB.SQL().QueryRow(`SELECT approved_workset_hash FROM execution_proposals WHERE id=?`, current.CurrentExecutionID).Scan(&worksetHash); err != nil {
				t.Fatal(err)
			}
			expected.ApprovedWorksetHash = worksetHash
			if _, _, err = store.ReserveTaskExecutionExpectedCommand(f.ctx, f.session, f.task, "START_EXECUTION", "unbound-start", &expected); !errors.Is(err, runtimeselection.ErrBindingMissing) {
				t.Fatalf("unbound fresh start=%v", err)
			}
			assertDefinitionFormalSnapshot(t, f, formalExecutionTables(), before, "unbound fresh start")
			stale := runtimeepoch.WithFence(f.ctx, "prior-epoch", "prior-run")
			if _, err = store.ReadApproveAndStart(stale, id); err != nil {
				t.Fatalf("completed stale-reader=%v", err)
			}
			replay, err := store.CommitApproveAndStart(f.ctx, id, fe.PlatformReference{})
			if err != nil || !reflect.DeepEqual(replay, receipt) {
				t.Fatalf("original receipt replay: %v", err)
			}
			assertDefinitionFormalSnapshot(t, f, formalExecutionTables(), before, "completed readonly replay")
			wrong := id
			wrong.TypedPayloadHash = fe.HashBytes([]byte("different payload"))
			if _, err = store.CommitApproveAndStart(f.ctx, wrong, ref); !errors.Is(err, fe.ErrConflict) {
				t.Fatalf("changed identity=%v", err)
			}
			assertDefinitionFormalSnapshot(t, f, formalExecutionTables(), before, "identity conflict")
			for _, table := range []string{"formal_execution_t1", "formal_execution_t2", "formal_execution_receipts"} {
				if _, err = f.plane.DB.SQL().Exec(fmt.Sprintf("DELETE FROM %s", table)); err == nil {
					t.Fatalf("immutable %s deleted", table)
				}
			}
			// Public completion now reads the real final archive, independently
			// materializes its binding and saves the actual confirmation result.
			journal, e := f.plane.Confirmations.Get(f.ctx, id.AISessionID, id.IntentID)
			if e != nil || journal.Commit == nil {
				t.Fatalf("original journal: %v", e)
			}
			input := *journal.Commit
			input.SubmittedHash = id.SubmittedHash
			completed, e := f.facade.CommitConfirmation(f.ctx, input)
			if e != nil || completed.State != "COMPLETED" || len(completed.Steps) != 1 || completed.Steps[0].ExecutionID != receipt.T2.ExecutionID || completed.Steps[0].RunID != "" {
				t.Fatalf("public formal completion: %+v %v", completed, e)
			}
			counts = formalExecutionCounts(t, f)
			if counts["runtime_selection_bindings"] != 1 || counts["runs"] != initial["runs"] || counts["execution_start_effects"] != initial["execution_start_effects"] {
				t.Fatalf("public approval unexpectedly started: %v", counts)
			}
			before = definitionFormalSnapshot(t, f, formalExecutionTables())
			again, e := f.facade.CommitConfirmation(stale, input)
			if e != nil || again.State != "COMPLETED" {
				t.Fatalf("public completed stale replay: %s %v", again.State, e)
			}
			assertDefinitionFormalSnapshot(t, f, formalExecutionTables(), before, "public completed stale replay")
			for _, field := range []string{"runtime", "serviceTier", "variant"} {
				changed := input
				if field == "variant" {
					changed.VariantID = "foreign-variant"
				} else {
					runtime := *input.Selection.RuntimeConfig
					if field == "runtime" {
						runtime.Model = "foreign-model"
					} else {
						runtime.ServiceTier = "foreign-tier"
					}
					changed.Selection.RuntimeConfig = &runtime
				}
				// Even a caller repeating the old raw fingerprint cannot change
				// the normalized typed choice attached to that receipt.
				if _, e = f.facade.CommitConfirmation(stale, changed); !errors.Is(e, c.ErrRequestConflict) {
					t.Fatalf("changed %s under same submitted hash: %v", field, e)
				}
				assertDefinitionFormalSnapshot(t, f, formalExecutionTables(), before, "changed completed typed payload")
			}
		})
	}
}

func TestFormalExecutionConcurrentExactRequestHasOneResult(t *testing.T) {
	f, id, ref := formalExecutionFixture(t)
	before := formalExecutionCounts(t, f)
	type result struct {
		receipt fe.Receipt
		err     error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			r, e := sqlite.NewWorkflowProgressionStore(f.plane.DB).CommitApproveAndStart(f.ctx, id, ref)
			results <- result{r, e}
		}()
	}
	close(start)
	a, b := <-results, <-results
	if a.err != nil || b.err != nil {
		t.Fatalf("concurrent exact results: %v; %v", a.err, b.err)
	}
	if !reflect.DeepEqual(a.receipt, b.receipt) {
		t.Fatal("same request obtained different committed identities")
	}
	after := formalExecutionCounts(t, f)
	for _, table := range []string{"formal_execution_t1", "formal_execution_t2", "formal_execution_receipts", "runtime_selection_sources"} {
		if after[table] != 1 {
			t.Fatalf("duplicate %s: %v", table, after)
		}
	}
	for _, table := range []string{"runs", "execution_start_effects", "runtime_selection_bindings"} {
		if after[table] != before[table] {
			t.Fatalf("unexpected %s: %v -> %v", table, before, after)
		}
	}
}
