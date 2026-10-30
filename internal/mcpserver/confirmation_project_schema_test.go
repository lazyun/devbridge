package mcpserver

import (
	c "github.com/lazyun/devbridge/internal/confirmation"
	"strings"
	"testing"
)

func TestProjectSetupSchemasAcceptIndependentActionsAndExactRepositoryRows(t *testing.T) {
	prepare := resolveSchema(t, confirmationPrepareSchema(c.ProjectSetup))
	for _, action := range []string{"SELECT_PROJECT", "CONFIGURE_DEVELOPMENT_CONTEXT", "SELECT_PROJECT_BRANCH"} {
		if err := prepare.Validate(map[string]any{"aiSessionId": "s", "requestId": "r", "purpose": "PROJECT_SETUP", "action": action, "projectSetup": map[string]any{"summary": "Configure project"}, "variants": []any{map[string]any{"id": "v", "label": "Confirm", "summary": "Prepared exact choice"}}}); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	commit := resolveSchema(t, confirmationCommitSchema())
	input := map[string]any{"aiSessionId": "s", "intentId": "i", "version": float64(1), "purpose": "PROJECT_SETUP", "requestId": "r", "action": "CONFIGURE_DEVELOPMENT_CONTEXT", "contentHash": strings.Repeat("a", 64), "variantId": "v", "selection": map[string]any{"projectId": "p", "developmentContexts": []any{map[string]any{"repositoryId": "app", "developmentRef": "refs/heads/dev"}, map[string]any{"repositoryId": "sdk", "developmentRef": "refs/heads/dev"}}}}
	if err := commit.Validate(input); err != nil {
		t.Fatal(err)
	}
	input["selection"].(map[string]any)["developmentContexts"] = []any{map[string]any{"developmentRef": "refs/heads/dev"}}
	if commit.Validate(input) == nil {
		t.Fatal("missing Repository identity accepted")
	}
}

func TestExecutionConfirmationSchemaFencesDirectFileRecovery(t *testing.T) {
	prepare := resolveSchema(t, confirmationPrepareSchema(c.Execution))
	input := executionPrepareSchemaRequest("RECOVER_DIRECT_FILE", "DIRECT_FILE")
	if err := prepare.Validate(input); err != nil {
		t.Fatalf("fenced direct-file recovery rejected: %v", err)
	}
	delete(input["execution"].(map[string]any), "grantGeneration")
	if err := prepare.Validate(input); err == nil {
		t.Fatal("direct-file recovery without generation was accepted")
	}
}

func TestFileImplementationResultSchemaAllowsCoreObservedV2Members(t *testing.T) {
	submit := resolveSchema(t, fileResultSchema(false))
	input := map[string]any{"aiSessionId": "s", "taskId": "task", "taskDefinitionRevisionId": "revision", "workflowId": "workflow", "requestId": "result-1", "summary": "Applied requested files", "verificationNotes": "NOT_RUN", "protocolVersion": float64(2), "runtimeEpoch": "epoch", "grantGeneration": float64(1), "members": []any{map[string]any{"repositoryId": "app", "memberId": "member-app", "memberVersion": float64(1)}}}
	if err := submit.Validate(input); err != nil {
		t.Fatalf("fenced v2 result rejected: %v", err)
	}
	delete(input, "members")
	if err := submit.Validate(input); err != nil {
		t.Fatalf("Core derives complete members; optional advisory omission rejected: %v", err)
	}
}

func TestFeatureAreaMergeConfirmationSchemaRequiresExactPlan(t *testing.T) {
	prepare := resolveSchema(t, confirmationPrepareSchema(c.FeatureArea))
	member := map[string]any{"repositoryId": "app", "memberId": "member-app", "memberVersion": float64(1), "worktreeId": "worktree-app", "sourcePath": "/tmp/source", "sourceBranch": "task", "sourceHead": strings.Repeat("a", 40), "expectedDevHead": strings.Repeat("b", 40), "developmentRef": "refs/heads/dev", "developmentCycleId": "cycle", "targetPath": "/tmp/target", "taskCommits": []any{strings.Repeat("c", 40)}}
	plan := map[string]any{"version": float64(1), "projectId": "project", "featureAreaId": "area", "workspaceId": "workspace", "members": []any{member}, "sha256": strings.Repeat("d", 64)}
	input := map[string]any{"aiSessionId": "s", "requestId": "merge-1", "purpose": "FEATURE_AREA", "action": "MERGE_FEATURE_AREA", "featureArea": map[string]any{"featureAreaId": "area", "deliveryPlan": plan}, "variants": []any{map[string]any{"id": "v", "label": "Merge", "summary": "Merge exact accepted commits"}}}
	if err := prepare.Validate(input); err != nil {
		t.Fatalf("exact feature-area merge plan rejected: %v", err)
	}
	delete(member, "expectedDevHead")
	if err := prepare.Validate(input); err == nil {
		t.Fatal("merge member without expected development head was accepted")
	}
}

func TestConfirmationCommitSchemaAllowsOnlyExplicitUserVerificationWaiver(t *testing.T) {
	commit := resolveSchema(t, confirmationCommitSchema())
	input := map[string]any{"aiSessionId": "s", "intentId": "i", "version": float64(1), "purpose": "EXECUTION", "requestId": "r", "action": "APPROVE", "contentHash": strings.Repeat("a", 64), "variantId": "v", "selection": map[string]any{"verificationWaiver": map[string]any{"source": "USER_WAIVED", "reason": "User explicitly accepts file-only verification omission"}}}
	if err := commit.Validate(input); err != nil {
		t.Fatalf("explicit waiver rejected: %v", err)
	}
	input["selection"].(map[string]any)["verificationWaiver"].(map[string]any)["source"] = "SYSTEM"
	if err := commit.Validate(input); err == nil {
		t.Fatal("non-user waiver source accepted")
	}
}

func TestConfirmationCommitSchemaRejectsClientInjectedCoreFacts(t *testing.T) {
	commit := resolveSchema(t, confirmationCommitSchema())
	canonical := func() map[string]any {
		return map[string]any{"aiSessionId": "s", "intentId": "i", "version": float64(1), "purpose": "TASK", "requestId": "r", "action": "CREATE_TASK_AND_REVIEW", "contentHash": strings.Repeat("a", 64), "variantId": "upper", "selection": map[string]any{}}
	}
	if err := commit.Validate(canonical()); err != nil {
		t.Fatalf("canonical submission rejected: %v", err)
	}
	empty := canonical()
	empty["variantId"] = ""
	if commit.Validate(empty) == nil {
		t.Fatal("empty variantId accepted")
	}
	// Core-derived facts must never be injectable through the public DTO.
	for _, key := range []string{"outcome", "recorded", "receipt", "receiptRequestId", "steps", "taskId", "reviewId", "runId", "status", "canResume", "force", "assumeNotConsumed", "payloadSHA256", "confirmedOutcome", "nextAction", "failure", "businessObjects", "result", "backendSelection"} {
		input := canonical()
		input[key] = "injected"
		if commit.Validate(input) == nil {
			t.Fatalf("client-injected %q accepted", key)
		}
	}
	selection := canonical()
	selection["selection"] = map[string]any{"outcome": "CONSUMED", "force": true, "taskId": "client_forged_task"}
	if commit.Validate(selection) == nil {
		t.Fatal("client-injected selection core facts accepted")
	}
}

func TestReviewConfirmationSchemaBindsOnlyReReviewFeedback(t *testing.T) {
	prepare := resolveSchema(t, confirmationPrepareSchema(c.Review))
	canonical := func() map[string]any {
		return map[string]any{"aiSessionId": "s", "requestId": "r", "purpose": "REVIEW", "action": "REVISE", "review": map[string]any{"taskId": "task", "featureAreaId": "area", "reviewId": "review-1", "decisionId": "REVIEW_APPROVAL:review-1", "reviewFeedback": "Inspect the unresolved evidence again."}, "variants": []any{map[string]any{"id": "revise", "label": "Re-review", "summary": "Create the exact successor Review"}}}
	}
	if err := prepare.Validate(canonical()); err != nil {
		t.Fatalf("canonical re-review rejected: %v", err)
	}
	missing := canonical()
	delete(missing["review"].(map[string]any), "reviewFeedback")
	if prepare.Validate(missing) == nil {
		t.Fatal("re-review without feedback accepted")
	}
	for _, key := range []string{"newReviewId", "newRunId", "ordinal", "backendSelection", "model", "runtime", "workSpec", "verificationSpec", "autoStart", "force", "repositoryTargets", "reviewDepth", "workspaceId", "expectedTarget"} {
		in := canonical()
		in["review"].(map[string]any)[key] = "injected"
		if prepare.Validate(in) == nil {
			t.Fatalf("client injected %q into re-review", key)
		}
	}
	reject := canonical()
	reject["action"] = "REJECT"
	delete(reject["review"].(map[string]any), "reviewFeedback")
	if err := prepare.Validate(reject); err != nil {
		t.Fatalf("feedback-free abandon rejected: %v", err)
	}
}
