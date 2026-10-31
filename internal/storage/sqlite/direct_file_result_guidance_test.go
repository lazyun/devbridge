package sqlite

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// Storage projection unit fixture only: deliberately minimal tables permit
// mismatched histories without weakening production constraints. These rows
// are not evidence of real admission, execution, user approval, or acceptance.
func fileResultGuidanceFixture(t *testing.T) (*WorkflowProgressionStore, *sql.DB, wp.Guidance) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`
 CREATE TABLE runtime_authority(singleton,epoch_id);
 INSERT INTO runtime_authority VALUES(1,'epoch');
 CREATE TABLE tasks(id,project_id,feature_area_id,current_definition_revision_id,current_execution_id,status,phase);
 INSERT INTO tasks VALUES('task','project','area','revision',NULL,'ACTIVE','ACCEPTANCE_PENDING');
 CREATE TABLE task_definition_revisions(id,task_id,payload_json);
 INSERT INTO task_definition_revisions VALUES('revision','task','{"version":2,"workSpec":{"implementationMode":"DIRECT_FILE"},"verificationSpec":{"mode":"NONE","waiver":{"source":"USER_WAIVED","reason":"unit fixture only"}}}');
 CREATE TABLE direct_file_task_authorizations(workflow_id,task_id,task_definition_revision_id,project_id,feature_area_id,ai_session_id,protocol_version,state,runtime_epoch);
 INSERT INTO direct_file_task_authorizations VALUES('workflow','task','revision','project','area','session',2,'CLOSED','epoch');
 CREATE TABLE direct_file_implementation_results(id,workflow_id,task_id,revision_id,project_id,ai_session_id,result_json);
 INSERT INTO direct_file_implementation_results VALUES('result','workflow','task','revision','project','session','{"protocolVersion":2,"verificationStatus":"NOT_RUN"}');
 CREATE TABLE direct_file_implementation_acceptances(result_id);
 CREATE TABLE direct_file_worker_claims(workflow_id);
 CREATE TABLE direct_file_operation_receipts(workflow_id,state);
 `)
	if err != nil {
		t.Fatal(err)
	}
	g := wp.Guidance{ProjectID: "project", FeatureAreaID: "area", TaskID: "task", TaskDefinitionRevisionID: "revision", TaskPhase: task.PhaseAcceptancePending, Generation: "prior-generation"}
	g.Set(string(g.TaskPhase), "READ_TASK_EVIDENCE", "No current Execution.")
	return NewWorkflowProgressionStore(&DB{sql: db}), db, g
}

func TestDirectFileResultGuidanceRequiresExactClosedNoneResult(t *testing.T) {
	for _, tc := range []struct{ name, mutation, action string }{
		{"exact", "", "READ_DIRECT_FILE_RESULT"},
		{"absent-result", "DELETE FROM direct_file_implementation_results", "READ_TASK_EVIDENCE"},
		{"result-other-workflow", "UPDATE direct_file_implementation_results SET workflow_id='other'", "READ_TASK_EVIDENCE"},
		{"result-other-task", "UPDATE direct_file_implementation_results SET task_id='other'", "READ_TASK_EVIDENCE"},
		{"result-other-revision", "UPDATE direct_file_implementation_results SET revision_id='other'", "READ_TASK_EVIDENCE"},
		{"result-other-project", "UPDATE direct_file_implementation_results SET project_id='other'", "READ_TASK_EVIDENCE"},
		{"result-other-session", "UPDATE direct_file_implementation_results SET ai_session_id='other'", "READ_TASK_EVIDENCE"},
		{"current-revision-moved", "UPDATE tasks SET current_definition_revision_id='other'", "READ_TASK_EVIDENCE"},
		{"definition-run", "UPDATE task_definition_revisions SET payload_json=json_set(payload_json,'$.verificationSpec.mode','RUN')", "READ_TASK_EVIDENCE"},
		{"definition-coding", "UPDATE task_definition_revisions SET payload_json=json_set(payload_json,'$.workSpec.implementationMode','CODING_AGENT')", "READ_TASK_EVIDENCE"},
		{"definition-v1", "UPDATE task_definition_revisions SET payload_json=json_set(payload_json,'$.version',1)", "READ_TASK_EVIDENCE"},
		{"waiver-not-user", "UPDATE task_definition_revisions SET payload_json=json_set(payload_json,'$.verificationSpec.waiver.source','LEGACY')", "READ_TASK_EVIDENCE"},
		{"grant-v1", "UPDATE direct_file_task_authorizations SET protocol_version=1", "READ_TASK_EVIDENCE"},
		{"result-v1", "UPDATE direct_file_implementation_results SET result_json=json_set(result_json,'$.protocolVersion',1)", "READ_TASK_EVIDENCE"},
		{"result-has-execution", "UPDATE direct_file_implementation_results SET result_json=json_set(result_json,'$.executionId','execution')", "READ_TASK_EVIDENCE"},
		{"task-has-execution", "UPDATE tasks SET current_execution_id='execution'", "READ_TASK_EVIDENCE"},
		{"result-accepted", "INSERT INTO direct_file_implementation_acceptances VALUES('result')", "READ_TASK_EVIDENCE"},
		{"open-grant", "UPDATE direct_file_task_authorizations SET state='ACTIVE'", "WAIT_FOR_DIRECT_FILE"},
		{"stale-enrolled-grant", "UPDATE direct_file_task_authorizations SET state='RECOVERY_REQUIRED',runtime_epoch='old'; INSERT INTO direct_file_worker_claims VALUES('workflow')", "CANCEL_DIRECT_FILE"},
		{"stale-unresolved-mutation", "UPDATE direct_file_task_authorizations SET state='RECOVERY_REQUIRED',runtime_epoch='old'; INSERT INTO direct_file_worker_claims VALUES('workflow'); INSERT INTO direct_file_operation_receipts VALUES('workflow','UNKNOWN')", "READ_DIRECT_FILE_AUTHORIZATION"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, db, g := fileResultGuidanceFixture(t)
			if tc.mutation != "" {
				if _, err := db.Exec(tc.mutation); err != nil {
					t.Fatal(err)
				}
			}
			var before, after int
			if err := db.QueryRow("SELECT total_changes()").Scan(&before); err != nil {
				t.Fatal(err)
			}
			if err := store.applyDirectFileGuidance(context.Background(), db, &g); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("SELECT total_changes()").Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Fatal("read projection mutated fixture rows")
			}
			if g.NextAction.Code != tc.action || g.DecisionID != "" || g.ExecutionID != "" || g.Approval != nil || len(g.AvailableActions) != 0 || g.TaskPhase != task.PhaseAcceptancePending {
				t.Fatalf("wrong read guidance: %+v want %s", g, tc.action)
			}
			if tc.action == "READ_DIRECT_FILE_RESULT" && (!strings.Contains(g.Message, "file_implementation_result_get") || !strings.Contains(g.Message, "workflow workflow") || g.Generation == "prior-generation") {
				t.Fatalf("missing exact result guidance/generation: %+v", g)
			}
		})
	}
}

func TestDirectFileResultGuidancePreservesBlockingOverlay(t *testing.T) {
	for _, overlay := range []struct{ state, action string }{
		{"RECOVERY_REQUIRED", "RECOVER_AGENT_OPERATION"},
		{"FEATURE_AREA_BUSY", "WAIT_FOR_AGENT_OPERATION"},
		{"GIT_FINALIZATION_PENDING", "RETRY_GIT_FINALIZATION"},
		{"FEATURE_AREA_RETIRING", "WAIT_FOR_RETIREMENT"},
		{"RETIRED", "WORKFLOW_CLOSED"},
	} {
		t.Run(overlay.state, func(t *testing.T) {
			store, db, g := fileResultGuidanceFixture(t)
			// Simulate the already-applied workflowTaskStateTx overlay. The exact
			// matching file result must not replace a higher-priority blocker.
			blockProgressionGuidance(&g, overlay.state, overlay.action, "existing blocker")
			before := g
			if err := store.applyDirectFileGuidance(context.Background(), db, &g); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, g) {
				t.Fatalf("file result replaced blocker: before=%+v after=%+v", before, g)
			}
		})
	}
	for _, action := range []string{"READ_TASK_EVIDENCE", "READ_DIRECT_FILE_RESULT"} {
		if !progressionActionNeedsIdle(action) {
			t.Fatalf("%s bypasses runtime busy suppression", action)
		}
	}
}
