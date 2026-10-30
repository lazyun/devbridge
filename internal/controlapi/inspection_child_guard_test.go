package controlapi_test

import (
	"context"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/recoverystatus"
	"github.com/lazyun/devbridge/internal/storage/sqlite"
)

// Exercise the SQL boundary at the real native admission checkpoint. Every
// candidate is rolled back before the ordinary production preparer proceeds.
type inspectionChildGuardStore struct {
	*sqlite.RecoveryStatusStore
	t  *testing.T
	db *sqlite.DB
}

func (s *inspectionChildGuardStore) AdmitRecoveryChild(ctx context.Context, id, request, payload string, admission recoverystatus.Admission, now time.Time) (string, error) {
	plan, err := s.RecoveryStatusStore.AdmitRecoveryChild(ctx, id, request, payload, admission, now)
	if err != nil {
		return plan, err
	}
	for _, candidate := range []struct {
		name, runID, operation, task, epoch, workspace, thread string
		allowed                                                bool
	}{
		{name: "exact independent child", allowed: true},
		{name: "unlinked child", runID: "'unlinked-child'"},
		{name: "foreign operation", operation: "'foreign-inspection'"},
		{name: "foreign task", task: "'foreign-task'"},
		{name: "wrong epoch", epoch: "'foreign-epoch'"},
		{name: "inherited workspace", workspace: "source.workspace_id"},
		{name: "inherited thread", thread: "source.thread_id"},
	} {
		func() {
			tx, e := s.db.SQL().BeginTx(ctx, nil)
			if e != nil {
				s.t.Fatal(e)
			}
			defer tx.Rollback()
			const child = "v110-guard-candidate"
			if _, e = tx.ExecContext(ctx, `UPDATE recovery_status_records SET recovery_run_id=? WHERE id=? AND state='PREPARED' AND recovery_run_id IS NULL`, child, id); e != nil {
				s.t.Fatal(e)
			}
			value := func(v, fallback string) string {
				if v == "" {
					return fallback
				}
				return v
			}
			query := `INSERT INTO runs(id,project_id,feature_area_id,operation_kind,operation_id,task_id,attempt,status,created_at,updated_at,purpose,codex_instruction,runtime_epoch,agent_execution_id,workspace_id,thread_id)
SELECT ` + value(candidate.runID, "'"+child+"'") + `,source.project_id,NULL,'INSPECTION',` + value(candidate.operation, "source.operation_id") + `,` + value(candidate.task, "source.task_id") + `,(SELECT COALESCE(MAX(attempt),0)+1 FROM runs WHERE task_id=source.task_id),'CREATED',source.created_at,source.updated_at,'RECOVERY_STATUS','diagnose',` + value(candidate.epoch, "x.runtime_epoch") + `,source.agent_execution_id,` + value(candidate.workspace, "NULL") + `,` + value(candidate.thread, "NULL") + ` FROM recovery_status_records x JOIN runs source ON source.id=x.source_run_id WHERE x.id=?`
			_, e = tx.ExecContext(ctx, query, id)
			if candidate.allowed && e != nil {
				s.t.Fatalf("v110 exact native child rejected: %v", e)
			}
			if !candidate.allowed && e == nil {
				s.t.Fatalf("v110 accepted %s", candidate.name)
			}
		}()
	}
	return plan, nil
}
