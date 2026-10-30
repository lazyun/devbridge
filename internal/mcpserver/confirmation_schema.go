package mcpserver

import (
	"encoding/json"
	"github.com/google/jsonschema-go/jsonschema"
	"reflect"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/recoverystatus"
)

// Keep the complete public shapes readable even when a host renders composed
// JSON Schema as an opaque parameter type. Core still derives initialScope.
const confirmationReviewPlanGuidance = ` Review plan input: use task.reviewPlan for TASK, featureArea.initialTask.reviewPlan for FEATURE_AREA, or review.reviewPlan for REVIEW. Repository example: {"mode":"REPOSITORY"}. Focused example: {"mode":"FOCUSED","selectedModules":[{"key":"<authorized module key>","role":"PRIMARY"}]}. Focused module roles are PRIMARY or SUPPORTING; read real module keys before using them. These are the complete allowed plan shapes: do not send initialScope, paths or scope. Core derives initialScope. For a new Task with startReview=true, include reviewDepth FAST or DEEP and the complete reviewPlan in task; the two variants carry executorKind UPPER_LLM and CODING_AGENT with their reviewInstruction. The user chooses the review method on the Card.`

const confirmationTaskActionGenerationGuidance = ` taskActionGeneration belongs at the TOP LEVEL of the prepare arguments, alongside aiSessionId, requestId, purpose and action; never put it inside execution, review, featureArea, task or variants. When the panel supplies this fence, preserve its exact observed positive integer. Core rejects a changed generation with TASK_ACTION_STALE. If a client rejects this top-level field as an additional property, its tool definition is out of date: refresh the tool catalog and read the original request outcome before any retry; never drop the fence, change its value or invent a new requestId to bypass the rejection.`

const confirmationExecutionGuidance = ` Execution input: repeat the action in action and execution.action; include execution.executionId (empty before an Execution exists). APPROVE and all implementation actions require complete variants: set BOTH variants[i].implementationMode and variants[i].workSpec.implementationMode to the same CODING_AGENT or DIRECT_FILE value, and include the full workSpec and verificationSpec from the reviewed or frozen plan. implementationMode is NOT an execution field; do not invent implementationInstruction or verificationInstruction aliases. Verification-stage primary choices are CONTINUE_TASK (retain the Task and prepare further work), COMPLETE_TASK (one confirmation accepts, commits and integrates into frozen development refs; completion requires successful integration), and DEFER_TASK (save an idle checkpoint with execution.note and no Git stash). The Core derives COMPLETE_TASK integration targets during prepare; do not invent a merge plan. These choices preserve all prior results. Only these choices plus ACCEPT, REJECT and REQUEST_REVERIFICATION use a single decision-only variant containing id, label, summary and optional displaySummary. Direct File actions require one complete DIRECT_FILE variant matching the frozen plan. START_DIRECT_FILE has no active workflow fences (omit workflowId, runtimeEpoch and grantGeneration); RECOVER_DIRECT_FILE and CANCEL_DIRECT_FILE require the exact current workflowId, runtimeEpoch and positive grantGeneration. Prepare only persists a candidate; approval and implementation require the separate user confirmation.`

func confirmationObject(properties map[string]any, required ...string) map[string]any {
	out := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
func confirmationString(min, max int) map[string]any {
	return map[string]any{"type": "string", "minLength": min, "maxLength": max}
}
func confirmationEnum(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}
func confirmationArray(items any, min, max int) map[string]any {
	return map[string]any{"type": "array", "items": items, "minItems": min, "maxItems": max}
}
func confirmationRaw(raw string) any {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		panic(err)
	}
	return v
}
func confirmationRuntimeSchema() any {
	schema := confirmationObject(map[string]any{"model": confirmationString(1, 128), "reasoningEffort": confirmationString(1, 64), "codexFastMode": map[string]any{"type": "boolean"}, "agentId": confirmationEnum("codex", "pi"), "agentConfigFingerprint": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}, "executionPolicy": confirmationEnum("TASK_SANDBOX_V1", "COOPERATIVE_TRUSTED_V1")}, "model", "reasoningEffort", "codexFastMode")
	schema["description"] = "Agent and execution policy are prospective explicit user selections, frozen for this work. Missing fields preserve historical Codex/native semantics. COOPERATIVE_TRUSTED_V1 trusts the selected agent to follow Core policy context and does not provide TASK_SANDBOX_V1 native isolation guarantees. Recommendations are not authorization."
	schema["allOf"] = []any{
		map[string]any{"if": map[string]any{"required": []string{"agentConfigFingerprint"}}, "then": map[string]any{"required": []string{"executionPolicy"}, "properties": map[string]any{"executionPolicy": map[string]any{"const": "COOPERATIVE_TRUSTED_V1"}}}},
		map[string]any{"if": map[string]any{"required": []string{"executionPolicy"}, "properties": map[string]any{"executionPolicy": map[string]any{"const": "COOPERATIVE_TRUSTED_V1"}}}, "then": map[string]any{"required": []string{"agentId", "agentConfigFingerprint"}}},
		map[string]any{"if": map[string]any{"required": []string{"agentId"}, "properties": map[string]any{"agentId": map[string]any{"const": "pi"}}}, "then": map[string]any{"required": []string{"executionPolicy"}, "properties": map[string]any{"executionPolicy": map[string]any{"const": "COOPERATIVE_TRUSTED_V1"}}}},
	}
	return schema
}
func confirmationBackendSchema() any {
	return confirmationObject(map[string]any{"configId": confirmationString(1, 128), "configFingerprint": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}}, "configId", "configFingerprint")
}
func confirmationReadSchema(options bool) any {
	// contentHash belongs to the public read identity itself, so every read tool
	// built from this schema must require it; there is no hash-free read surface
	// that could later be handed commit authority (§7/§8).
	p := map[string]any{"aiSessionId": confirmationString(1, 128), "intentId": confirmationString(1, 128), "version": map[string]any{"type": "integer", "minimum": 1}, "purpose": confirmationEnum("PROJECT_SETUP", "FEATURE_AREA", "TASK", "REVIEW", "EXECUTION", "RECOVERY_PURPOSE"), "contentHash": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}}
	p["requestId"] = confirmationString(1, 128)
	if !options {
		p["replacementRequestId"] = confirmationString(1, 128)
	}
	if options {
		p["startReview"] = map[string]any{"type": "boolean"}
		p["projectId"] = confirmationString(1, 128)
		p["repositoryId"] = confirmationString(1, 128)
	}
	return confirmationObject(p, "aiSessionId", "intentId", "version", "purpose", "contentHash")
}
func confirmationReprepareSchema() any {
	// §10 closed replacement request: only the source identity and this request.
	// A new intentId, version, Decision version, Task/Review identity, topology
	// revision, development-context hash, variants or targets are never inputs.
	return confirmationObject(map[string]any{
		"aiSessionId":       confirmationString(1, 128),
		"sourceIntentId":    confirmationString(1, 128),
		"sourceVersion":     map[string]any{"type": "integer", "minimum": 1},
		"sourceContentHash": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"},
		"requestId":         confirmationString(1, 128),
		"reviewFeedback":    confirmationString(1, 8192),
		"recoveryPurpose":   confirmationRaw(runtimeDTOSchema[recoverystatus.PurposeProposal]()),
	}, "aiSessionId", "sourceIntentId", "sourceVersion", "sourceContentHash", "requestId")
}
func confirmationCommitSchema() any {
	p := confirmationReadSchema(false).(map[string]any)["properties"].(map[string]any)
	delete(p, "replacementRequestId")
	p["requestId"] = confirmationString(1, 128)
	p["action"] = confirmationEnum("SELECT_PROJECT", "CONFIGURE_DEVELOPMENT_CONTEXT", "SELECT_PROJECT_BRANCH", "CREATE_AREA", "CREATE_AREA_AND_REVIEW", "MERGE_FEATURE_AREA", "CREATE_TASK", "CREATE_TASK_AND_REVIEW", "START_REVIEW", "REQUEST_SUPPLEMENTAL_REVIEW", "REVISE", "APPROVE", "APPROVE_AND_START", "CONFIRM_IMPLEMENTATION", "CONFIRM_AND_START_IMPLEMENTATION", "START_APPROVED", "START_DIRECT_FILE", "RECOVER_DIRECT_FILE", "CANCEL_DIRECT_FILE", "RETRY_IMPLEMENTATION", "REQUEST_CHANGES", "ACCEPT", "REJECT", "REQUEST_REVERIFICATION", "COMPLETE_TASK", "DEFER_TASK", "CONTINUE_TASK", c.ConfirmRecoveryPurpose)
	p["variantId"] = confirmationString(1, 128)
	p["selection"] = confirmationObject(map[string]any{"reviewMode": confirmationEnum("UPPER_LLM", "CODING_AGENT", "NONE"), "startReview": map[string]any{"type": "boolean"}, "projectId": confirmationString(1, 128), "repositoryId": confirmationString(1, 128), "developmentRef": confirmationString(1, 256), "developmentContexts": confirmationArray(confirmationObject(map[string]any{"repositoryId": confirmationString(1, 128), "developmentRef": confirmationString(1, 256)}, "repositoryId", "developmentRef"), 1, 128), "backend": confirmationBackendSchema(), "runtimeConfig": confirmationRuntimeSchema(), "verificationWaiver": confirmationObject(map[string]any{"source": confirmationEnum("USER_WAIVED"), "reason": confirmationString(1, 2048)}, "source", "reason")})
	return confirmationObject(p, "aiSessionId", "intentId", "version", "purpose", "requestId", "action", "contentHash", "variantId", "selection")
}
func confirmationTaskBodySchema(initial bool) any {
	p := map[string]any{"featureAreaId": confirmationString(0, 128), "repositoryId": confirmationString(0, 128), "title": confirmationString(1, 256), "problemStatement": confirmationString(1, 16384), "constraints": confirmationArray(confirmationString(0, 2048), 0, 32), "startReview": map[string]any{"type": "boolean"}}
	p["reviewDepth"] = confirmationEnum("FAST", "DEEP")
	plan := confirmationRaw(externalReviewPlanSchema).(map[string]any)
	plan["description"] = confirmationReviewPlanGuidance
	p["reviewPlan"] = plan
	if initial {
		p["featureAreaId"] = map[string]any{"const": ""}
	}
	schema := confirmationObject(p, "featureAreaId", "repositoryId", "title", "problemStatement", "constraints", "startReview")
	schema["allOf"] = []any{map[string]any{"if": map[string]any{"properties": map[string]any{"startReview": map[string]any{"const": true}}}, "then": map[string]any{"required": []string{"reviewDepth", "reviewPlan"}}}}
	return schema
}
func confirmationDisplaySummarySchema() any {
	return confirmationObject(map[string]any{"summary": confirmationString(1, 160), "keyConstraints": confirmationArray(confirmationString(1, 60), 0, 3)}, "summary")
}
func confirmationDisplayMetadataSchema() any {
	p := map[string]any{"aiSessionId": confirmationString(1, 128), "intentId": confirmationString(1, 128), "version": map[string]any{"type": "integer", "minimum": 1}, "purpose": confirmationEnum("PROJECT_SETUP", "FEATURE_AREA", "TASK", "REVIEW", "EXECUTION", "RECOVERY_PURPOSE"), "projectId": confirmationString(0, 128), "status": confirmationEnum("AVAILABLE", "PARTIAL", "UNAVAILABLE"), "objectNameSource": confirmationEnum("PREPARED_CONTENT", "CURRENT_AUTHORIZED_OBJECT", "UNAVAILABLE")}
	p["related"] = confirmationObject(map[string]any{
		"availability":    confirmationEnum("AVAILABLE", "UNAVAILABLE"),
		"kind":            confirmationEnum("REVIEW", "EXECUTION"),
		"id":              confirmationString(1, 128),
		"state":           confirmationString(0, 128),
		"outcome":         confirmationString(0, 128),
		"failureCode":     confirmationString(0, 128),
		"readFailureCode": confirmationString(0, 128),
		"taskDisposition": confirmationString(0, 128), "taskDispositionSummary": confirmationString(0, 8192),
		"reviewRun":         confirmationObject(map[string]any{"id": confirmationString(1, 128), "status": confirmationString(1, 128), "failureCode": confirmationString(0, 128)}, "id", "status"),
		"implementationRun": confirmationObject(map[string]any{"id": confirmationString(1, 128), "status": confirmationString(1, 128), "failureCode": confirmationString(0, 128)}, "id", "status"),
		"verificationRun":   confirmationObject(map[string]any{"id": confirmationString(1, 128), "status": confirmationString(1, 128), "failureCode": confirmationString(0, 128)}, "id", "status"),
	}, "availability", "kind", "id")
	for _, key := range []string{"taskId", "reviewId", "featureAreaId", "repositoryId"} {
		p[key] = confirmationString(1, 128)
	}
	p["decisionId"] = confirmationString(1, 256)
	for _, key := range []string{"projectName", "taskName", "featureAreaName", "repositoryName", "developmentBranch"} {
		p[key] = confirmationString(1, 1024)
	}
	p["reviewOrdinal"] = map[string]any{"type": "integer", "minimum": 1}
	p["repositoryCount"] = map[string]any{"type": "integer", "minimum": 0}
	p["topologyRevision"] = map[string]any{"type": "integer", "minimum": 0}
	p["decisionPurpose"] = confirmationString(1, 256)
	p["presentationIssue"] = confirmationString(1, 256)
	return confirmationObject(p, "aiSessionId", "intentId", "version", "purpose", "projectId", "status", "objectNameSource")
}

func confirmationIntentEligibilitySchema() any {
	return confirmationObject(map[string]any{
		"available":                map[string]any{"type": "boolean"},
		"reasonCode":               confirmationString(1, 128),
		"reasonMessage":            confirmationString(1, 1024),
		"reprepareAllowed":         map[string]any{"type": "boolean"},
		"currentGeneration":        confirmationString(1, 128),
		"preparedGeneration":       confirmationString(1, 128),
		"currentTopologyRevision":  map[string]any{"type": "integer", "minimum": 0},
		"preparedTopologyRevision": map[string]any{"type": "integer", "minimum": 0},
		"currentDecisionVersion":   confirmationString(1, 128),
		"preparedDecisionVersion":  confirmationString(1, 128),
		"attemptState":             confirmationString(0, 128),
		"receiptState":             confirmationString(0, 128),
		// The eligibility result repeats the intent's durable next step so an
		// options-only reader is never told a rejected intent is still available.
		"nextAction":       confirmationEnum("EDIT_SELECTION", "READ_CURRENT_TARGET", "PREPARE_NEW_VERSION", "PREPARE_RECOVERY_STATUS", "READ_ORIGINAL_RECEIPT", "CONTINUE_ORIGINAL_STEPS", "ACCESS_BLOCKED"),
		"retryDisposition": confirmationEnum("NONE", "EDIT_SELECTION_AND_RESUBMIT", "REPREPARE_REQUIRED", "READ_ORIGINAL_RECEIPT"),
	}, "available", "reprepareAllowed", "attemptState", "receiptState")
}
func confirmationVariantSchema(purpose c.Purpose) any {
	base := map[string]any{"id": confirmationString(1, 128), "label": confirmationString(1, 128), "summary": confirmationString(1, 2048), "displaySummary": confirmationDisplaySummarySchema()}
	if purpose == c.ProjectSetup {
		return confirmationObject(base, "id", "label", "summary")
	}
	if purpose == c.Execution {
		base["repositoryTargets"] = repositoryTargetsSchema()
		base["primaryMemberId"] = confirmationString(1, 128)
		base["implementationMode"] = confirmationEnum("CODING_AGENT", "DIRECT_FILE")
		base["workSpec"] = confirmationRaw(WorkSpecInputJSONSchema)
		base["verificationSpec"] = confirmationRaw(VerificationSpecInputJSONSchema)
		base["executionInstructions"] = confirmationRaw(ExecutionInstructionsJSONSchema)
		schema := confirmationObject(base, "id", "label", "summary", "implementationMode", "workSpec", "verificationSpec")
		for _, mode := range []string{"CODING_AGENT", "DIRECT_FILE"} {
			conditions, _ := schema["allOf"].([]any)
			schema["allOf"] = append(conditions, map[string]any{
				"if":   map[string]any{"properties": map[string]any{"implementationMode": map[string]any{"const": mode}}},
				"then": map[string]any{"properties": map[string]any{"workSpec": map[string]any{"properties": map[string]any{"implementationMode": map[string]any{"const": mode}}}}},
			})
		}
		return schema
	}
	simple := confirmationObject(map[string]any{"id": confirmationString(1, 128), "label": confirmationString(1, 128), "summary": confirmationString(1, 2048), "displaySummary": confirmationDisplaySummarySchema()}, "id", "label", "summary")
	base["executorKind"] = confirmationEnum("CODING_AGENT", "UPPER_LLM")
	base["reviewInstruction"] = confirmationString(1, 65536)
	withReview := confirmationObject(base, "id", "label", "summary", "executorKind", "reviewInstruction")
	if purpose == c.FeatureArea || purpose == c.Task {
		return map[string]any{"oneOf": []any{simple, withReview}}
	}
	return withReview
}
func confirmationDecisionVariantSchema() any {
	return confirmationObject(map[string]any{"id": confirmationString(1, 128), "label": confirmationString(1, 128), "summary": confirmationString(1, 2048), "displaySummary": confirmationDisplaySummarySchema()}, "id", "label", "summary")
}
func featureAreaDeliveryPlanSchema() any {
	member := confirmationObject(map[string]any{
		"repositoryId": confirmationString(1, 128), "memberId": confirmationString(1, 128), "memberVersion": map[string]any{"type": "integer", "minimum": 1}, "worktreeId": confirmationString(1, 128), "sourcePath": confirmationString(1, 4096), "sourceBranch": confirmationString(1, 1024), "sourceHead": confirmationString(1, 128), "expectedDevHead": confirmationString(1, 128), "developmentRef": confirmationString(1, 1024), "developmentCycleId": confirmationString(1, 128), "targetPath": confirmationString(1, 4096), "taskCommits": confirmationArray(confirmationString(1, 128), 0, 128),
	}, "repositoryId", "memberId", "memberVersion", "worktreeId", "sourcePath", "sourceBranch", "sourceHead", "expectedDevHead", "developmentRef", "developmentCycleId", "targetPath", "taskCommits")
	return confirmationObject(map[string]any{"completionTaskId": confirmationString(1, 128), "predecessorMergeRequestId": confirmationString(1, 128), "version": map[string]any{"const": 1}, "projectId": confirmationString(1, 128), "featureAreaId": confirmationString(1, 128), "workspaceId": confirmationString(1, 128), "members": confirmationArray(member, 1, 128), "sha256": map[string]any{"type": "string", "pattern": "^[a-f0-9]{64}$"}}, "version", "projectId", "featureAreaId", "workspaceId", "members", "sha256")
}
func confirmationPrepareSchema(purpose c.Purpose) any {
	p := map[string]any{"aiSessionId": confirmationString(1, 128), "requestId": confirmationString(1, 128), "purpose": map[string]any{"const": string(purpose)}, "variants": confirmationArray(confirmationVariantSchema(purpose), 1, 2), "recommendedRuntimeConfig": confirmationRuntimeSchema()}
	p["taskActionGeneration"] = map[string]any{"type": "integer", "minimum": 1, "description": confirmationTaskActionGenerationGuidance}
	required := []string{"aiSessionId", "requestId", "purpose", "action", "variants"}
	var body string
	switch purpose {
	case c.ProjectSetup:
		body = "projectSetup"
		p["action"] = confirmationEnum("SELECT_PROJECT", "CONFIGURE_DEVELOPMENT_CONTEXT", "SELECT_PROJECT_BRANCH")
		p[body] = confirmationObject(map[string]any{"summary": confirmationString(1, 2048)}, "summary")
		p["variants"] = confirmationArray(confirmationVariantSchema(purpose), 1, 1)
		delete(p, "recommendedRuntimeConfig")
	case c.FeatureArea:
		body = "featureArea"
		p["action"] = confirmationEnum("CREATE_AREA", "CREATE_AREA_AND_REVIEW", "MERGE_FEATURE_AREA")
		p[body] = confirmationObject(map[string]any{"title": confirmationString(1, 256), "problemStatement": confirmationString(1, 16384), "initialTask": confirmationTaskBodySchema(true), "featureAreaId": confirmationString(1, 128), "completionTaskId": confirmationString(1, 128), "deliveryPlan": featureAreaDeliveryPlanSchema()})
		p["variants"] = confirmationArray(confirmationVariantSchema(purpose), 1, 2)
	case c.Task:
		body = "task"
		p["action"] = confirmationEnum("CREATE_TASK", "CREATE_TASK_AND_REVIEW")
		p[body] = confirmationTaskBodySchema(false)
	case c.Review:
		body = "review"
		p["action"] = confirmationEnum("START_REVIEW", "REQUEST_SUPPLEMENTAL_REVIEW", "REVISE", "REJECT")
		p[body] = confirmationObject(map[string]any{"taskId": confirmationString(1, 128), "featureAreaId": confirmationString(1, 128), "repositoryId": confirmationString(0, 128), "workspaceId": confirmationString(1, 128), "repositoryTargets": repositoryTargetsSchema(), "reviewId": confirmationString(1, 128), "decisionId": confirmationString(1, 256), "reviewFeedback": confirmationString(1, 8192), "reviewDepth": confirmationEnum("FAST", "DEEP"), "reviewPlan": confirmationRaw(externalReviewPlanSchema), "supplementalPurpose": confirmationString(1, 65536), "expectedTarget": confirmationRaw(TaskReviewRequestTargetSchema)}, "taskId", "featureAreaId")
		p["variants"] = confirmationArray(map[string]any{"oneOf": []any{confirmationVariantSchema(purpose), confirmationDecisionVariantSchema()}}, 1, 2)
	case c.Execution:
		body = "execution"
		p["action"] = confirmationEnum("APPROVE", "APPROVE_AND_START", "CONFIRM_IMPLEMENTATION", "CONFIRM_AND_START_IMPLEMENTATION", "START_APPROVED", "START_DIRECT_FILE", "RECOVER_DIRECT_FILE", "CANCEL_DIRECT_FILE", "RETRY_IMPLEMENTATION", "REQUEST_CHANGES", "ACCEPT", "REJECT", "REQUEST_REVERIFICATION", "COMPLETE_TASK", "DEFER_TASK", "CONTINUE_TASK")
		p[body] = confirmationObject(map[string]any{"repositoryId": confirmationString(1, 128), "baseCommit": map[string]any{"type": "string", "pattern": "^[a-f0-9]{40}$"}, "action": p["action"], "taskId": confirmationString(1, 128), "featureAreaId": confirmationString(1, 128), "reviewId": confirmationString(0, 128), "executionId": confirmationString(0, 128), "taskDefinitionRevisionId": confirmationString(0, 128), "decisionId": confirmationString(0, 256), "workflowId": confirmationString(0, 128), "runtimeEpoch": confirmationString(0, 128), "grantGeneration": map[string]any{"type": "integer", "minimum": 1}, "note": confirmationString(1, 8192), "expectedTarget": confirmationObject(map[string]any{"featureAreaId": confirmationString(1, 128), "taskDefinitionRevisionId": confirmationString(1, 128), "executionId": confirmationString(0, 128), "phase": confirmationEnum("IMPLEMENTATION_READY")}, "featureAreaId", "taskDefinitionRevisionId", "executionId", "phase")}, "action", "taskId", "featureAreaId", "reviewId", "executionId", "taskDefinitionRevisionId", "decisionId")
		// The action below selects the one applicable shape. A union here hides
		// missing implementation fields behind an unhelpful anonymous oneOf error.
		p["variants"] = map[string]any{"type": "array", "minItems": 1, "maxItems": 2}
	}
	schema := confirmationObject(p, append(required, body)...)
	if purpose == c.Task {
		schema["allOf"] = []any{map[string]any{"if": map[string]any{"properties": map[string]any{"action": confirmationEnum("CREATE_TASK_AND_REVIEW")}}, "then": map[string]any{"properties": map[string]any{"task": map[string]any{"properties": map[string]any{"repositoryId": confirmationString(1, 128), "startReview": map[string]any{"const": true}}}, "variants": map[string]any{"minItems": 2, "maxItems": 2, "allOf": []any{map[string]any{"contains": map[string]any{"required": []string{"executorKind"}, "properties": map[string]any{"executorKind": confirmationEnum("UPPER_LLM")}}}, map[string]any{"contains": map[string]any{"required": []string{"executorKind"}, "properties": map[string]any{"executorKind": confirmationEnum("CODING_AGENT")}}}}}}}}}
	}

	if purpose == c.Review {
		decisionForbidden := []any{}
		for _, key := range []string{"repositoryId", "workspaceId", "repositoryTargets", "reviewDepth", "reviewPlan", "supplementalPurpose", "expectedTarget"} {
			decisionForbidden = append(decisionForbidden, map[string]any{"required": []string{key}})
		}
		schema["allOf"] = []any{
			map[string]any{
				"if":   map[string]any{"properties": map[string]any{"action": confirmationEnum("REVISE")}},
				"then": map[string]any{"properties": map[string]any{"review": map[string]any{"required": []string{"taskId", "featureAreaId", "reviewId", "decisionId", "reviewFeedback"}, "not": map[string]any{"anyOf": decisionForbidden}}, "variants": confirmationArray(confirmationDecisionVariantSchema(), 1, 1)}},
			},
			map[string]any{
				"if":   map[string]any{"properties": map[string]any{"action": confirmationEnum("REJECT")}},
				"then": map[string]any{"properties": map[string]any{"review": map[string]any{"required": []string{"taskId", "featureAreaId", "reviewId", "decisionId"}, "not": map[string]any{"anyOf": append(decisionForbidden, map[string]any{"required": []string{"reviewFeedback"}})}}, "variants": confirmationArray(confirmationDecisionVariantSchema(), 1, 1)}},
			},
			map[string]any{
				"if":   map[string]any{"properties": map[string]any{"action": confirmationEnum("START_REVIEW", "REQUEST_SUPPLEMENTAL_REVIEW")}},
				"then": map[string]any{"properties": map[string]any{"review": map[string]any{"required": []string{"taskId", "featureAreaId", "repositoryId", "reviewDepth", "reviewPlan"}}, "variants": confirmationArray(confirmationVariantSchema(purpose), 1, 2)}},
			},
		}
	}
	if purpose == c.FeatureArea {
		schema["allOf"] = []any{
			map[string]any{"if": map[string]any{"properties": map[string]any{"action": confirmationEnum("CREATE_AREA", "CREATE_AREA_AND_REVIEW")}}, "then": map[string]any{"properties": map[string]any{"featureArea": map[string]any{"required": []string{"title", "problemStatement"}}, "variants": confirmationArray(confirmationVariantSchema(purpose), 1, 2)}}},
			map[string]any{"if": map[string]any{"properties": map[string]any{"action": confirmationEnum("MERGE_FEATURE_AREA")}}, "then": map[string]any{"properties": map[string]any{"featureArea": map[string]any{"required": []string{"featureAreaId"}}, "variants": confirmationArray(confirmationVariantSchema(purpose), 1, 1)}}},
		}
	}
	if purpose == c.Execution {
		schema["allOf"] = []any{
			map[string]any{
				"if": map[string]any{"properties": map[string]any{"action": confirmationEnum("CONFIRM_AND_START_IMPLEMENTATION")}},
				"then": map[string]any{"properties": map[string]any{
					"execution": map[string]any{"properties": map[string]any{"reviewId": map[string]any{"const": ""}, "decisionId": map[string]any{"const": ""}, "executionId": map[string]any{"const": ""}, "taskDefinitionRevisionId": confirmationString(1, 128)}, "not": map[string]any{"required": []string{"expectedTarget"}}},
					"variants":  map[string]any{"items": map[string]any{"properties": map[string]any{"implementationMode": map[string]any{"const": "CODING_AGENT"}}}},
				}},
			},
			map[string]any{
				"if":   map[string]any{"properties": map[string]any{"action": confirmationEnum("ACCEPT", "REJECT", "REQUEST_REVERIFICATION", "COMPLETE_TASK", "DEFER_TASK", "CONTINUE_TASK")}},
				"then": map[string]any{"properties": map[string]any{"execution": map[string]any{"required": []string{"action", "taskId", "featureAreaId", "executionId", "taskDefinitionRevisionId", "decisionId"}}, "variants": confirmationArray(confirmationDecisionVariantSchema(), 1, 1)}},
				"else": map[string]any{"properties": map[string]any{"variants": confirmationArray(confirmationVariantSchema(purpose), 1, 2)}},
			},
			map[string]any{
				"if":   map[string]any{"properties": map[string]any{"action": confirmationEnum("START_DIRECT_FILE", "RECOVER_DIRECT_FILE", "CANCEL_DIRECT_FILE")}},
				"then": map[string]any{"properties": map[string]any{"variants": map[string]any{"minItems": 1, "maxItems": 1, "items": map[string]any{"properties": map[string]any{"implementationMode": map[string]any{"const": "DIRECT_FILE"}}}}}},
			},
			map[string]any{
				"if": map[string]any{"properties": map[string]any{"action": confirmationEnum("START_DIRECT_FILE")}},
				"then": map[string]any{"properties": map[string]any{"execution": map[string]any{"properties": map[string]any{
					"workflowId": map[string]any{"const": ""}, "runtimeEpoch": map[string]any{"const": ""},
				}, "not": map[string]any{"required": []string{"grantGeneration"}}}}},
			},
			map[string]any{
				"if": map[string]any{"properties": map[string]any{"action": confirmationEnum("RECOVER_DIRECT_FILE", "CANCEL_DIRECT_FILE")}},
				"then": map[string]any{"properties": map[string]any{"execution": map[string]any{
					"required":   []string{"workflowId", "runtimeEpoch", "grantGeneration"},
					"properties": map[string]any{"workflowId": confirmationString(1, 128), "runtimeEpoch": confirmationString(1, 128)},
				}}},
			},
		}
	}
	return schema
}

// Every confirmation tool uses the same closed result envelope. Domain DTOs
// remain inferred; enum leaves and the supplemental five-field target reuse
// their authoritative contracts rather than a permissive error map.
func confirmationResultSchema() any {
	types := actionTargetSchemaTypes()
	parse := func(value any) *jsonschema.Schema {
		raw, err := json.Marshal(value)
		if err != nil {
			panic(err)
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			panic(err)
		}
		return &schema
	}
	types[reflect.TypeFor[c.DisplaySummary]()] = parse(confirmationDisplaySummarySchema())
	types[reflect.TypeFor[c.DisplayMetadata]()] = parse(confirmationDisplayMetadataSchema())
	types[reflect.TypeFor[c.Purpose]()] = parse(confirmationEnum("PROJECT_SETUP", "FEATURE_AREA", "TASK", "REVIEW", "EXECUTION", "RECOVERY_PURPOSE"))
	types[reflect.TypeFor[c.Outcome]()] = parse(confirmationEnum("REJECTED_NOT_CONSUMED", "CONSUMED", "UNKNOWN"))
	types[reflect.TypeFor[c.NextAction]()] = parse(confirmationEnum("EDIT_SELECTION", "READ_CURRENT_TARGET", "PREPARE_NEW_VERSION", "PREPARE_RECOVERY_STATUS", "READ_ORIGINAL_RECEIPT", "CONTINUE_ORIGINAL_STEPS", "ACCESS_BLOCKED"))
	types[reflect.TypeFor[c.PrepareBusinessOutcome]()] = parse(confirmationEnum("PREPARED", "REJECTED_NOT_PREPARED", "REQUEST_CONFLICT"))
	types[reflect.TypeFor[c.IntentEligibility]()] = parse(confirmationIntentEligibilitySchema())
	types[reflect.TypeFor[c.Failure]()] = parse(confirmationObject(map[string]any{
		"code": confirmationString(1, 128), "reasonCode": confirmationString(1, 128), "source": confirmationEnum("CORE", "DOMAIN", "RUNTIME", "CONFIRMATION", "STORAGE"),
		"stage":   confirmationEnum("INPUT_IDENTITY", "PREPARE", "CONFIRMATION_PREFLIGHT", "UNIQUE_CONSUMPTION", "BUSINESS_STEP", "DISPATCH", "RECEIPT_READBACK"),
		"message": confirmationString(1, 1024), "nextAction": confirmationEnum("EDIT_SELECTION", "READ_CURRENT_TARGET", "PREPARE_NEW_VERSION", "PREPARE_RECOVERY_STATUS", "READ_ORIGINAL_RECEIPT", "CONTINUE_ORIGINAL_STEPS", "ACCESS_BLOCKED"),
	}, "code", "reasonCode", "source", "stage", "message", "nextAction"))
	schema, err := jsonschema.For[c.Result](&jsonschema.ForOptions{TypeSchemas: types})
	if err != nil {
		panic(err)
	}
	// Optional envelope members are omitted, never nullable empty results.
	for _, name := range []string{"intent", "options", "failure", "display"} {
		schema.Properties[name].Type = "object"
		schema.Properties[name].Types = nil
	}
	// Prepare-only booleans are Core-computed and never null: an absent member
	// (read/commit envelopes) simply omits the key.
	for _, name := range []string{"factsComplete", "presentationReady"} {
		schema.Properties[name].Type = "boolean"
		schema.Properties[name].Types = nil
	}
	intent := schema.Properties["intent"]
	intent.Properties["content"].Properties["variants"].Items.Properties["displaySummary"] = parse(confirmationDisplaySummarySchema())
	// Card-required collections are always arrays, never JSON null. intent.steps
	// and content.variants are already required, so pinning the type here makes a
	// null projection fail SDK output validation instead of reaching the Card as
	// steps:null (which the strict Card entry validator rejects).
	for _, collection := range []*jsonschema.Schema{
		intent.Properties["steps"],
		intent.Properties["content"].Properties["variants"],
	} {
		collection.Type = "array"
		collection.Types = nil
	}
	// Options arrays the Card validateOptions gate requires to always be present
	// are advertised array-only, mirroring the intent.steps/content.variants pin:
	// a null must fail SDK output validation instead of reaching the Card as
	// options.projects:null (which validateOptions rejects as unavailable).
	options := schema.Properties["options"]
	options.Properties["taskContinuity"] = parse(confirmationObject(map[string]any{
		"availability":    confirmationEnum("AVAILABLE", "UNAVAILABLE"),
		"projectId":       confirmationString(1, 128),
		"featureAreaId":   confirmationString(1, 128),
		"total":           map[string]any{"type": "integer", "minimum": 0},
		"readFailureCode": confirmationString(1, 128),
		"tasks": confirmationArray(confirmationObject(map[string]any{
			"taskId": confirmationString(1, 128), "title": confirmationString(1, 1024),
			"status": confirmationEnum("ACTIVE", "PLANNING", "READY", "RUNNING"),
			"phase":  confirmationString(0, 128), "stage": confirmationString(0, 1024),
			"summary": confirmationString(0, 4096), "taskDefinitionRevisionId": confirmationString(1, 256),
		}, "taskId", "title", "status", "phase", "stage", "summary"), 0, 20),
	}, "availability", "projectId", "featureAreaId", "tasks", "total"))
	options.Properties["startReview"] = parse(map[string]any{"type": "boolean"})
	for _, selection := range []*jsonschema.Schema{intent.Properties["commit"].Properties["selection"], intent.Properties["submission"].Properties["request"].Properties["selection"]} {
		selection.Properties["startReview"] = parse(map[string]any{"type": "boolean"})
		selection.Properties["reviewMode"] = parse(confirmationEnum("UPPER_LLM", "CODING_AGENT", "NONE"))
	}
	options.Properties["runtimeSelectionMode"] = parse(confirmationEnum("FROZEN_EXECUTION", "EXPLICIT_SELECTION"))
	for _, collection := range []*jsonschema.Schema{
		options.Properties["projects"],
		options.Properties["repositories"],
		options.Properties["branches"],
		options.Properties["variants"],
		options.Properties["developmentContexts"],
		options.Properties["runtimeOptions"],
		options.Properties["backendCandidates"],
	} {
		collection.Type = "array"
		collection.Types = nil
	}
	if devContexts := options.Properties["developmentContexts"]; devContexts != nil && devContexts.Items != nil {
		if rowBranches := devContexts.Items.Properties["branches"]; rowBranches != nil {
			rowBranches.Type = "array"
			rowBranches.Types = nil
		}
	}
	intent.Properties["state"] = parse(confirmationEnum("PENDING", "CONFIRMING", "COMPLETED", "PARTIAL", "UNKNOWN", "REJECTED", "SUPERSEDED", "ABANDONED"))
	intent.Properties["submission"].Type = "object"
	intent.Properties["submission"].Types = nil
	step := intent.Properties["steps"].Items
	step.Properties["state"] = parse(confirmationEnum("PENDING", "UNKNOWN", "COMPLETED", "REJECTED"))
	step.Properties["effect"] = parse(confirmationEnum("NOT_STARTED", "COMMAND_COMPLETED", "RESERVED", "RUNTIME_HANDOFF_ACCEPTED", "NO_EFFECT_PROVEN", "PREPARATION_READY", "UNKNOWN"))
	// Pointer inference must not add JSON null to the exact five-field target.
	intent.Properties["content"].Properties["review"].Properties["expectedTarget"] = parse(confirmationRaw(TaskReviewRequestTargetSchema))
	raw, err := json.Marshal(schema)
	if err != nil {
		panic(err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		panic(err)
	}
	submission := result["properties"].(map[string]any)["intent"].(map[string]any)["properties"].(map[string]any)["submission"].(map[string]any)
	submissionProperties := submission["properties"].(map[string]any)
	submissionProperties["businessOutcome"] = confirmationEnum("COMMITTED", "REJECTED_NO_EFFECT", "REJECTED_NOT_CONSUMED", "OUTCOME_UNKNOWN")
	submissionProperties["consumptionDisposition"] = confirmationEnum("CONSUMED", "NOT_CONSUMED", "UNKNOWN")
	submissionProperties["consumed"] = map[string]any{"type": "boolean"}
	submissionProperties["retryDisposition"] = confirmationEnum("NONE", "EDIT_SELECTION_AND_RESUBMIT", "REPREPARE_REQUIRED", "READ_ORIGINAL_RECEIPT")
	submission["allOf"] = []any{
		map[string]any{"if": map[string]any{"properties": map[string]any{"recorded": map[string]any{"const": true}}}, "then": map[string]any{"properties": map[string]any{"outcome": confirmationEnum("REJECTED_NOT_CONSUMED", "CONSUMED")}}},
		map[string]any{"if": map[string]any{"properties": map[string]any{"recorded": map[string]any{"const": false}}}, "then": map[string]any{"properties": map[string]any{"outcome": confirmationEnum("UNKNOWN")}}},
		map[string]any{"if": map[string]any{"properties": map[string]any{"outcome": map[string]any{"const": "REJECTED_NOT_CONSUMED"}}}, "then": map[string]any{"required": []string{"failure"}, "properties": map[string]any{"receiptRequestId": map[string]any{"const": ""}, "failure": map[string]any{"type": "object"}}}},
		map[string]any{"if": map[string]any{"properties": map[string]any{"outcome": map[string]any{"const": "CONSUMED"}}}, "then": map[string]any{"properties": map[string]any{"receiptRequestId": confirmationString(1, 128)}}},
		// retryDisposition is derived, never negotiated: the wire contract refuses
		// an edit-and-resubmit that is not backed by the exact recorded non-consumption.
		map[string]any{"if": map[string]any{"properties": map[string]any{"retryDisposition": map[string]any{"const": "EDIT_SELECTION_AND_RESUBMIT"}}}, "then": map[string]any{"properties": map[string]any{"recorded": map[string]any{"const": true}, "outcome": confirmationEnum("REJECTED_NOT_CONSUMED"), "nextAction": confirmationEnum("EDIT_SELECTION"), "businessOutcome": confirmationEnum("REJECTED_NOT_CONSUMED"), "consumptionDisposition": confirmationEnum("NOT_CONSUMED"), "consumed": map[string]any{"const": false}, "redispatchSameIntent": map[string]any{"const": false}}}},
	}
	result["dependentRequired"] = map[string]any{"display": []string{"intent"}}
	result["not"] = map[string]any{"required": []string{"intent", "options"}}
	result["anyOf"] = []any{map[string]any{"required": []string{"intent"}}, map[string]any{"required": []string{"options"}}, map[string]any{"required": []string{"failure"}}}
	return result
}
