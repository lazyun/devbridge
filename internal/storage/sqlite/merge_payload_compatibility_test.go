package sqlite

import (
	"encoding/json"
	"github.com/lazyun/devbridge/internal/delivery"
	"testing"
)

func TestMergePayloadCompatibilityPreservesExactAuthority(t *testing.T) {
	in := mergeDispatchInput{ProjectID: "project", FeatureAreaID: "area", WorkspaceID: "workspace", PlanSHA256: "plan", Member: delivery.FeatureAreaMember{RepositoryID: "repo", SourceHead: "source", ExpectedDevHead: "target"}}
	current, _ := json.Marshal(in)
	legacy, _ := json.Marshal(struct {
		ProjectID, FeatureAreaID, WorkspaceID, PlanSHA256 string
		Member                                            delivery.FeatureAreaMember
	}{in.ProjectID, in.FeatureAreaID, in.WorkspaceID, in.PlanSHA256, in.Member})
	for _, raw := range []string{string(current), string(legacy)} {
		if !mergePayloadMatches(raw, workflowHash(raw), in) {
			t.Fatal("lost exact stored format")
		}
		if mergePayloadMatches(raw, "invalid hash", in) {
			t.Fatal("accepted invalid hash")
		}
		altered := in
		altered.Member.SourceHead = "another"
		if mergePayloadMatches(raw, workflowHash(raw), altered) {
			t.Fatal("substituted source")
		}
		altered = in
		altered.CompletionTaskID = "task"
		if mergePayloadMatches(raw, workflowHash(raw), altered) {
			t.Fatal("borrowed ordinary merge authority for completion")
		}
	}
	in.CompletionTaskID = "task"
	raw, _ := json.Marshal(in)
	if !mergePayloadMatches(string(raw), workflowHash(string(raw)), in) {
		t.Fatal("lost completion format")
	}
	altered := in
	altered.CompletionTaskID = ""
	if mergePayloadMatches(string(raw), workflowHash(string(raw)), altered) {
		t.Fatal("discarded completion authority")
	}
}
