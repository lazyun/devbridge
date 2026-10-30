package controlapi_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lazyun/devbridge/internal/codex"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexlaunch"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/execution"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// The token below comes from a real native compatibility collection and a
// public formal approval's durable source/binding. Only inventory revocations
// are injected directly because that store currently exposes no update port.
// Native startup performs initialize only; no online model is called.
func TestQualifiedNativeLaunchTokenRechecksLiveAuthority(t *testing.T) {
	native := os.Getenv("DEVBRIDGE_PLATFORM_NATIVE_BINARY")
	if native == "" {
		t.Skip("explicit installed native required; offline loopback collection only")
	}
	native, err := filepath.EvalSymlinks(native)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../../build/constraint-followup-2026-10-02/native-live-launch")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	root, err = os.MkdirTemp(root, "public-")
	if err != nil {
		t.Fatal(err)
	}
	t.Log("native live launch evidence:", root)
	t.Setenv("TMPDIR", root)
	f := newUpperLifecycleFixture(t)
	f.plane.Retirement.Close()
	targets, work, verification, _ := submitLifecycleApprovalReady(t, f, "live-launch-review-result")
	parent, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	selectionFile := filepath.Join(root, "selection.json")
	if err = codexlaunch.Write(selectionFile, codexlaunch.Selection{Executable: native}); err != nil {
		t.Fatal(err)
	}
	t.Setenv(codexlaunch.SelectionEnv, selectionFile)
	backend, err := f.plane.CodexConfigs.Create(f.ctx, codexconfig.CreateInput{Name: "Native launch live authority", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	f.plane.RuntimeQualification.BaseCodexPath = ""
	f.plane.RuntimeQualification.EvidenceRoot = filepath.Join(root, "platform-evidence")
	mapping, err := f.plane.RuntimeQualification.QualifyBackend(f.ctx, backend.ID, "IMPLEMENTATION", "live-launch-qualification")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "live-launch-prepare", Purpose: c.Execution, Action: "APPROVE_AND_START", Execution: &c.ExecutionBody{Action: "APPROVE_AND_START", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), TaskDefinitionRevisionID: parent.CurrentDefinitionRevisionID, ExecutionID: parent.CurrentExecutionID}, Variants: []c.Variant{{ID: "approve", Summary: "Approve measured native for live launch checks", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: targets, PrimaryMemberID: targets[0].MemberID, WorkSpec: &work, VerificationSpec: &verification}}})
	if err != nil {
		t.Fatal(err)
	}
	options, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
	if err != nil || len(options.RuntimeOptions) == 0 {
		t.Fatalf("options: %+v %v", options, err)
	}
	runtime := codexruntime.NextRunRuntimeConfig{Model: options.RuntimeOptions[0].ModelID, ReasoningEffort: options.RuntimeOptions[0].SupportedReasoningEfforts[0]}
	commit := c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "live-launch-commit", ContentHash: prepared.ContentHash, VariantID: "approve", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ID, ConfigFingerprint: backend.Fingerprint()}}}
	completed, err := f.facade.CommitConfirmation(f.ctx, commit)
	if err != nil || completed.State != "COMPLETED" {
		t.Fatalf("formal producer: %+v %v", completed, err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := sqlite.NewExecutionProposalStore(f.plane.DB).GetByID(f.ctx, current.CurrentExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := f.plane.RuntimeQualification.VerifiedExecutionLaunch(f.ctx, owner)
	if err != nil {
		t.Fatal(err)
	}
	// Isolate even initialize from the user's installed native configuration.
	nativeHome := filepath.Join(root, "native-home")
	if err = os.MkdirAll(nativeHome, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", nativeHome)
	factory := execution.DefaultCodexFactory{Config: codex.Config{CodexPath: "/missing/default", VerifiedExecutable: launch}}
	start := func(t *testing.T, want error, refused bool) {
		t.Helper()
		before := definitionFormalSnapshot(t, f, []string{"tasks", "execution_proposals", "runs", "runtime_handoffs", "runtime_selection_sources", "runtime_selection_bindings"})
		session, e := factory.StartForRuntimeSelection(f.ctx, backend, runtime.Model, runtime.ReasoningEffort, runtime.ServiceTier, codex.AuthorizationReadOnly)
		if session != nil {
			_ = session.Close()
		}
		if refused && (e == nil || session != nil) {
			t.Fatalf("revoked retained token initialized native: session=%T error=%v", session, e)
		}
		if !refused && e != nil {
			t.Fatalf("original exact token could not initialize native: %v", e)
		}
		if want != nil && !errors.Is(e, want) {
			t.Fatalf("error=%v want=%v", e, want)
		}
		if definitionFormalSnapshot(t, f, []string{"tasks", "execution_proposals", "runs", "runtime_handoffs", "runtime_selection_sources", "runtime_selection_bindings"}) != before {
			t.Fatal("launch authority read changed durable business facts")
		}
	}
	t.Run("initial native", func(t *testing.T) { start(t, nil, false) })
	platform := sqlite.NewRuntimePlatformStore(f.plane.DB)
	q, err := platform.GetQualification(f.ctx, mapping.Platform.QualificationID, mapping.Platform.QualificationGeneration)
	if err != nil {
		t.Fatal(err)
	}
	lineage, err := platform.GetLineage(f.ctx, q.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = platform.Supersede(f.ctx, runtimeplatform.QualificationInput{QualificationID: "live-launch-new-q", PlatformLineageID: q.PlatformLineageID, DescriptorID: q.DescriptorID, CompatibilityPolicyVersion: q.CompatibilityPolicyVersion, CompatibilityEvidenceHash: q.CompatibilityEvidenceHash, IssuanceRequestID: "live-launch-supersede", Generation: q.Generation + 1, ExpectedLineageVersion: lineage.Version, ExpectedQualificationID: q.QualificationID, ExpectedGeneration: q.Generation}); err != nil {
		t.Fatal(err)
	}
	t.Run("superseded original binding", func(t *testing.T) { start(t, nil, false) })
	if err = codexlaunch.Write(selectionFile, codexlaunch.Selection{Error: "changed after token capture; must not reselect"}); err != nil {
		t.Fatal(err)
	}
	t.Run("selector change cannot replace captured native", func(t *testing.T) {
		path, e := launch.Executable(f.ctx)
		if e != nil || path != native {
			t.Fatalf("native=%q error=%v", path, e)
		}
		start(t, nil, false)
	})
	t.Run("disabled backend", func(t *testing.T) {
		if _, e := f.plane.DB.SQL().ExecContext(f.ctx, `UPDATE codex_configs SET enabled=0 WHERE id=?`, backend.ID); e != nil {
			t.Fatal(e)
		}
		defer func() {
			if _, e := f.plane.DB.SQL().ExecContext(f.ctx, `UPDATE codex_configs SET enabled=1 WHERE id=?`, backend.ID); e != nil {
				t.Error(e)
			}
		}()
		start(t, runtimeplatform.ErrIdentityMismatch, true)
	})
	t.Run("changed backend fingerprint", func(t *testing.T) {
		if _, e := f.plane.DB.SQL().ExecContext(f.ctx, `UPDATE codex_configs SET name=? WHERE id=?`, backend.Name+" changed", backend.ID); e != nil {
			t.Fatal(e)
		}
		defer func() {
			if _, e := f.plane.DB.SQL().ExecContext(f.ctx, `UPDATE codex_configs SET name=? WHERE id=?`, backend.Name, backend.ID); e != nil {
				t.Error(e)
			}
		}()
		start(t, runtimeplatform.ErrIdentityMismatch, true)
	})
	lineage, err = platform.GetLineage(f.ctx, q.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = platform.Invalidate(f.ctx, runtimeplatform.InvalidationInput{QualificationID: q.QualificationID, Generation: q.Generation, ExpectedLineageVersion: lineage.Version, RequestID: "live-launch-invalidate", Reason: "explicit revocation after token capture"}); err != nil {
		t.Fatal(err)
	}
	t.Run("invalidated original qualification", func(t *testing.T) { start(t, runtimeplatform.ErrQualificationInvalidated, true) })
}
