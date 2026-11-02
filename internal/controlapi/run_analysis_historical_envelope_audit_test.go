package controlapi_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/controlplane"
	"github.com/lazyun/devbridge/internal/runanalysis"
)

// Core/SQLite/Git and real Local MCP, with the existing explicitly test-only
// separate-process JSON-RPC agent fixture. No native/model acceptance claimed.
func TestRunAnalysisHistoricalReviewEnvelopeAfterRevise(t *testing.T) {
	t.Setenv("DEVBRIDGE_TEST_UNIQUE_TURNS", "1")
	runCodingReviewFixtureScoped(t, "data-instance-local", func(f *upperLifecycleFixture, taskID, reviewID, runID string) bool {
		t := f.t
		if strings.HasSuffix(t.Name(), "/mutation-blocked") {
			return true
		}
		f.task, f.review, f.run = taskID, reviewID, runID
		waitComplete := func(id string) {
			t.Helper()
			deadline := time.Now().Add(20 * time.Second)
			for {
				var status string
				var claims int
				err := f.plane.DB.SQL().QueryRow(`SELECT status,(SELECT count(*) FROM feature_area_agent_claims WHERE feature_area_id=r.feature_area_id) FROM runs r WHERE id=?`, id).Scan(&status, &claims)
				if err == nil && status == "SUCCEEDED" && claims == 0 {
					return
				}
				if status == "FAILED" || status == "INFRASTRUCTURE_FAILED" {
					value, _ := f.facade.GetRunForSession(f.ctx, f.session, id)
					t.Fatalf("protocol fixture failed: run=%s failure=%+v", id, value.Failure)
				}
				if time.Now().After(deadline) {
					t.Fatalf("protocol fixture did not settle: run=%s status=%s claims=%d err=%v", id, status, claims, err)
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
		waitComplete(runID)
		submitNaturalFixtureAnalysis(t, f, runID, []byte(os.Getenv("DEVBRIDGE_CODING_V1_REVIEW_RESULT")))
		_, commit := prepareLifecycleReviewDecision(t, f, "REVISE", "analysis-history-revise")
		completed, err := f.facade.CommitConfirmation(f.ctx, commit)
		if err != nil || completed.State != "COMPLETED" {
			t.Fatalf("REVISE: %+v %v", completed, err)
		}
		current, err := f.plane.Tasks.Get(f.ctx, taskID)
		if err != nil || current.CurrentReviewID == reviewID {
			t.Fatalf("no successor: %+v %v", current, err)
		}
		successor, err := f.plane.Reviews.Get(f.ctx, current.CurrentReviewID)
		if err != nil {
			t.Fatal(err)
		}
		waitComplete(successor.RunID)
		f.plane.Retirement.Close()
		originalSource, err := f.facade.GetRunAnalysis(f.ctx, f.session, runID)
		if err != nil {
			t.Fatal(err)
		}
		readerID := f.session
		client := newApprovedStartLocalClient(t, f)
		for _, cold := range []bool{false, true} {
			if cold {
				client.close()
				reopenRecoveryFixture(t, f, controlplane.Config{DataRoot: f.dataRoot})
				f.plane.Retirement.Close()
				client = newApprovedStartLocalClient(t, f)
			}
			tables := []string{"tasks", "reviews", "runs", "run_analyses", "review_result_materializations", "review_approval_decisions", "execution_proposals"}
			before := definitionFormalSnapshot(t, f, tables)
			var response map[string]any
			raw := client.call("run_analysis_get", map[string]any{"aiSessionId": readerID, "runId": runID}, false)
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			t.Logf("cold=%v original=%s/%s successor=%s/%s context=%v workflow=%v", cold, reviewID, runID, successor.ID, successor.RunID, response["context"], response["workflow"])
			result := response["result"].(map[string]any)
			var source runanalysis.Source
			encoded, _ := json.Marshal(result)
			if err := json.Unmarshal(encoded, &source); err != nil {
				t.Fatal(err)
			}
			var originalReport, returnedReport any
			_ = json.Unmarshal(originalSource.Report, &originalReport)
			_ = json.Unmarshal(source.Report, &returnedReport)
			if source.Run.ID != runID || source.Run.Result == nil || source.Run.Result.AgentClaim.Message != originalSource.Run.Result.AgentClaim.Message || source.RecordedSourceHash != originalSource.RecordedSourceHash || !reflect.DeepEqual(source.Receipt, originalSource.Receipt) || !reflect.DeepEqual(originalReport, returnedReport) {
				t.Errorf("historical immutable analysis source changed")
			}

			if result["review"].(map[string]any)["ID"] != reviewID || result["receipt"].(map[string]any)["runId"] != runID || result["state"] != "RECORDED" {
				t.Errorf("exact source rewritten: %+v", result)
			}
			context := response["context"].(map[string]any)
			workflow := response["workflow"].(map[string]any)
			if context["reviewId"] != nil || context["runId"] != runID {
				t.Errorf("cold=%v old analysis borrowed successor identity: %+v", cold, context)
			}
			if len(workflow) != 0 || response["refreshRequired"] == true {
				t.Errorf("cold=%v old analysis borrowed successor action: %+v", cold, workflow)
			}
			var submitted map[string]any
			submit := map[string]any{"aiSessionId": readerID, "runId": runID, "requestId": originalSource.Receipt.RequestID, "sourceHash": originalSource.RecordedSourceHash, "report": json.RawMessage(originalSource.Report)}
			if err := json.Unmarshal(client.call("run_analysis_submit", submit, false), &submitted); err != nil {
				t.Fatal(err)
			}
			wantReceipt, _ := json.Marshal(originalSource.Receipt)
			gotReceipt, _ := json.Marshal(submitted["result"])
			if string(wantReceipt) != string(gotReceipt) { // Decode before comparison to ignore struct field ordering.
				var want any
				_ = json.Unmarshal(wantReceipt, &want)
				wantReceipt, _ = json.Marshal(want)
				if string(wantReceipt) != string(gotReceipt) {
					t.Errorf("replay changed receipt: %s -> %s", wantReceipt, gotReceipt)
				}
			}
			if len(submitted["workflow"].(map[string]any)) != 0 || submitted["context"].(map[string]any)["runId"] != runID {
				t.Errorf("submit replay borrowed current run: %+v", submitted)
			}
			submit["requestId"] = "changed-request"
			client.call("run_analysis_submit", submit, true)
			var currentRead map[string]any
			if err := json.Unmarshal(client.call("run_analysis_get", map[string]any{"aiSessionId": readerID, "runId": successor.RunID}, false), &currentRead); err != nil {
				t.Fatal(err)
			}
			if currentRead["result"].(map[string]any)["state"] != "REQUIRED" || currentRead["context"].(map[string]any)["runId"] != successor.RunID {
				t.Errorf("current source lost: %+v", currentRead)
			}
			assertDefinitionFormalSnapshot(t, f, tables, before, "historical/current run analysis reads and exact/conflicting submit replay")
		}
		return false
	})
}
