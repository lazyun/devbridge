package sqlite

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/review"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestReviewModelWireInvalidResultRemainsInvalidAfterRestart(t *testing.T) {
	for _, tc := range []struct{ name, before, after string }{
		{"duplicate disposition", `"reviewDisposition":"APPROVAL_READY"`, `"reviewDisposition":"FURTHER_REVIEW_REQUIRED","reviewDisposition":"APPROVAL_READY"`},
		{"case folded field", `"workSpec":`, `"WorkSpec":`},
		{"null check paths", `"paths":[]`, `"paths":null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReviewApprovalFixture(t)
			var wire map[string]any
			if err := json.Unmarshal([]byte(approvedResult(t)), &wire); err != nil {
				t.Fatal(err)
			}
			wire["repositoryTargets"], wire["primaryTargetAlias"] = []any{}, ""
			checks := wire["verificationSpec"].(map[string]any)["checks"].([]any)
			for _, v := range checks {
				check := v.(map[string]any)
				check["targetAlias"], check["targetAliases"], check["workingDirectory"] = "", []any{}, ""
				check["instruction"], check["paths"], check["packages"] = "", []any{}, []any{}
			}
			encoded, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			valid := string(encoded)
			if _, err := review.ParseModelResult(valid, nil, ""); err != nil {
				t.Fatalf("invalid baseline: %v", err)
			}
			raw := strings.Replace(valid, tc.before, tc.after, 1)
			if raw == valid {
				t.Fatal("mutation did not apply")
			}
			a := f.start(t)
			f.complete(t, a, raw)
			before := scanMaterializationClaim(t, f.db, a.Review.ID)
			if !before.found || before.state != "FINALIZED" || before.outcome != "INVALID" || before.decisionID != wp.InvalidResultDecisionID(a.Review.ID) || before.rawSHA != workflowHash(raw) {
				t.Fatalf("invalid wire result was promoted: %+v", before)
			}
			for _, cold := range []bool{false, true} {
				if cold {
					root := f.db.stateRoot
					if err := f.db.Close(); err != nil {
						t.Fatal(err)
					}
					f.db = openTestDB(t, root)
					t.Cleanup(func() { f.db.Close() })
					f.store = NewWorkflowProgressionStore(f.db)
				}
				if err := f.store.Reconcile(context.Background()); err != nil {
					t.Fatal(err)
				}
				after := scanMaterializationClaim(t, f.db, a.Review.ID)
				if after != before {
					t.Fatalf("cold=%v changed immutable invalid claim: before=%+v after=%+v", cold, before, after)
				}
				var retained string
				if err := f.db.sql.QueryRow(`SELECT result_text FROM runs WHERE id=?`, a.Run.ID).Scan(&retained); err != nil || retained != raw {
					t.Fatalf("raw evidence changed: %q %v", retained, err)
				}
				if n := countRows(t, f.db, `SELECT count(*) FROM review_result_materializations WHERE review_id=? AND outcome='INVALID' AND reason_code='RESULT_INVALID' AND formal_result_json IS NULL`, a.Review.ID); n != 1 {
					t.Fatalf("invalid materializations=%d", n)
				}
				if n := countRows(t, f.db, `SELECT count(*) FROM review_approval_decisions WHERE review_id=?`, a.Review.ID); n != 1 {
					t.Fatalf("decisions=%d", n)
				}
				if n := countRows(t, f.db, `SELECT count(*) FROM execution_proposals`); n != 0 {
					t.Fatalf("invalid result created execution proposals=%d", n)
				}
			}
		})
	}
}
