package controlapi_test

import (
	"encoding/json"
	"testing"

	"github.com/lazyun/devbridge/internal/workset"
)

// Uses the existing real SQLite/Core facade and JSON-RPC backend fixture.
// The full approval, execution, verification, acceptance, delivery recovery,
// and merge assertions remain the same as the legacy lifecycle test.
func TestProjectTrustParentCodingLifecycleRetainsCoreDeliveryGates(t *testing.T) {
	t.Setenv("DEVBRIDGE_PARENT_TRUSTED_CODING_FIXTURE", "1")
	var fixture *upperLifecycleFixture
	runCodingDeliveryFixture(t, nil, func(f *upperLifecycleFixture) {
		fixture = f
		if _, err := f.plane.ProjectAuthorizations.Update(f.ctx, f.project, 0, true, false); err != nil {
			t.Fatal(err)
		}
	})
	current, err := fixture.facade.GetTask(fixture.ctx, fixture.task)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := fixture.plane.DB.SQL().Query(`SELECT purpose,runtime_workset_json FROM runs WHERE execution_proposal_id=? AND purpose IN ('IMPLEMENTATION','VERIFICATION') ORDER BY purpose`, current.CurrentExecutionID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var purpose, raw string
		if err := rows.Scan(&purpose, &raw); err != nil {
			t.Fatal(err)
		}
		var w workset.RuntimeWorkset
		if err := json.Unmarshal([]byte(raw), &w); err != nil || w.Validate() != nil {
			t.Fatalf("invalid historical frozen workset: %v", err)
		}
		p := w.TaskPermissions
		if p == nil || !p.HasTrustedExecution() || p.TrustedExecution.ProjectID != fixture.project || p.TrustedExecution.AuthorizationRevision != 1 || p.NetworkAccess {
			t.Fatalf("project trust was not frozen independently from network: %+v", p)
		}
		if purpose == "VERIFICATION" && len(p.Copies) != 0 {
			t.Fatal("trusted lifecycle cloned verification source")
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("expected one implementation and one independent verification, got %d", count)
	}
}
