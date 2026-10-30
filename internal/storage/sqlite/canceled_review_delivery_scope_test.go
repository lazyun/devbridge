package sqlite

import (
	"database/sql"
	"testing"
)

// Exercise merge readiness against ambiguous histories without weakening the
// production append-only constraints just to construct invalid test histories.
func TestCanceledReadOnlyReviewDeliveryScopeFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, mutation string
		blocked        bool
	}{
		{"closed-upper", "", false},
		{"canceled-upper", "UPDATE review_grants SET state='CANCELED'", false},
		{"no-review", "DELETE FROM runs; DELETE FROM review_grants; DELETE FROM reviews", false},
		{"active-grant", "UPDATE review_grants SET state='ACTIVE'", true},
		{"missing-grant", "DELETE FROM review_grants", true},
		{"wrong-run-binding", "UPDATE reviews SET linked_run_id='other'", true},
		{"active-run", "UPDATE runs SET status='RUNNING',finished_at=NULL", true},
		{"coding-review", "UPDATE reviews SET executor_kind='CODING_AGENT'", true},
		{"physical-thread", "UPDATE runs SET thread_id='thread'", true},
		{"implementation-run", "UPDATE runs SET purpose='IMPLEMENTATION'", true},
		{"extra-run", "INSERT INTO runs(id,task_id) VALUES('extra','task')", true},
		{"execution", "INSERT INTO execution_proposals VALUES('task')", true},
		{"file-grant", "INSERT INTO direct_file_task_authorizations VALUES('task')", true},
		{"delivery", "INSERT INTO task_commit_receipts VALUES('task')", true},
		{"unknown-cancel", "UPDATE execution_cancellations SET backend_disposition='UNKNOWN_ORPHANED'", true},
		{"unresolved-interrupt", "INSERT INTO unresolved_cancellation_interrupts VALUES('project','cancel')", true},
		{"missing-cancel", "DELETE FROM execution_cancellations", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", ":memory:")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			_, err = db.Exec(`
CREATE TABLE tasks(id,project_id,feature_area_id,status,commit_sha,merged_commit);
INSERT INTO tasks VALUES('task','project','area','CANCELED',NULL,NULL);
CREATE TABLE reviews(id,task_id,executor_kind,linked_run_id);
INSERT INTO reviews VALUES('review','task','UPPER_LLM','run');
CREATE TABLE review_grants(review_id,project_id,feature_area_id,state,run_id);
INSERT INTO review_grants VALUES('review','project','area','CLOSED','run');
CREATE TABLE runs(id,task_id,review_id,purpose,status,finished_at,thread_id,turn_id,execution_proposal_id);
INSERT INTO runs VALUES('run','task','review','SCOPE_REVIEW','SUCCEEDED','finished',NULL,NULL,NULL);
CREATE TABLE execution_cancellations(project_id,request_id,task_id,owner_type,owner_id,state,run_id,backend_disposition,interrupt_state,resolved_at);
INSERT INTO execution_cancellations VALUES('project','cancel','task','TASK','task','CANCELED',NULL,'NO_BACKEND','NOT_SENT','resolved');
CREATE TABLE unresolved_cancellation_interrupts(project_id,request_id);
CREATE TABLE execution_proposals(task_id);
CREATE TABLE direct_file_task_authorizations(task_id);
CREATE TABLE task_commit_receipts(task_id);`)
			if err != nil {
				t.Fatal(err)
			}
			if tc.mutation != "" {
				if _, err = db.Exec(tc.mutation); err != nil {
					t.Fatal(err)
				}
			}
			var blocked bool
			if err = db.QueryRow(`SELECT ` + deliveryRelevantTaskSQL + ` FROM tasks t WHERE id='task'`).Scan(&blocked); err != nil || blocked != tc.blocked {
				t.Fatalf("blocked=%t want=%t err=%v", blocked, tc.blocked, err)
			}
		})
	}
}
