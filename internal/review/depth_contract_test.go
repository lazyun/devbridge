package review

import (
	"context"
	"encoding/json"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/codexruntime"
	"github.com/lazyun/devbridge/internal/task"
)

func TestDepthScopeEffortModelAndInstructionAreOrthogonal(t *testing.T) {
	for _, depth := range []task.ReviewDepth{task.ReviewDepthFast, task.ReviewDepthDeep} {
		for _, scope := range []FocusKind{FocusFocused, FocusRepository} {
			for _, effort := range codexruntime.AllReasoningEfforts {
				t.Run(string(depth)+"/"+string(scope)+"/"+effort, func(t *testing.T) {
					store := &serviceStore{}
					svc, _ := NewService(store, nil, nil)
					instruction := " \t检查\r\n symbols <$&`{}> \n "
					v, err := svc.Create(context.Background(), Review{ReviewDepth: depth, TaskID: "task", ProjectID: "project", FeatureAreaID: "area", CreatedByAISessionID: "session", RepositoryID: "repo", CodexConfigID: "config", BackendSelection: &codexconfig.BackendSelection{ConfigID: "config", ConfigFingerprint: "test-backend"}, BaseCommit: strings.Repeat("a", 40), Focus: Focus{Kind: scope}, EngineeringGoal: "Semantic intent", Instruction: instruction, Model: "gpt-6-codex-preview", ReasoningEffort: effort})
					if err != nil || v.ReviewDepth != depth || v.Focus.Kind != scope || v.ReasoningEffort != effort || v.Model != "gpt-6-codex-preview" || v.Instruction != instruction {
						t.Fatalf("orthogonal request changed: %+v %v", v, err)
					}
				})
			}
		}
	}
	for _, invalid := range []task.ReviewDepth{"", "fast", "UNKNOWN"} {
		store := &serviceStore{}
		svc, _ := NewService(store, nil, nil)
		_, err := svc.Create(context.Background(), Review{ReviewDepth: invalid, TaskID: "task", ProjectID: "project", FeatureAreaID: "area", CreatedByAISessionID: "session", RepositoryID: "repo", CodexConfigID: "config", BackendSelection: &codexconfig.BackendSelection{ConfigID: "config", ConfigFingerprint: "test-backend"}, BaseCommit: strings.Repeat("a", 40), Focus: Focus{Kind: FocusRepository}, EngineeringGoal: "FAST", Instruction: "DEEP"})
		if err != ErrInvalidArgument || store.created.ID != "" {
			t.Fatalf("depth inferred: %q %v", invalid, err)
		}
	}
}

func TestReviewDispositionStructuredContract(t *testing.T) {
	raw := `{"version":1,"reviewDisposition":"FURTHER_REVIEW_REQUIRED","repositoryTargets":[],"primaryMemberId":"","summary":"APPROVAL_READY is text, not authority","workSpec":null,"verificationSpec":null}`
	result, err := ParseResult(raw)
	if err != nil || result.ReviewDisposition != DispositionFurtherReviewRequired {
		t.Fatalf("further result: %+v %v", result, err)
	}
	if _, err = ParseImplementationResult(raw); err == nil {
		t.Fatal("further authorized approval")
	}
	var schema jsonschema.Schema
	if err = json.Unmarshal([]byte(ResultJSONSchema), &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	_ = json.Unmarshal([]byte(`{"version":1,"reviewDisposition":"FURTHER_REVIEW_REQUIRED","repositoryTargets":[],"primaryTargetAlias":"","summary":"APPROVAL_READY is text, not authority","workSpec":null,"verificationSpec":null}`), &value)
	if err = resolved.Validate(value); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "UNKNOWN", "INFRASTRUCTURE_FAILED", "APPROVAL_READY"} {
		value["reviewDisposition"] = bad
		encoded, _ := json.Marshal(value)
		if _, err = ParseResult(string(encoded)); err == nil {
			t.Fatalf("bad disposition/proposal accepted: %s", encoded)
		}
		if bad != "APPROVAL_READY" && resolved.Validate(value) == nil {
			t.Fatal("schema accepted invalid disposition")
		}
	}
	delete(value, "reviewDisposition")
	encoded, _ := json.Marshal(value)
	if resolved.Validate(value) == nil {
		t.Fatal("schema omitted disposition")
	}
	if _, err = ParseResult(string(encoded)); err == nil {
		t.Fatal("inferred disposition from summary")
	}
}
