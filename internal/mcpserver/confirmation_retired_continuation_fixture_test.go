package mcpserver

import (
	"encoding/json"
	"os"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

func TestRetiredContinuationPublicProjectionMatchesCard(t *testing.T) {
	raw, err := os.ReadFile("testdata/confirmation_retired_continuation_v5.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Entry json.RawMessage `json:"entry"`
		Read  json.RawMessage `json:"read"`
	}
	if err = json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, envelope := range []json.RawMessage{file.Entry, file.Read} {
		if failure := ValidateConfirmationCardEntryProjectionJSON(envelope); failure != nil {
			t.Fatalf("real retired read rejected: %+v", failure)
		}
	}
	for name, alter := range map[string]func(*c.Intent){
		"resumable":       func(v *c.Intent) { v.CanResume = true },
		"unknown":         func(v *c.Intent) { v.State = "UNKNOWN" },
		"missingProof":    func(v *c.Intent) { v.Steps[0].Effect = "UNKNOWN" },
		"ordinaryFailure": func(v *c.Intent) { v.Steps[0].Failure.Stage = "BUSINESS_STEP" },
		"started":         func(v *c.Intent) { v.Steps[1].State = "COMPLETED" },
		"output":          func(v *c.Intent) { v.Steps[0].RunID = "unproven-run" },
		"reprepare":       func(v *c.Intent) { v.Submission.ReprepareAllowed = true },
		"otherReceipt":    func(v *c.Intent) { v.Submission.ReceiptRequestID = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			var result c.Result
			if err := json.Unmarshal(file.Read, &result); err != nil {
				t.Fatal(err)
			}
			alter(result.Intent)
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if failure := ValidateConfirmationCardEntryProjectionJSON(encoded); failure == nil {
				t.Fatal("incomplete no-effect proof accepted")
			}
		})
	}
}

func TestFormalPreparationReadyPublicProjection(t *testing.T) {
	intent := liveShapeReviewIntent()
	intent.Purpose = c.Execution
	intent.Action = "APPROVE_AND_START"
	intent.State = "COMPLETED"
	intent.Content.Purpose = c.Execution
	intent.Content.Action = intent.Action
	intent.Content.Review = nil
	intent.Content.Execution = &c.ExecutionBody{Action: intent.Action, TaskID: "task", FeatureAreaID: "area", ReviewID: "review"}
	intent.Steps = []c.Step{{Kind: "APPROVE", RequestID: "original-formal-step", State: "COMPLETED", Effect: "PREPARATION_READY"}}
	result, invalid, err := publicConfirmationResultOrFallback(c.Result{Intent: &intent})
	if invalid || err != nil {
		t.Fatalf("formal preparation output schema: %v %v", invalid, err)
	}
	if failure := ValidateConfirmationCardEntryProjection(result); failure != nil {
		t.Fatalf("formal preparation Card projection: %+v", failure)
	}
}
