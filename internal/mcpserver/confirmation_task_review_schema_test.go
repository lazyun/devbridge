package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	c "github.com/lazyun/devbridge/internal/confirmation"
)

func taskReviewSchemaRequest() map[string]any {
	return map[string]any{"aiSessionId": "session", "intentId": "intent", "version": float64(1), "purpose": "TASK", "requestId": "request", "action": "CREATE_TASK_AND_REVIEW", "contentHash": strings.Repeat("a", 64), "variantId": "prepared", "selection": map[string]any{}}
}

func TestConfirmationTaskReviewSelectionSchemas(t *testing.T) {
	commit, options, read := resolveSchema(t, confirmationCommitSchema()), resolveSchema(t, confirmationReadSchema(true)), resolveSchema(t, confirmationReadSchema(false))
	for _, value := range []any{false, true, nil, "false", float64(0), map[string]any{}, []any{}} {
		request := taskReviewSchemaRequest()
		request["selection"].(map[string]any)["startReview"] = value
		_, valid := value.(bool)
		if err := commit.Validate(request); (err == nil) != valid {
			t.Fatalf("commit startReview=%#v: %v", value, err)
		}
		if reason, _ := validateRequestShape(request); (reason == "") != valid {
			t.Fatalf("projection startReview=%#v: %s", value, reason)
		}
		query := map[string]any{"aiSessionId": "session", "intentId": "intent", "version": float64(1), "purpose": "TASK", "contentHash": strings.Repeat("a", 64), "startReview": value}
		if err := options.Validate(query); (err == nil) != valid {
			t.Fatalf("options startReview=%#v: %v", value, err)
		}
		if err := read.Validate(query); err == nil {
			t.Fatal("a receipt read must not accept a new Review choice")
		}
	}
	if err := commit.Validate(taskReviewSchemaRequest()); err != nil {
		t.Fatalf("legacy omission rejected: %v", err)
	}
}

func TestConfirmationTaskReviewChoiceParticipatesInReceiptIdentity(t *testing.T) {
	for _, value := range []any{false, true} {
		a, b := taskReviewSchemaRequest(), taskReviewSchemaRequest()
		a["selection"].(map[string]any)["startReview"] = value
		if sameCommit(a, b) {
			t.Fatal("an omitted legacy choice is not the same exact request as an explicit choice")
		}
		b["selection"].(map[string]any)["startReview"] = value
		if !sameCommit(a, b) {
			t.Fatal("an identical explicit choice must replay")
		}
		b["selection"].(map[string]any)["startReview"] = !value.(bool)
		if sameCommit(a, b) {
			t.Fatal("receipt accepted another Review choice")
		}
	}
	on, off := true, false
	selections := []c.Selection{{}, {StartReview: &off}, {StartReview: &on}}
	hashes := make(map[string]bool)
	for _, selection := range selections {
		hash := c.ValueHash(selection)
		if hashes[hash] {
			t.Fatal("Review choices collapsed in the canonical selection hash")
		}
		hashes[hash] = true
	}
	legacy, _ := json.Marshal(selections[0])
	disabled, _ := json.Marshal(selections[1])
	if string(legacy) != "{}" || string(disabled) != `{"startReview":false}` {
		t.Fatalf("legacy omission / explicit false changed: %s / %s", legacy, disabled)
	}
}

func TestConfirmationTaskReviewOptionsProjectionPreservesFalse(t *testing.T) {
	off := false
	options := c.Options{AISessionID: "session", IntentID: "intent", Version: 1, Purpose: c.Task, StartReview: &off}
	result, invalid, err := publicConfirmationResultOrFallback(c.Result{Options: &options})
	if err != nil || invalid {
		t.Fatalf("options projection: invalid=%v err=%v", invalid, err)
	}
	if failure := ValidateConfirmationOptionsProjection(result); failure != nil {
		t.Fatalf("options projection: %+v", failure)
	}
	encoded := schemaObject(result)
	projected := encoded["options"].(map[string]any)
	if value, ok := projected["startReview"].(bool); !ok || value {
		t.Fatalf("explicit false disappeared from public options: %+v", projected)
	}
	projected["startReview"] = nil
	if err := resolveSchema(t, confirmationResultSchema()).Validate(encoded); err == nil {
		t.Fatal("public options schema accepts an ambiguous null Review choice")
	}
	if code, _ := validateOptionsAbsolute(projected); code != "TASK_REVIEW_OPTIONS_INVALID" {
		t.Fatalf("projection accepts an ambiguous null Review choice: %s", code)
	}
}

func TestConfirmationReviewModeIsClosedAndPartOfReceipt(t *testing.T) {
	schema := resolveSchema(t, confirmationCommitSchema())
	for _, value := range []any{"NONE", "UPPER_LLM", "CODING_AGENT", "other", nil, false} {
		req := taskReviewSchemaRequest()
		req["selection"].(map[string]any)["reviewMode"] = value
		mode, _ := value.(string)
		valid := mode == "NONE" || mode == "UPPER_LLM" || mode == "CODING_AGENT"
		if err := schema.Validate(req); (err == nil) != valid {
			t.Fatalf("reviewMode=%v: %v", value, err)
		}
		if code, _ := validateRequestShape(req); (code == "") != valid {
			t.Fatalf("projection reviewMode=%v: %s", value, code)
		}
		if valid && sameCommit(req, taskReviewSchemaRequest()) {
			t.Fatal("review mode omitted from exact request identity")
		}
	}
}

func TestTaskPrepareOffersBothReviewExecutors(t *testing.T) {
	schema := resolveSchema(t, confirmationPrepareSchema(c.Task))
	req := map[string]any{"aiSessionId": "session", "requestId": "prepare", "purpose": "TASK", "action": "CREATE_TASK_AND_REVIEW", "task": map[string]any{"featureAreaId": "area", "repositoryId": "repo", "title": "Task", "problemStatement": "Inspect change", "constraints": []any{}, "startReview": true, "reviewDepth": "FAST", "reviewPlan": map[string]any{"mode": "REPOSITORY"}}, "variants": []any{map[string]any{"id": "ai", "label": "AI", "summary": "Read files", "executorKind": "UPPER_LLM", "reviewInstruction": "Read authorized files and submit the report."}, map[string]any{"id": "codex", "label": "Codex", "summary": "Review", "executorKind": "CODING_AGENT", "reviewInstruction": "Review the repository and report findings."}}}
	if err := schema.Validate(req); err != nil {
		t.Fatal(err)
	}
	variants := req["variants"].([]any)
	req["variants"] = variants[:1]
	if err := schema.Validate(req); err == nil {
		t.Fatal("new Task proposal omitted a user-selectable executor")
	}
	req["variants"] = []any{variants[0], variants[0]}
	if err := schema.Validate(req); err == nil {
		t.Fatal("duplicate executor replaced the other user choice")
	}
}

func TestConfirmationReviewPlanExamplesRespectClosedInput(t *testing.T) {
	schema := resolveSchema(t, confirmationRaw(externalReviewPlanSchema))
	for _, marker := range []string{"Repository example: ", "Focused example: "} {
		_, example, found := strings.Cut(confirmationReviewPlanGuidance, marker)
		if !found {
			t.Fatalf("missing public plan example %q", marker)
		}
		var plan map[string]any
		if err := json.NewDecoder(strings.NewReader(example)).Decode(&plan); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(plan); err != nil {
			t.Fatalf("published example is rejected by the actual plan schema: %v", err)
		}
		plan["initialScope"] = map[string]any{"kind": "REPOSITORY"}
		if err := schema.Validate(plan); err == nil {
			t.Fatal("derived scope became an accepted transport override")
		}
	}
}
