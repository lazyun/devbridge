package controlapi_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/codexruntime"
	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/formalexecution"
	"github.com/lazyun/devbridge/internal/runtimeplatform"
	"github.com/lazyun/devbridge/internal/runtimeselection"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// This enters the public confirmation facade with a real evidence-backed v2
// Review and an explicitly collected installed native qualification. No SQL
// creates successful business state or supplies a compatibility assertion.
func TestFormalExecutionNativeApprovalReady(t *testing.T) {
	native := os.Getenv("DEVBRIDGE_PLATFORM_NATIVE_BINARY")
	if native == "" {
		t.Skip("explicit installed native binary required; loopback provider only")
	}
	root, err := filepath.Abs("../../build/remaining-constraints-2026-10-02/formal-native")
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
	t.Log("formal native evidence:", root)
	t.Setenv("TMPDIR", root)
	f := newUpperLifecycleFixture(t)
	f.plane.Retirement.Close()
	targets, work, verification, _ := submitLifecycleApprovalReady(t, f, "formal-native-review-result")
	parent, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil || parent.CurrentDefinitionRevisionID == "" {
		t.Fatalf("real authored parent missing: %+v %v", parent, err)
	}
	backend, err := f.plane.CodexConfigs.Create(f.ctx, codexconfig.CreateInput{Name: "Installed native exact qualification", CodexPath: native, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	f.plane.RuntimeQualification.EvidenceRoot = filepath.Join(root, "platform-evidence")
	qualified, err := f.plane.RuntimeQualification.QualifyBackend(f.ctx, backend.ID, "IMPLEMENTATION", "formal-native-qualification")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.facade.PrepareConfirmation(f.ctx, c.PrepareInput{
		AISessionID: f.session, RequestID: "formal-native-prepare", Purpose: c.Execution, Action: "APPROVE_AND_START",
		Execution: &c.ExecutionBody{Action: "APPROVE_AND_START", TaskID: f.task, FeatureAreaID: f.area, ReviewID: f.review, DecisionID: wp.DecisionID(f.review), TaskDefinitionRevisionID: parent.CurrentDefinitionRevisionID, ExecutionID: parent.CurrentExecutionID},
		Variants:  []c.Variant{{ID: "approve", Summary: "Approve measured native runtime and prepare the exact definition", ImplementationMode: task.ImplementationCodingAgent, RepositoryTargets: targets, PrimaryMemberID: targets[0].MemberID, WorkSpec: &work, VerificationSpec: &verification}},
	})
	if err != nil {
		t.Fatal(err)
	}
	options, err := f.facade.ConfirmationOptions(f.ctx, c.OptionsInput{ReadInput: c.ReadInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, ContentHash: prepared.ContentHash}})
	if err != nil || len(options.RuntimeOptions) == 0 {
		t.Fatalf("runtime options: %+v %v", options, err)
	}
	runtime := codexruntime.NextRunRuntimeConfig{Model: options.RuntimeOptions[0].ModelID, ReasoningEffort: options.RuntimeOptions[0].SupportedReasoningEfforts[0]}
	input := c.CommitInput{AISessionID: f.session, IntentID: prepared.IntentID, Version: prepared.Version, Purpose: prepared.Purpose, Action: prepared.Action, RequestID: "formal-native-commit", ContentHash: prepared.ContentHash, VariantID: "approve", Selection: c.Selection{RuntimeConfig: &runtime, Backend: &codexconfig.BackendSelectionInput{ConfigID: backend.ID, ConfigFingerprint: backend.Fingerprint()}}}
	beforeRuntime := definitionFormalSnapshot(t, f, []string{"runs", "runtime_preparation_attempts", "runtime_workset_bindings", "runtime_start_authorities", "runtime_handoffs"})
	completed, err := f.facade.CommitConfirmation(f.ctx, input)
	if err != nil || completed.State != "COMPLETED" {
		t.Fatalf("public formal commit: state=%s steps=%+v error=%v", completed.State, completed.Steps, err)
	}
	if len(completed.Steps) != 1 || completed.Steps[0].Kind != "APPROVE" || completed.Steps[0].Effect != "PREPARATION_READY" || completed.Steps[0].RunID != "" {
		t.Fatalf("formal commit dispatched runtime: %+v", completed.Steps)
	}
	current, err := f.facade.GetTask(f.ctx, f.task)
	if err != nil || current.Phase != task.PhaseImplementationReady || current.CurrentDefinitionRevisionID == parent.CurrentDefinitionRevisionID || current.CurrentExecutionID == "" {
		t.Fatalf("formal ready: %+v %v", current, err)
	}
	sourceID := definitionFormalIdentity(t, f, prepared, input.RequestID)
	identity := formalexecution.ReadIdentity{AISessionID: sourceID.AISessionID, IntentID: sourceID.IntentID, IntentVersion: sourceID.IntentVersion, RequestID: sourceID.RequestID, ContentHash: sourceID.ContentHash, TypedPayloadHash: sourceID.TypedPayloadHash, SubmittedHash: sourceID.SubmittedHash}
	receipt, err := f.plane.WorkflowProgression.ReadApproveAndStart(f.ctx, identity)
	if err != nil || receipt.Validate() != nil || receipt.T2.ExecutionID != current.CurrentExecutionID || receipt.Archive.PlatformReference != qualified.Platform {
		t.Fatalf("T1/T2/T3 receipt: %+v %v", receipt, err)
	}
	owner := runtimeselection.Owner{ProjectID: f.project, TaskID: f.task, TaskDefinitionRevisionID: current.CurrentDefinitionRevisionID, RuntimeRole: "IMPLEMENTATION"}
	authority, err := sqlite.NewRuntimeSelectionStore(f.plane.DB).ReadCurrent(f.ctx, owner)
	if err != nil || authority.Platform != qualified.Platform || authority.Values.BackendIdentity != backend.ID || authority.Values.Model != runtime.Model {
		t.Fatalf("exact binding: %+v %v", authority, err)
	}
	boundSource, err := sqlite.NewRuntimeSelectionStore(f.plane.DB).ReadSource(f.ctx, receipt.T1.Identity.SourceOriginID)
	if err != nil {
		t.Fatal(err)
	}
	bindingInput := runtimeselection.BindingInput{RequestID: "runtime_binding_" + boundSource.SourceID, Owner: boundSource.Owner, SourceID: boundSource.SourceID, SourcePayloadHash: boundSource.SourcePayloadHash, Entry: boundSource.Entry, ExpectedLineageVersion: boundSource.Platform.LineageVersion}
	originalBinding, err := sqlite.NewRuntimeSelectionStore(f.plane.DB).ReplayBinding(f.ctx, bindingInput)
	if err != nil || originalBinding.BindingID != authority.BindingID {
		t.Fatalf("binding immutable receipt: %+v %v", originalBinding, err)
	}
	execution, err := sqlite.NewExecutionProposalStore(f.plane.DB).GetByID(f.ctx, current.CurrentExecutionID)
	if err != nil || execution.Status != "ADMITTED" || execution.RunID != "" {
		t.Fatalf("no-run admitted owner: %+v %v", execution, err)
	}
	if err = f.plane.RuntimeQualification.VerifyExecution(f.ctx, execution); err != nil {
		t.Fatalf("qualified exact execution failed verification: %v", err)
	}
	wrong := execution
	wrong.TaskDefinitionRevisionID = parent.CurrentDefinitionRevisionID
	if err = f.plane.RuntimeQualification.VerifyExecution(f.ctx, wrong); err == nil {
		t.Fatal("execution without exact revision binding accepted")
	}
	if got := definitionFormalSnapshot(t, f, []string{"runs", "runtime_preparation_attempts", "runtime_workset_bindings", "runtime_start_authorities", "runtime_handoffs"}); got != beforeRuntime {
		t.Fatal("approval created a Run, preparation attempt, workset binding, runtime start or handoff")
	}
	tables := []string{"tasks", "task_definition_revisions", "execution_proposals", "execution_proposal_admissions", "formal_execution_t1", "formal_execution_t2", "formal_execution_receipts", "runtime_scope_plans", "runtime_preparation_authorities", "runtime_selection_sources", "runtime_selection_bindings", "runs", "runtime_handoffs"}
	baseline := definitionFormalSnapshot(t, f, tables)
	if replay, e := f.facade.CommitConfirmation(f.ctx, input); e != nil || replay.State != "COMPLETED" {
		t.Fatalf("same-process replay: %+v %v", replay, e)
	}
	if got := definitionFormalSnapshot(t, f, tables); got != baseline {
		t.Fatal("completed approval replay changed durable business facts")
	}
	if err = f.plane.Close(); err != nil {
		t.Fatal(err)
	}
	f.plane = nil
	f.plane, err = controlplane.Open(f.ctx, controlplane.Config{DataRoot: f.dataRoot})
	if err != nil {
		t.Fatal(err)
	}
	f.plane.Retirement.Close()
	f.facade, err = controlapi.NewFromControlPlane(f.plane)
	if err != nil {
		t.Fatal(err)
	}
	baseline = definitionFormalSnapshot(t, f, tables)
	cold, err := f.plane.WorkflowProgression.ReadApproveAndStart(f.ctx, identity)
	if err != nil || !reflect.DeepEqual(cold, receipt) {
		t.Fatalf("cold immutable receipt changed: %v", err)
	}
	if replay, e := f.facade.CommitConfirmation(f.ctx, input); e != nil || replay.State != "COMPLETED" {
		t.Fatalf("cold public replay: %+v %v", replay, e)
	}
	if got := definitionFormalSnapshot(t, f, tables); got != baseline {
		t.Fatal("cold completed approval replay wrote business facts")
	}
	raw, _ := json.MarshalIndent(receipt, "", "  ")
	if err = os.WriteFile(filepath.Join(root, "formal-receipt.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("native qualification=%s descriptor=%s T1=%s T2=%s T3=%s revision=%s execution=%s binding=%s; zero implementation Runs", qualified.Platform.QualificationID, qualified.Platform.DescriptorID, receipt.T1.Transaction, receipt.T2.Transaction, formalexecution.T3Transaction, current.CurrentDefinitionRevisionID, current.CurrentExecutionID, authority.BindingID)
	runFormalNativeImplementation(t, f, native, root, runtime, backend)
	// Lifecycle changes do not rewrite the already-issued binding or formal
	// receipt. Superseded remains readable; invalidated denies live authority.
	platformStore := sqlite.NewRuntimePlatformStore(f.plane.DB)
	q := receipt.Archive.Qualification
	lineage, e := platformStore.GetLineage(f.ctx, q.PlatformLineageID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = platformStore.Supersede(f.ctx, runtimeplatform.QualificationInput{QualificationID: "later-native-qualification", PlatformLineageID: q.PlatformLineageID, DescriptorID: q.DescriptorID, CompatibilityPolicyVersion: q.CompatibilityPolicyVersion, CompatibilityEvidenceHash: q.CompatibilityEvidenceHash, IssuanceRequestID: "later-native-request", Generation: q.Generation + 1, ExpectedLineageVersion: lineage.Version, ExpectedQualificationID: q.QualificationID, ExpectedGeneration: q.Generation}); e != nil {
		t.Fatal(e)
	}
	selectionStore := sqlite.NewRuntimeSelectionStore(f.plane.DB)
	if prior, e := selectionStore.ReadCurrent(f.ctx, owner); e != nil || !reflect.DeepEqual(prior, authority) {
		t.Fatalf("supersede changed exact binding: %+v %v", prior, e)
	}
	// A new explicit backend mapping authorizes future bindings only. The
	// current Task revision remains bound to q1, including on the launch path.
	renewed, e := f.plane.RuntimeQualification.QualifyBackend(f.ctx, backend.ID, "IMPLEMENTATION", "formal-native-explicit-renewal")
	if e != nil || renewed.Platform.QualificationID != "later-native-qualification" {
		t.Fatalf("explicit backend renewal: %+v %v", renewed, e)
	}
	if prior, e := selectionStore.ReadCurrent(f.ctx, owner); e != nil || !reflect.DeepEqual(prior, authority) {
		t.Fatalf("backend renewal rewrote original revision binding: %+v %v", prior, e)
	}
	if original, e := platformStore.ReadBackendMappingForRequest(f.ctx, qualified.RequestID); e != nil || original != qualified {
		t.Fatalf("backend renewal rewrote original mapping receipt: %+v %v", original, e)
	}
	if e = f.plane.RuntimeQualification.VerifyExecution(f.ctx, execution); e != nil {
		t.Fatalf("superseded exact binding lost authority after backend renewal: %v", e)
	}
	if launch, e := f.plane.RuntimeQualification.VerifiedExecutionLaunch(f.ctx, execution); e != nil {
		t.Fatalf("original binding cannot resolve exact native launch: %v", e)
	} else if path, e := launch.Executable(f.ctx); e != nil || path != native {
		t.Fatalf("original binding launch=%q error=%v", path, e)
	}
	lineage, e = platformStore.GetLineage(f.ctx, q.PlatformLineageID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = platformStore.Invalidate(f.ctx, runtimeplatform.InvalidationInput{QualificationID: q.QualificationID, Generation: q.Generation, ExpectedLineageVersion: lineage.Version, RequestID: "invalidate-original-native", Reason: "explicit native qualification revocation"}); e != nil {
		t.Fatal(e)
	}
	if _, e = selectionStore.ReadCurrent(f.ctx, owner); !errors.Is(e, runtimeplatform.ErrQualificationInvalidated) {
		t.Fatalf("invalidated binding remains current: %v", e)
	}
	baseline = definitionFormalSnapshot(t, f, tables)
	if historical, e := selectionStore.ReplayBinding(f.ctx, bindingInput); e != nil || !reflect.DeepEqual(historical, originalBinding) {
		t.Fatalf("immutable binding receipt blocked by lifecycle: %+v %v", historical, e)
	}
	if historical, e := f.plane.WorkflowProgression.ReadApproveAndStart(f.ctx, identity); e != nil || !reflect.DeepEqual(historical, receipt) {
		t.Fatalf("immutable formal receipt blocked by lifecycle: %v", e)
	}
	if replay, e := f.facade.CommitConfirmation(f.ctx, input); e != nil || replay.State != "COMPLETED" {
		t.Fatalf("completed formal replay consulted revoked live authority: %v", e)
	}
	if definitionFormalSnapshot(t, f, tables) != baseline {
		t.Fatal("historical revoked qualification replay wrote business facts")
	}
}

func TestFormalExecutionNativeDefinitionOnlyAdoption(t *testing.T) {
	native := os.Getenv("DEVBRIDGE_PLATFORM_NATIVE_BINARY")
	if native == "" {
		t.Skip("explicit installed native binary required; loopback provider only")
	}
	root, e := filepath.Abs("../../build/remaining-constraints-2026-10-02/formal-native")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(root, 0700); e != nil {
		t.Fatal(e)
	}
	root, e = os.MkdirTemp(root, "adoption-")
	if e != nil {
		t.Fatal(e)
	}
	t.Setenv("TMPDIR", root)
	t.Log("native definition-only adoption:", root)
	f := newUpperLifecycleFixture(t)
	f.plane.Retirement.Close()
	prepared, commit := prepareDefinitionFormalApproval(t, f, "native-definition-only")
	completed, e := f.facade.CommitConfirmation(f.ctx, commit)
	if e != nil || completed.State != "COMPLETED" {
		t.Fatalf("NO_START approval: %+v %v", completed, e)
	}
	priorID := definitionFormalIdentity(t, f, prepared, commit.RequestID)
	prior, e := f.plane.Confirmations.ReadDefinitionApproval(f.ctx, priorID)
	if e != nil || prior.Validate() != nil {
		t.Fatalf("NO_START real formal archive: %+v %v", prior, e)
	}
	current, e := f.facade.GetTask(f.ctx, f.task)
	if e != nil || current.CurrentExecutionID != "" || current.Phase != task.PhaseImplementationReady {
		t.Fatalf("definition-only task: %+v %v", current, e)
	}
	backend, e := f.plane.CodexConfigs.Create(f.ctx, codexconfig.CreateInput{Name: "Native adoption backend", CodexPath: native, Enabled: true})
	if e != nil {
		t.Fatal(e)
	}
	f.plane.RuntimeQualification.EvidenceRoot = filepath.Join(root, "platform-evidence")
	if _, e = f.plane.RuntimeQualification.ResolvePlatformReference(f.ctx, backend.Selection("SESSION_INVENTORY"), "IMPLEMENTATION"); !errors.Is(e, runtimeplatform.ErrQualificationMissing) {
		t.Fatalf("unqualified backend gained authority: %v", e)
	}
	qualified, e := f.plane.RuntimeQualification.QualifyBackend(f.ctx, backend.ID, "IMPLEMENTATION", "native-adoption-qualification")
	if e != nil {
		t.Fatal(e)
	}
	_, options := definitionOnlyStart(t, f, current)
	if len(options.RuntimeOptions) == 0 {
		t.Fatal("no canonical runtime choices")
	}
	runtime := codexruntime.NextRunRuntimeConfig{Model: options.RuntimeOptions[0].ModelID, ReasoningEffort: options.RuntimeOptions[0].SupportedReasoningEfforts[0]}
	started, startInput := runFormalNativeImplementation(t, f, native, root, runtime, backend)
	var intentID string
	if e = f.plane.DB.SQL().QueryRowContext(f.ctx, `SELECT intent_id FROM formal_adoption_business WHERE execution_id=?`, started.Proposal.ID).Scan(&intentID); e != nil {
		t.Fatal(e)
	}
	intent, e := f.plane.Confirmations.Get(f.ctx, f.session, intentID)
	if e != nil || intent.Commit == nil {
		t.Fatalf("adoption normalized commit: %v", e)
	}
	sid := definitionFormalIdentity(t, f, intent, intent.Commit.RequestID)
	id := formalexecution.ReadIdentity{AISessionID: sid.AISessionID, IntentID: sid.IntentID, IntentVersion: sid.IntentVersion, RequestID: sid.RequestID, ContentHash: sid.ContentHash, TypedPayloadHash: sid.TypedPayloadHash, SubmittedHash: sid.SubmittedHash}
	receipt, e := f.plane.WorkflowProgression.ReadAdoption(f.ctx, id)
	if e != nil || receipt.Validate() != nil || receipt.Archive.PlatformReference != qualified.Platform || !reflect.DeepEqual(receipt.Archive.PriorApproval, prior) || receipt.Archive.RevisionID != current.CurrentDefinitionRevisionID {
		t.Fatalf("exact adoption archive: %+v %v", receipt, e)
	}
	if e = f.plane.Close(); e != nil {
		t.Fatal(e)
	}
	f.plane = nil
	f.plane, e = controlplane.Open(f.ctx, controlplane.Config{DataRoot: f.dataRoot})
	if e != nil {
		t.Fatal(e)
	}
	f.plane.Retirement.Close()
	f.facade, e = controlapi.NewFromControlPlane(f.plane)
	if e != nil {
		t.Fatal(e)
	}
	tables := append(definitionFormalAllTables(), "formal_adoption_business", "formal_adoption_receipts", "runtime_selection_sources", "runtime_selection_bindings")
	baseline := definitionFormalSnapshot(t, f, tables)
	if cold, e := f.plane.WorkflowProgression.ReadAdoption(f.ctx, id); e != nil || !reflect.DeepEqual(cold, receipt) {
		t.Fatalf("cold adoption archive: %v", e)
	}
	if cold, e := f.facade.StartApprovedTaskExecution(f.ctx, startInput); e != nil || cold.Run.ID != started.Run.ID || cold.Proposal.ID != started.Proposal.ID {
		t.Fatalf("cold public adoption replay: %+v %v", cold, e)
	}
	if definitionFormalSnapshot(t, f, tables) != baseline {
		t.Fatal("cold adoption replay wrote business facts")
	}
	raw, _ := json.MarshalIndent(receipt, "", "  ")
	if e = os.WriteFile(filepath.Join(root, "adoption-receipt.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
}

// The local provider supplies deterministic instructions, while the real
// installed native executable executes commands and produces authoritative
// command items. It does not supply qualification or a forged App Server.
func runFormalNativeImplementation(t *testing.T, f *upperLifecycleFixture, native, root string, runtime codexruntime.NextRunRuntimeConfig, backend codexconfig.Config) (executionproposal.StartResult, controlapi.ApprovedTaskStartInput) {
	t.Helper()
	expected, err := f.facade.TaskExecutionTargetForSession(f.ctx, f.session, f.task)
	if err != nil {
		t.Fatal(err)
	}
	startInput := controlapi.ApprovedTaskStartInput{AISessionID: f.session, TaskID: f.task, RequestID: "formal-native-explicit-start", ExpectedTarget: expected, RuntimeConfig: runtime, Backend: backend.Selection("SESSION_INVENTORY").Input()}
	started := runQualifiedNativeImplementation(t, f, root, runtime, func() (executionproposal.StartResult, error) {
		return f.facade.StartApprovedTaskExecution(f.ctx, startInput)
	})
	t.Log("public explicit START_APPROVED joined real native Implementation+Verification")
	return started, startInput
}

// Both ordinary formal starts and Recovery-purpose starts use this real native
// provider. The supplied callback must enter the appropriate public command;
// it is invoked again after completion to verify exact, non-dispatching replay.
func runQualifiedNativeImplementation(t *testing.T, f *upperLifecycleFixture, root string, runtime codexruntime.NextRunRuntimeConfig, start func() (executionproposal.StartResult, error)) executionproposal.StartResult {
	t.Helper()
	var calls atomic.Int64
	var roots []string
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusUpgradeRequired)
			return
		}
		defer r.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		n := calls.Add(1)
		if err = os.WriteFile(filepath.Join(root, fmt.Sprintf("native-provider-request-%d.json", n)), raw, 0600); err != nil {
			t.Error(err)
			http.Error(w, "persist request", 500)
			return
		}
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil || body["model"] != runtime.Model {
			t.Errorf("native changed selected runtime: %s", raw)
			http.Error(w, "runtime changed", 400)
			return
		}
		if n == 1 {
			rows, e := f.plane.DB.SQL().QueryContext(f.ctx, `SELECT DISTINCT w.path FROM run_repository_bindings b JOIN runs r ON r.id=b.run_id JOIN worktrees w ON w.id=b.worktree_id WHERE r.task_id=? AND r.purpose='IMPLEMENTATION' AND b.preparation='READY' AND w.status='READY' ORDER BY w.path`, f.task)
			if e != nil {
				t.Error(e)
				http.Error(w, "read roots", 500)
				return
			}
			for rows.Next() {
				var path string
				if e = rows.Scan(&path); e != nil {
					t.Error(e)
					break
				}
				roots = append(roots, path)
			}
			_ = rows.Close()
			if len(roots) != 2 {
				t.Errorf("implementation exact roots=%v", roots)
				http.Error(w, "roots", 500)
				return
			}
		}
		if len(roots) != 2 {
			http.Error(w, "exact roots unavailable", 500)
			return
		}
		var item map[string]any
		if n == 1 || n == 3 || n == 4 {
			command, dir := "test -f same.txt", roots[0]
			if n == 1 {
				command = "printf 'native app contract\\n' > " + quote(filepath.Join(roots[0], "same.txt")) + " && printf 'native sdk contract\\n' > " + quote(filepath.Join(roots[1], "same.txt"))
			}
			if n == 4 {
				dir = roots[1]
			}
			args, _ := json.Marshal(map[string]any{"cmd": command, "workdir": dir, "max_output_tokens": 1000})
			item = map[string]any{"type": "function_call", "call_id": fmt.Sprintf("native-formal-%d", n), "name": "exec_command", "arguments": string(args)}
		} else if n == 2 || n == 5 {
			message := `{"resultDisposition":"COMPLETED","summary":"Updated both exact native member contracts"}`
			if n == 5 {
				message = `{"version":2,"summary":"Both exact commands passed","checks":[{"index":0,"status":"PASSED","summary":"first member exists","evidence":"test -f same.txt exited 0","reason":""},{"index":1,"status":"PASSED","summary":"second member exists","evidence":"test -f same.txt exited 0","reason":""}],"acceptanceCriteria":[{"index":0,"status":"PASSED","summary":"both contracts present","evidence":"two native commands","reason":""}]}`
			}
			item = map[string]any{"id": fmt.Sprintf("native-formal-result-%d", n), "type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": message}}}
		} else {
			t.Errorf("unexpected provider call %d", n)
			http.Error(w, "extra call", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		id := fmt.Sprintf("native-formal-response-%d", n)
		for _, event := range []map[string]any{{"type": "response.created", "response": map[string]any{"id": id}}, {"type": "response.output_item.done", "item": item}, {"type": "response.completed", "response": map[string]any{"id": id, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}}} {
			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
		}
	}))
	t.Cleanup(provider.Close)
	home := filepath.Join(root, "runtime-home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("openai_base_url=%q\nmodel_provider=\"formal_offline\"\n[model_providers.formal_offline]\nname=\"Formal native loopback\"\nbase_url=%q\nwire_api=\"responses\"\nrequires_openai_auth=false\nrequest_max_retries=0\nstream_max_retries=0\n[features]\nresponses_websockets=false\nresponses_websockets_v2=false\nplugins=false\nremote_plugin=false\n[analytics]\nenabled=false\n[feedback]\nenabled=false\n", provider.URL+"/v1", provider.URL+"/v1")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"CODEX_HOME": home, "HOME": home, "HTTP_PROXY": "", "HTTPS_PROXY": "", "ALL_PROXY": "", "OPENAI_BASE_URL": provider.URL + "/v1", "OPENAI_API_KEY": "", "CODEX_API_KEY": "", "OTEL_SDK_DISABLED": "true"} {
		t.Setenv(k, v)
	}
	started, err := start()
	if err != nil || started.Run.ID == "" {
		t.Fatalf("public qualified start: %+v %v", started, err)
	}
	waitDefinitionRuntimeSession(t, f, started.Run.ID, started.Proposal.ID)
	if calls.Load() != 5 {
		t.Fatalf("native provider calls=%d want=5", calls.Load())
	}
	for _, path := range roots {
		raw, e := os.ReadFile(filepath.Join(path, "same.txt"))
		if e != nil || !strings.HasPrefix(string(raw), "native ") {
			t.Fatalf("native command did not write exact member: %s %v", raw, e)
		}
	}
	baseline := definitionRuntimeSnapshot(t, f, true)
	if replay, e := start(); e != nil || replay.Run.ID != started.Run.ID {
		t.Fatalf("explicit start replay: %+v %v", replay, e)
	}
	if definitionRuntimeSnapshot(t, f, true) != baseline || calls.Load() != 5 {
		t.Fatal("completed explicit start replay redispatched or wrote durable facts")
	}
	t.Logf("real native Implementation+Verification; provider calls=%d", calls.Load())
	return started
}
