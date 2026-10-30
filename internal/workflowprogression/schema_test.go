package workflowprogression

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/decisionresolution"
	"github.com/lazyun/devbridge/internal/task"
)

// Approval decision replay can return a Task after automatic Implementation has
// started. Its exact Execution identity must survive the strict MCP schema.
func TestResolutionSchemaAcceptsUniversalTaskWork(t *testing.T) {
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(ResolutionJSONSchema), &schema); err != nil {
		t.Fatal(err)
	}
	workSchema := schema.Properties["task"].Properties["Work"]
	resolved, err := workSchema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	work := task.Work{ExecutionID: "execution", ExecutionKind: "DEVELOPMENT", ExecutionStatus: "COMPLETED", DomainStatus: "FAILED", DomainResult: "SCOPE_CONFLICT", OwnerType: "EXECUTION", OwnerID: "execution", RunID: "run", UpdatedAt: time.Now()}
	data, err := json.Marshal(work)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if err = resolved.Validate(wire); err != nil {
		t.Fatalf("universal Task.Work rejected: %v", err)
	}
	wire["executionProposalId"] = "authorization"
	if err = resolved.Validate(wire); err != nil {
		t.Fatalf("compatibility authorization rejected: %v", err)
	}
	wire["inventedAuthority"] = true
	if err = resolved.Validate(wire); err == nil {
		t.Fatal("strict schema was weakened")
	}
}

func TestResolutionSchemaAcceptsReReviewSuccessor(t *testing.T) {
	var schema jsonschema.Schema
	if err := json.Unmarshal([]byte(ResolutionJSONSchema), &schema); err != nil {
		t.Fatal(err)
	}
	value := Resolution{
		Guidance: Guidance{AllowedReviewDepths: []task.ReviewDepth{}, SourceSequence: 1, Status: "CURRENT", Message: "Review #2 is preparing", CurrentState: "REVIEWING", NextAction: Action{Code: "WAIT_FOR_REVIEW_START", Required: true, Message: "Wait for the authorized Review"}, RequiredAction: Action{Code: "WAIT_FOR_REVIEW_START", Required: true, Message: "Wait for the authorized Review"}, AvailableActions: []string{}, ProjectID: "project", FeatureAreaID: "area", TaskID: "task", ReviewID: "review-2", RunID: "run-2", Generation: "generation", RawResultAvailable: false, FormalResultAvailable: false},
		Receipt:  decisionresolution.Receipt{DecisionID: "REVIEW_APPROVAL:review-1", ProjectID: "project", Kind: KindReviewApproval, SubjectKind: "TASK_REVIEW_GATE", SubjectID: "task", Option: "REVISE", RequestID: "request", ResultKind: decisionresolution.ResultTask, ResultID: "task", ResolvedAt: time.Now()},
		Replay:   false, StartEffect: StartEffectNoStart, SuccessorReviewID: "review-2", SuccessorRunID: "run-2", ReviewStartRequestID: "start-2",
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var wire any
	if err = json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = resolved.Validate(wire); err != nil {
		t.Fatalf("re-review resolution rejected: %v\n%s", err, raw)
	}
}
