package controlapi_test

import (
	"encoding/json"
	"github.com/lazyun/devbridge/internal/agentconversation"
	"github.com/lazyun/devbridge/internal/controlplane"
	"testing"
)

// Real Local MCP/Core/SQLite with typed isolated Git and UPPER report bootstrap;
// no native agent or model is invoked.
func TestUpperRecoveryReplayDoesNotBorrowRevisedReview(t *testing.T) {
	f := newUpperLifecycleFixtureScoped(t, "data-instance-local")
	old := f.access.Target
	reopenRecoveryFixture(t, f, controlplane.Config{DataRoot: f.dataRoot})
	client := newApprovedStartLocalClient(t, f)
	command := map[string]any{"aiSessionId": f.session, "featureAreaId": f.area, "operationKind": "REVIEW", "operationId": f.review, "runId": f.run, "requestId": "recover-before-revise", "expectedUpperVersion": map[string]any{"authorityEpoch": f.plane.DB.RuntimeEpoch(), "grantEpoch": old.RuntimeEpoch, "grantGeneration": old.GrantGeneration}}
	read := func() map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal(client.call("agent_operation_recover", command, false), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	original := read()
	result := original["result"].(map[string]any)
	originalContext := original["context"].(map[string]any)
	if original["refreshRequired"] == true || originalContext["reviewId"] != f.review || originalContext["runId"] != f.run || original["workflow"].(map[string]any)["nextAction"] == nil {
		t.Fatalf("current recovery lost matching guidance: %+v", original)
	}
	var version agentconversation.UpperVersion
	b, _ := json.Marshal(result["authorizedUpperVersion"])
	if err := json.Unmarshal(b, &version); err != nil {
		t.Fatal(err)
	}
	if result["backendDisposition"] != "UPPER_REAUTHORIZED" || version.GrantEpoch == "" {
		t.Fatalf("not reauthorized: %+v", original)
	}
	target := old
	target.RuntimeEpoch = version.GrantEpoch
	target.GrantGeneration = version.GrantGeneration
	var err error
	f.access, err = f.facade.PrepareUpperReview(f.ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	submitLifecycleApprovalReady(t, f, "report-after-recovery")
	_, commit := prepareLifecycleReviewDecision(t, f, "REVISE", "revise-recovered-review")
	if completed, err := f.facade.CommitConfirmation(f.ctx, commit); err != nil || completed.State != "COMPLETED" {
		t.Fatalf("REVISE: %+v %v", completed, err)
	}
	current, err := f.plane.Tasks.Get(f.ctx, f.task)
	if err != nil || current.CurrentReviewID == f.review || current.CurrentReviewID == "" {
		t.Fatalf("no successor: %+v %v", current, err)
	}
	// Join the exact authorized successor's UPPER preparation under the
	// coordinator's per-Run lock. This is environment-only, never a model call;
	// waiting merely for PREPARING would race legitimate grant/Run writes.
	successorAccess, err := f.facade.UpperReviewAccessForSession(f.ctx, f.session, current.CurrentReviewID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.facade.PrepareUpperReview(f.ctx, successorAccess.Target)
	if err != nil || prepared.State != "ACTIVE" {
		t.Fatalf("successor preparation: %+v %v", prepared, err)
	}
	originalResult, _ := json.Marshal(result)
	tables := []string{"tasks", "reviews", "runs", "agent_operation_recoveries", "agent_operation_recovery_states", "review_grants", "upper_review_worker_claims", "task_definition_revisions", "execution_proposals"}
	for _, cold := range []bool{false, true} {
		if cold {
			client.close()
			reopenRecoveryFixture(t, f, controlplane.Config{DataRoot: f.dataRoot})
			client = newApprovedStartLocalClient(t, f)
		}
		before := definitionFormalSnapshot(t, f, tables)
		replay := read()
		b, _ := json.Marshal(replay)
		t.Logf("cold=%v response=%s", cold, b)
		replayResult, _ := json.Marshal(replay["result"])
		if string(replayResult) != string(originalResult) {
			t.Errorf("cold=%v durable receipt changed", cold)
		}
		context := replay["context"].(map[string]any)
		if context["reviewId"] != f.review || context["runId"] != f.run {
			t.Errorf("cold=%v historical recovery borrowed identity: %+v", cold, context)
		}
		workflow := replay["workflow"].(map[string]any)
		if replay["refreshRequired"] != true || workflow["nextAction"] != nil || workflow["nextStep"] != nil {
			t.Errorf("cold=%v historical recovery borrowed workflow: %+v", cold, workflow)
		}
		changed := map[string]any{}
		for key, value := range command {
			changed[key] = value
		}
		changed["operationId"] = current.CurrentReviewID
		client.call("agent_operation_recover", changed, true)
		assertDefinitionFormalSnapshot(t, f, tables, before, "historical UPPER recovery replay and changed payload refusal")
		var implementations int
		if err := f.plane.DB.SQL().QueryRow(`SELECT count(*) FROM runs WHERE purpose='IMPLEMENTATION'`).Scan(&implementations); err != nil || implementations != 0 {
			t.Fatalf("unexpected implementation runtime: %d %v", implementations, err)
		}
	}
}
