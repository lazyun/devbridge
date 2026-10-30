package controlapi_test

import (
	"testing"
	"time"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// These are persistence-port fixtures, not native compatibility evidence. Every
// business source and binding is issued through its real canonical transaction.
func TestExecutionRuntimeEligibilityUsesOriginalBindingLifecycle(t *testing.T) {
	f, id, ref := formalExecutionFixture(t)
	f.plane.Retirement.Close()
	receipt, err := f.plane.WorkflowProgression.CommitApproveAndStart(f.ctx, id, ref)
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	read := func(want bool, reason string) {
		t.Helper()
		before := definitionFormalSnapshot(t, f, formalExecutionTables())
		available, gotReason, err := f.plane.WorkflowProgression.ExecutionRuntimeEligibility(f.ctx, f.session, current.ID, current.FeatureAreaID, current.CurrentDefinitionRevisionID, current.CurrentExecutionID)
		if err != nil || available != want || gotReason != reason {
			t.Fatalf("available=%t reason=%q want=%t/%q err=%v", available, gotReason, want, reason, err)
		}
		assertDefinitionFormalSnapshot(t, f, formalExecutionTables(), before, "runtime eligibility must be a pure read")
	}
	read(false, runtimeselection.ErrBindingMissing.Error())
	selection := sqlite.NewRuntimeSelectionStore(f.plane.DB)
	source, err := selection.ReadSource(f.ctx, receipt.T1.Identity.SourceOriginID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = selection.MaterializeBinding(f.ctx, runtimeselection.BindingInput{RequestID: "runtime_binding_" + source.SourceID, Owner: source.Owner, SourceID: source.SourceID, SourcePayloadHash: source.SourcePayloadHash, Entry: source.Entry, ExpectedLineageVersion: source.Platform.LineageVersion}); err != nil {
		t.Fatal(err)
	}
	read(true, "")
	backend, err := f.plane.CodexConfigs.Get(f.ctx, source.Backend.Identity)
	if err != nil {
		t.Fatal(err)
	}
	// Fault injection models a live inventory change, without rewriting the
	// historical Execution, formal source, qualification, or binding.
	if _, err = f.plane.DB.SQL().Exec(`UPDATE codex_configs SET enabled=0 WHERE id=?`, backend.ID); err != nil {
		t.Fatal(err)
	}
	read(false, "FROZEN_BACKEND_SELECTION_UNAVAILABLE")
	if _, err = f.plane.DB.SQL().Exec(`UPDATE codex_configs SET enabled=1,codex_path=? WHERE id=?`, "/changed/backend", backend.ID); err != nil {
		t.Fatal(err)
	}
	read(false, "FROZEN_BACKEND_SELECTION_UNAVAILABLE")
	if _, err = f.plane.DB.SQL().Exec(`UPDATE codex_configs SET codex_path=? WHERE id=?`, backend.CodexPath, backend.ID); err != nil {
		t.Fatal(err)
	}
	read(true, "")
	q := receipt.Archive.Qualification
	platform := sqlite.NewRuntimePlatformStore(f.plane.DB)
	l, err := platform.GetLineage(f.ctx, q.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = platform.Supersede(f.ctx, runtimeplatform.QualificationInput{QualificationID: "eligibility-next-q", PlatformLineageID: q.PlatformLineageID, DescriptorID: q.DescriptorID, CompatibilityPolicyVersion: q.CompatibilityPolicyVersion, CompatibilityEvidenceHash: q.CompatibilityEvidenceHash, IssuanceRequestID: "eligibility-next-q-request", Generation: q.Generation + 1, ExpectedLineageVersion: l.Version, ExpectedQualificationID: q.QualificationID, ExpectedGeneration: q.Generation}); err != nil {
		t.Fatal(err)
	}
	read(true, "") // Supersession alone cannot invalidate an existing exact binding.
	l, err = platform.GetLineage(f.ctx, q.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = platform.Invalidate(f.ctx, runtimeplatform.InvalidationInput{QualificationID: q.QualificationID, Generation: q.Generation, ExpectedLineageVersion: l.Version, RequestID: "eligibility-revoke", Reason: "test explicit revocation"}); err != nil {
		t.Fatal(err)
	}
	read(false, runtimeplatform.ErrQualificationInvalidated.Error())
	before := definitionFormalSnapshot(t, f, formalExecutionTables())
	presented, err := f.facade.TaskPresentationForSession(f.ctx, f.session, current.FeatureAreaID, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, action := range presented.TaskView.Actions {
		if action.Action == "START_EXECUTION" {
			found = true
			if action.Available || action.Reason != runtimeplatform.ErrQualificationInvalidated.Error() {
				t.Fatalf("revoked start advertised: %+v", action)
			}
		}
	}
	if !found {
		t.Fatal("missing explicit disabled start action")
	}
	assertDefinitionFormalSnapshot(t, f, formalExecutionTables(), before, "revoked action projection must be a pure read")
}

func TestReverificationProjectionRejectsRevokedBindingBeforeConsumption(t *testing.T) {
	runCodingDeliveryFixture(t, func(f *upperLifecycleFixture, executionID string) bool {
		f.plane.Retirement.Close()
		deadline := time.Now().Add(15 * time.Second)
		var current task.Task
		for {
			submitCompletedFixtureVerificationAnalysis(t, f, executionID)
			var err error
			current, err = f.facade.GetTask(f.ctx, f.task)
			if err != nil {
				t.Fatal(err)
			}
			if current.Phase == task.PhaseAcceptancePending {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("implementation/verification did not finish: %+v", current)
			}
			time.Sleep(20 * time.Millisecond)
		}
		execution, err := f.facade.GetExecutionProposal(f.ctx, f.session, executionID)
		if err != nil {
			t.Fatal(err)
		}
		for _, runID := range []string{execution.RunID, execution.VerificationRunID} {
			if session, ok := f.plane.Supervisor.GetSession(runID); ok {
				select {
				case <-session.Done():
				case <-time.After(8 * time.Second):
					t.Fatal("runtime pipeline did not join")
				}
			}
		}
		check := func(want bool) {
			t.Helper()
			view, err := f.facade.TaskPresentationForSession(f.ctx, f.session, f.area, f.task)
			if err != nil {
				t.Fatal(err)
			}
			for _, action := range view.TaskView.Actions {
				if action.Action == "REQUEST_REVERIFICATION" {
					if action.Available != want || !want && action.Reason != runtimeplatform.ErrQualificationInvalidated.Error() {
						t.Fatalf("reverification availability: %+v", action)
					}
					return
				}
			}
			t.Fatal("reverification action disappeared instead of carrying its eligibility")
		}
		check(true)
		authority, err := sqlite.NewRuntimeSelectionStore(f.plane.DB).ReadCurrent(f.ctx, runtimeselection.Owner{ProjectID: f.project, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, RuntimeRole: "IMPLEMENTATION"})
		if err != nil {
			t.Fatal(err)
		}
		platform := sqlite.NewRuntimePlatformStore(f.plane.DB)
		l, err := platform.GetLineage(f.ctx, authority.Platform.PlatformLineageID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = platform.Invalidate(f.ctx, runtimeplatform.InvalidationInput{QualificationID: authority.Platform.QualificationID, Generation: authority.Platform.QualificationGeneration, ExpectedLineageVersion: l.Version, RequestID: "reverification-revoke", Reason: "test explicit revocation"}); err != nil {
			t.Fatal(err)
		}
		before := definitionFormalSnapshot(t, f, definitionFormalEveryTable(t, f))
		check(false)
		_, err = f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "revoked-reverification", Purpose: c.Execution, Action: "REQUEST_REVERIFICATION", Execution: &c.ExecutionBody{Action: "REQUEST_REVERIFICATION", TaskID: f.task, FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExecutionID: executionID, DecisionID: wp.TaskAcceptanceDecisionID(executionID)}, Variants: []c.Variant{{ID: "reverify", Summary: "Verify the existing implementation again"}}})
		if err == nil {
			t.Fatal("revoked verification reached confirmation preparation")
		}
		assertDefinitionFormalSnapshot(t, f, definitionFormalEveryTable(t, f), before, "revoked reverify read and rejected prepare")
		return false
	})
}

func TestExecutionRuntimeEligibilityDoesNotOfferReplacementForRetiredConfig(t *testing.T) {
	f, id, ref := formalExecutionFixture(t)
	f.plane.Retirement.Close()
	receipt, err := f.plane.WorkflowProgression.CommitApproveAndStart(f.ctx, id, ref)
	if err != nil {
		t.Fatal(err)
	}
	selection := sqlite.NewRuntimeSelectionStore(f.plane.DB)
	source, err := selection.ReadSource(f.ctx, receipt.T1.Identity.SourceOriginID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = selection.MaterializeBinding(f.ctx, runtimeselection.BindingInput{RequestID: "retired-config-binding", Owner: source.Owner, SourceID: source.SourceID, SourcePayloadHash: source.SourcePayloadHash, Entry: source.Entry, ExpectedLineageVersion: source.Platform.LineageVersion}); err != nil {
		t.Fatal(err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := f.facade.CodexRuntimeInventory()
	if err != nil {
		t.Fatal(err)
	}
	for i := range inventory.Allowlist.ModelConfigs {
		if inventory.Allowlist.ModelConfigs[i].ModelID == source.Runtime.Model {
			inventory.Allowlist.ModelConfigs[i].Enabled = false
		}
	}
	if _, err = f.facade.ApplyCodexRuntimeInventory(inventory.Revision, inventory.Allowlist); err != nil {
		t.Fatal(err)
	}
	before := definitionFormalSnapshot(t, f, formalExecutionTables())
	available, reason, err := f.plane.WorkflowProgression.ExecutionRuntimeEligibility(f.ctx, f.session, current.ID, current.FeatureAreaID, current.CurrentDefinitionRevisionID, current.CurrentExecutionID)
	if err != nil || available || reason != "FROZEN_RUNTIME_CONFIGURATION_UNAVAILABLE" {
		t.Fatalf("retired runtime allowed: %t %q %v", available, reason, err)
	}
	assertDefinitionFormalSnapshot(t, f, formalExecutionTables(), before, "retired config eligibility")
}
