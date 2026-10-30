package controlapi_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	fe "github.com/lazyun/devbridge/internal/formalexecution"
	rp "github.com/lazyun/devbridge/internal/runtimeplatform"
	rs "github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
)

func TestFormalAdoptionAtomicCommitAndBindingLifecycle(t *testing.T) {
	f := newUpperLifecycleFixture(t)
	f.plane.Retirement.Close()
	_, approve := prepareDefinitionFormalApproval(t, f, "adoption-prior")
	approved, err := f.facade.CommitConfirmation(f.ctx, approve)
	if err != nil || approved.State != "COMPLETED" {
		t.Fatalf("prior: %+v %v", approved, err)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := f.facade.TaskExecutionTargetForSession(f.ctx, f.session, f.task)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{AISessionID: f.session, RequestID: "adoption-prepare", Purpose: c.Execution, Action: "START_APPROVED", Execution: &c.ExecutionBody{Action: "START_APPROVED", TaskID: f.task, FeatureAreaID: f.area, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, ExpectedTarget: &expected}, Variants: []c.Variant{{ID: "start", Summary: "Select independent implementation runtime", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: current.RepositoryTargets, PrimaryMemberID: current.PrimaryMemberID, WorkSpec: &current.WorkSpec, VerificationSpec: &current.VerificationSpec}}})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := differentDefinitionRuntime(t, opts, codexruntime.NextRunRuntimeConfig{})
	if len(opts.BackendCandidates) == 0 {
		t.Fatal("no backend")
	}
	backend := opts.BackendCandidates[0]
	ref := formalExecutionPlatform(t, f, backend)
	commit := c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "adoption-commit", ContentHash: prepared.ContentHash, VariantID: "start", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ConfigID, ConfigFingerprint: backend.ConfigFingerprint}}}
	commit.SubmittedHash = c.ValueHash(commit)
	commit.BackendSelection = &backend
	step := c.Step{Kind: "START_IMPLEMENTATION", RequestID: "confirmation_" + c.ValueHash([]string{prepared.IntentID, "START_IMPLEMENTATION"})[:32], State: "UNKNOWN"}
	if _, _, err = sqlite.NewConfirmationStore(f.plane.DB).BeginCommit(f.ctx, commit, []c.Step{step}); err != nil {
		t.Fatal(err)
	}
	store := sqlite.NewWorkflowProgressionStore(f.plane.DB)
	id, err := store.FormalExecutionIdentity(f.ctx, f.session, prepared.IntentID, commit.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	tables := append(formalExecutionTables(), "formal_adoption_business", "formal_adoption_receipts")
	before := definitionFormalSnapshot(t, f, tables)
	if _, err = store.CommitAdoption(f.ctx, id, rs.PlatformReference{}); !errors.Is(err, fe.ErrQualificationRequired) {
		t.Fatalf("fresh zero qualification: %v", err)
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "zero platform read")
	if _, err = f.plane.DB.SQL().Exec(`CREATE TRIGGER adoption_business_fault BEFORE INSERT ON formal_adoption_business BEGIN SELECT RAISE(ABORT,'adoption-business-fault'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CommitAdoption(f.ctx, id, ref); err == nil || !strings.Contains(err.Error(), "adoption-business-fault") {
		t.Fatalf("real business fault: %v", err)
	}
	assertDefinitionFormalSnapshot(t, f, tables, before, "business transaction rollback")
	if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER adoption_business_fault`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.DB.SQL().Exec(`CREATE TRIGGER adoption_final_fault BEFORE INSERT ON formal_adoption_receipts BEGIN SELECT RAISE(ABORT,'adoption-final-fault'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = store.CommitAdoption(f.ctx, id, ref); err == nil || !strings.Contains(err.Error(), "adoption-final-fault") {
		t.Fatalf("real final fault: %v", err)
	}
	var basis, final, sources, runs int
	for table, out := range map[string]*int{"formal_adoption_business": &basis, "formal_adoption_receipts": &final, "runtime_selection_sources": &sources} {
		if err = f.plane.DB.SQL().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(out); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.plane.DB.SQL().QueryRow(`SELECT COUNT(*) FROM runs WHERE purpose='IMPLEMENTATION'`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if basis != 0 || final != 0 || sources != 0 || runs != 0 {
		t.Fatalf("atomic rollback basis=%d final=%d sources=%d implementationRuns=%d", basis, final, sources, runs)
	}
	if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER adoption_final_fault`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.plane.DB.SQL().Exec(`CREATE TRIGGER adoption_source_fault BEFORE INSERT ON runtime_selection_sources BEGIN SELECT RAISE(ABORT,'adoption-source-fault'); END`); err != nil {
		t.Fatal(err)
	}
	prefix := definitionFormalSnapshot(t, f, tables)
	if _, err = store.CommitAdoption(f.ctx, id, ref); err == nil || !strings.Contains(err.Error(), "adoption-source-fault") {
		t.Fatalf("real source fault: %v", err)
	}
	assertDefinitionFormalSnapshot(t, f, tables, prefix, "final receipt and source atomic rollback")
	if _, err = f.plane.DB.SQL().Exec(`DROP TRIGGER adoption_source_fault`); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.CommitAdoption(f.ctx, id, ref)
	if err != nil {
		t.Fatalf("atomic admission: %v", err)
	}
	// Keep the altered Plan internally self-consistent and rehash it. Its
	// scope still must match the independently archived approved definition.
	encoded, _ := json.Marshal(receipt.Archive)
	var changedArchive fe.AdoptionArchive
	if err = json.Unmarshal(encoded, &changedArchive); err != nil {
		t.Fatal(err)
	}
	changedTarget := &changedArchive.Plan.RepositoryTargets[0]
	changedTarget.OriginCommit = strings.Repeat("d", 40)
	changedTarget.Write.Kind = "PATHS"
	changedTarget.Write.Paths = []string{"outside-approved.txt"}
	changedArchive.Plan.WriteScopes[changedTarget.MemberID] = changedTarget.Write
	changedArchive.Plan.ExpectedSourceHEADs[changedTarget.MemberID] = changedTarget.OriginCommit
	changedArchive.Plan.ContentHash, err = changedArchive.Plan.ComputeContentHash()
	if err != nil {
		t.Fatal(err)
	}
	if err = changedArchive.Plan.Validate(); err != nil {
		t.Fatalf("test must present internally valid rehashed Plan: %v", err)
	}
	if _, err = fe.NewAdoptionReceipt(changedArchive); !errors.Is(err, fe.ErrCorrupt) {
		t.Fatalf("self-consistent substituted approved source head accepted: %v", err)
	}
	selections := sqlite.NewRuntimeSelectionStore(f.plane.DB)
	source, err := selections.ReadSource(f.ctx, receipt.SourceID())
	if err != nil {
		t.Fatal(err)
	}
	in := rs.BindingInput{RequestID: "adoption-binding", Owner: source.Owner, SourceID: source.SourceID, SourcePayloadHash: source.SourcePayloadHash, Entry: source.Entry, ExpectedLineageVersion: ref.LineageVersion}
	binding, err := selections.MaterializeBinding(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	platform := sqlite.NewRuntimePlatformStore(f.plane.DB)
	next := rp.QualificationInput{QualificationID: "adoption-new-q", PlatformLineageID: ref.PlatformLineageID, DescriptorID: ref.DescriptorID, CompatibilityEvidenceHash: ref.CompatibilityEvidenceHash, CompatibilityPolicyVersion: ref.CompatibilityPolicyVersion, IssuanceRequestID: "adoption-supersede", Generation: ref.QualificationGeneration + 1, ExpectedQualificationID: ref.QualificationID, ExpectedGeneration: ref.QualificationGeneration, ExpectedLineageVersion: ref.LineageVersion}
	if _, err = platform.Supersede(f.ctx, next); err != nil {
		t.Fatal(err)
	}
	authority, err := selections.ReadCurrent(f.ctx, source.Owner)
	if err != nil || authority.BindingID != binding.BindingID || authority.Platform.QualificationID != ref.QualificationID {
		t.Fatalf("superseded original binding: %+v %v", authority, err)
	}
	lineage, err := platform.GetLineage(f.ctx, ref.PlatformLineageID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = platform.Invalidate(f.ctx, rp.InvalidationInput{QualificationID: ref.QualificationID, Generation: ref.QualificationGeneration, RequestID: "adoption-invalidate", Reason: "test original-generation revocation", ExpectedLineageVersion: lineage.Version}); err != nil {
		t.Fatal(err)
	}
	if _, err = selections.ReadCurrent(f.ctx, source.Owner); !errors.Is(err, rp.ErrQualificationInvalidated) {
		t.Fatalf("invalidated current authority: %v", err)
	}
	after := definitionFormalSnapshot(t, f, tables)
	replay, err := store.CommitAdoption(f.ctx, id, rs.PlatformReference{})
	if err != nil || !reflect.DeepEqual(receipt, replay) {
		t.Fatalf("historical final: %v", err)
	}
	got, err := selections.MaterializeBinding(f.ctx, in)
	if err != nil || !reflect.DeepEqual(binding, got) {
		t.Fatalf("historical binding: %v", err)
	}
	changed := in
	changed.SourcePayloadHash = rs.HashValue("other")
	if _, err = selections.MaterializeBinding(f.ctx, changed); !errors.Is(err, rs.ErrBindingConflict) {
		t.Fatalf("request substitution: %v", err)
	}
	assertDefinitionFormalSnapshot(t, f, tables, after, "historical replay")
}
