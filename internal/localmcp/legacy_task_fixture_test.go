package localmcp_test

import (
	"context"
	"encoding/json"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/task"
	"github.com/lazyun/devbridge/internal/workflowtest"
	"strconv"
	"sync/atomic"
	"testing"
)

var seededTaskRequestSequence uint64

// frozenCodingReview supplies the explicit backend and runtime facts required
// by current Review admission. It is test fixture construction only; the
// resulting Review still goes through the production Review service/store.
func (f *integrationFixture) frozenCodingReview(t *testing.T, value review.Review) review.Review {
	t.Helper()
	config, err := f.plane.CodexConfigs.Get(context.Background(), value.CodexConfigID)
	if err != nil {
		t.Fatal(err)
	}
	selection := config.Selection("localmcp-test-fixture")
	value.ExecutorKind = review.ExecutorCodingAgent
	value.BackendSelection = &selection
	value.Model = "gpt-5.6-terra"
	value.ReasoningEffort = "high"
	return value
}

func (f *integrationFixture) seedTask(t *testing.T, args map[string]any) task.Task {
	t.Helper()
	sid := args["aiSessionId"].(string)
	session, err := f.plane.AISessions.Get(f.ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	requestID := "fixture-seed-task-" + strconv.FormatUint(atomic.AddUint64(&seededTaskRequestSequence, 1), 10)
	current, err := f.plane.Tasks.Create(f.ctx, task.CreateInput{AISessionID: sid, RequestID: requestID, ProjectID: session.ProjectID, FeatureAreaID: args["featureAreaId"].(string), RepositoryID: f.repositoryID, ModuleID: f.moduleID, CodexConfigID: codexconfig.DefaultConfigID, ProblemStatement: args["goal"].(string), BaseRevision: "HEAD", Planning: true})
	if err != nil {
		t.Fatal(err)
	}
	return current
}
func (f *integrationFixture) seedTaskExecution(t *testing.T, args map[string]any) map[string]any {
	t.Helper()
	current := f.seedTask(t, args)
	sid := args["aiSessionId"].(string)
	var scope task.ExecutionScope
	raw, _ := json.Marshal(args["executionScope"])
	if err := json.Unmarshal(raw, &scope); err != nil {
		t.Fatal(err)
	}
	var verification task.VerificationSpec
	raw, _ = json.Marshal(args["verificationSpec"])
	if err := json.Unmarshal(raw, &verification); err != nil {
		t.Fatal(err)
	}
	work := task.WorkSpec{Goal: args["goal"].(string), CodexInstruction: args["implementationInstruction"].(string), ExecutionScope: scope}
	current = workflowtest.Approve(t, f.plane.DB, current, sid, work, verification)
	started, err := f.control.StartTaskExecution(f.ctx, sid, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"id": current.ID, "executionId": started.Proposal.ID, "runId": started.Run.ID}
}
