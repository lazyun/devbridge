package localmcp_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lazyun/devbridge/internal/bridgehttp"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

func TestNoRunSuccessorCoreCommandAfterCancellation(t *testing.T) {
	f, sid, predecessor := localMCPQualifiedAdmission(t)
	first, err := f.control.CancelExecutionForSession(f.ctx, admissionCancelInput(f, sid, predecessor))
	if err != nil {
		t.Fatal(err)
	}
	assertAdmissionCancelReceipt(t, predecessor, first)
	owner, err := f.plane.Tasks.Get(f.ctx, predecessor.TaskID)
	if err != nil || owner.Phase != task.PhaseNoRunSuccessorRequired || owner.CurrentExecutionID != predecessor.ID {
		t.Fatalf("projected Task %+v %v", owner, err)
	}
	in := wp.PrepareSuccessorExecution{RequestID: "localmcp-core-successor", PayloadSHA256: strings.Repeat("a", 64), TaskID: owner.ID, PredecessorExecutionID: predecessor.ID, ExpectedTaskState: wp.NoRunSuccessorState, ExpectedCurrentExecutionID: predecessor.ID, ExpectedDefinitionRevisionID: owner.CurrentDefinitionRevisionID}
	created, err := f.control.PrepareSuccessorExecutionForSession(f.ctx, sid, in)
	if err != nil || created.Outcome != wp.NoRunSuccessorCreated {
		t.Fatalf("successor %+v %v (cause: %v)", created, err, errors.Unwrap(err))
	}
	replay, err := f.control.PrepareSuccessorExecutionForSession(f.ctx, sid, in)
	if err != nil || replay != created {
		t.Fatalf("replay %+v %v", replay, err)
	}
}

func TestNoRunSuccessorCommandHTTPParity(t *testing.T) {
	f, sid, predecessor := localMCPQualifiedAdmission(t)
	if _, err := f.control.CancelExecutionForSession(f.ctx, admissionCancelInput(f, sid, predecessor)); err != nil {
		t.Fatal(err)
	}
	owner, err := f.plane.Tasks.Get(f.ctx, predecessor.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	input := wp.PrepareSuccessorExecution{RequestID: "http-successor-parity", PayloadSHA256: strings.Repeat("c", 64), ProjectID: f.projectID, TaskID: owner.ID, PredecessorExecutionID: predecessor.ID, ExpectedTaskState: wp.NoRunSuccessorState, ExpectedCurrentExecutionID: predecessor.ID, ExpectedDefinitionRevisionID: owner.CurrentDefinitionRevisionID}
	web, err := bridgehttp.New(f.plane, false)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/executions/successor/prepare", bytes.NewReader(payload))
	setLocalOperatorLoopback(request)
	web.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("HTTP successor status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var created wp.SuccessorCommandReceipt
	if err := json.Unmarshal(recorder.Body.Bytes(), &created); err != nil || created.Outcome != wp.NoRunSuccessorCreated {
		t.Fatalf("HTTP successor %+v %v", created, err)
	}
	// The HTTP command belongs to its local operator. An AI Session cannot
	// borrow that exact request identity, even within the same Project.
	if _, err = f.control.PrepareSuccessorExecutionForSession(f.ctx, sid, input); err == nil || !strings.Contains(err.Error(), "REQUEST_IDENTITY_CONFLICT") {
		t.Fatalf("AI Session borrowed operator replay: %v", err)
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/v1/executions/successor/prepare", bytes.NewReader(payload))
	setLocalOperatorLoopback(request)
	web.Handler().ServeHTTP(recorder, request)
	var replay wp.SuccessorCommandReceipt
	if err = json.Unmarshal(recorder.Body.Bytes(), &replay); err != nil || recorder.Code != http.StatusOK || replay != created {
		t.Fatalf("exact HTTP replay %+v %+v %v", created, replay, err)
	}
	input.RequestID += "-ai-ack"
	ack, err := f.control.PrepareSuccessorExecutionForSession(f.ctx, sid, input)
	if err != nil || ack.Outcome != wp.NoRunSuccessorAlreadyExists || ack.SuccessorExecutionID != created.SuccessorExecutionID || ack.SuccessorEffectAuthorityID != created.SuccessorEffectAuthorityID {
		t.Fatalf("HTTP/Core effect parity %+v %+v %v", created, ack, err)
	}
}

func TestNoRunSuccessorCommandParityAcrossMCPAdapters(t *testing.T) {
	f, sid, predecessor := localMCPQualifiedAdmission(t)
	if _, err := f.control.CancelExecutionForSession(f.ctx, admissionCancelInput(f, sid, predecessor)); err != nil {
		t.Fatal(err)
	}
	owner, err := f.plane.Tasks.Get(f.ctx, predecessor.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{
		"aiSessionId": sid, "requestId": "mcp-successor-parity", "payloadSha256": strings.Repeat("b", 64),
		"taskId": owner.ID, "predecessorExecutionId": predecessor.ID,
		"expectedTaskState": wp.NoRunSuccessorState, "expectedCurrentExecutionId": predecessor.ID,
		"expectedDefinitionRevisionId": owner.CurrentDefinitionRevisionID,
	}
	first := callToolMap(t, f.ctx, connectGPT(t, f.gptURL), "successor_execution_prepare", args)
	replay := callToolMap(t, f.ctx, connectLocalMCP(t, f.libreURL), "successor_execution_prepare", args)
	if first["outcome"] != wp.NoRunSuccessorCreated || replay["outcome"] != wp.NoRunSuccessorCreated || first["successorExecutionId"] != replay["successorExecutionId"] || first["successorEffectAuthorityId"] != replay["successorEffectAuthorityId"] {
		t.Fatalf("MCP successor parity first=%v replay=%v", first, replay)
	}
}
