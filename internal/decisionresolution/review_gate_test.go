package decisionresolution

import "testing"

func TestReviewGateIdentitySeparatesTaskSubjectFromSourceReview(t *testing.T) {
	valid := Request{DecisionID: "REVIEW_APPROVAL:review-current", Kind: "REVIEW_APPROVAL", SubjectKind: "TASK_REVIEW_GATE", SubjectID: "task-intent", SourceReviewID: "review-current", Option: "APPROVE", RequestID: "stable-request"}
	if err := ValidateRequest(valid); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Request){func(v *Request) { v.SourceReviewID = "review-old" }, func(v *Request) { v.SourceReviewID = "" }, func(v *Request) { v.SubjectKind = "REVIEW" }, func(v *Request) { v.SubjectID = "" }} {
		value := valid
		change(&value)
		if ValidateRequest(value) == nil {
			t.Fatalf("invalid Task gate identity accepted: %+v", value)
		}
	}
}
