package bridgehttp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/run"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestTaskActionHTTPErrorPreservesOnlyProvenStartReceipt(t *testing.T) {
	receipt := &executionproposal.StartReceiptIdentity{ProjectID: "project", TaskID: "task", TaskDefinitionRevisionID: "revision", OriginalExecutionID: "execution-original", OriginalRunID: "run-original"}
	partial := executionproposal.StartResult{Receipt: receipt, Proposal: executionproposal.Proposal{ID: receipt.OriginalExecutionID}, Run: run.Run{ID: receipt.OriginalRunID}, Handoff: executionproposal.Handoff{State: executionproposal.HandoffUnknown}}
	for _, failure := range []struct {
		name   string
		err    error
		status int
	}{
		{"read-failure", errors.New("durable handoff read unavailable"), http.StatusInternalServerError},
		{"conflict", &controlapi.Error{Code: controlapi.CodeConflict, Message: "exact handoff unknown"}, http.StatusConflict},
		{"canceled", &controlapi.Error{Code: controlapi.CodeCanceled, Message: "caller canceled after commit"}, http.StatusRequestTimeout},
		{"progression", wp.Reject("FEATURE_AREA_BUSY", wp.Guidance{}), http.StatusConflict},
	} {
		t.Run(failure.name, func(t *testing.T) {
			baseline := httptest.NewRecorder()
			writeError(baseline, failure.err)
			w := httptest.NewRecorder()
			respondTaskAction(w, partial, failure.err)
			if w.Code != failure.status || w.Code != baseline.Code || w.Code < 400 {
				t.Fatalf("committed error became success: %d vs %d", w.Code, baseline.Code)
			}
			var original, body map[string]json.RawMessage
			if err := json.Unmarshal(baseline.Body.Bytes(), &original); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			for key, value := range original {
				if !reflect.DeepEqual(body[key], value) {
					t.Fatalf("original error field %s changed: %s vs %s", key, body[key], value)
				}
			}
			var saved executionproposal.StartResult
			if err := json.Unmarshal(body["partial"], &saved); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(saved.Receipt, receipt) || saved.Proposal.ID != receipt.OriginalExecutionID || saved.Run.ID != receipt.OriginalRunID || saved.Handoff.State != executionproposal.HandoffUnknown {
				t.Fatalf("partial lost original receipt: %+v", saved)
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("error receipt became cacheable")
			}
		})
	}
	for _, value := range []any{executionproposal.StartResult{Proposal: partial.Proposal, Run: partial.Run}, map[string]any{"executionId": "unproven"}, executionproposal.StartResult{Receipt: &executionproposal.StartReceiptIdentity{OriginalExecutionID: "incomplete"}}} {
		w := httptest.NewRecorder()
		respondTaskAction(w, value, errors.New("before durable receipt"))
		var body map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != 500 || body["partial"] != nil {
			t.Fatalf("unverified result exposed as committed: %d %s", w.Code, w.Body.String())
		}
	}
	for _, code := range []controlapi.ErrorCode{controlapi.CodePermissionDenied, controlapi.CodeNotFound} {
		failure := &controlapi.Error{Code: code, Message: "original resource is outside this caller scope"}
		w := httptest.NewRecorder()
		respondTaskAction(w, partial, failure)
		var body map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if w.Code != apiErrorStatus(failure) || body["partial"] != nil || body["receipt"] != nil {
			t.Fatalf("scope denial disclosed receipt: %d %s", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	respondTaskAction(w, partial, nil)
	var success executionproposal.StartResult
	if err := json.Unmarshal(w.Body.Bytes(), &success); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || !reflect.DeepEqual(success.Receipt, receipt) {
		t.Fatalf("successful response changed: %d %s", w.Code, w.Body.String())
	}
}
