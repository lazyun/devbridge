package mcpserver

import wp "github.com/lazyun/devbridge/internal/workflowprogression"

// externalWorkflowOutputSchema is the single closed GPT-facing state contract.
// Detailed Review and Execution bodies remain deliberate detail reads.
const externalWorkflowOutputSchema = `{
  "type":"object",
  "properties":{
    "directFile":{"type":"object","properties":{"enabled":{"type":"boolean"},"projectId":{"type":"string"},"repositoryId":{"type":"string"},"featureAreaId":{"type":"string"},"worktreeId":{"type":"string"},"workspaceId":{"type":"string"},"workflowId":{"type":"string"},"protocolVersion":{"type":"integer","enum":[1,2]},"runtimeEpoch":{"type":"string"},"grantGeneration":{"type":"integer","minimum":1},"repositoryTargets":` + repositoryTargetsJSONSchema + `},"required":["enabled"],"additionalProperties":false},
    "agentExecution":` + agentExecutionProjectionSchema + `,
    "workflowGuidance":` + wp.GuidanceJSONSchema + `,"currentState":{"type":"string"},"nextAction":` + wp.ActionJSONSchema + `,
    "state":{"type":"string","enum":["FEATURE_AREA_IDLE","FEATURE_AREA_REQUIRED","RUNTIME_APPROVAL_REQUIRED","REVIEW_REQUIRED","REVIEWING","REVIEW_MATERIALIZING","REVIEW_APPROVAL_PENDING","IMPLEMENTATION_READY","TASK_NO_RUN_SUCCESSOR_REQUIRED","TASK_IMPLEMENTATION_PREPARING","IMPLEMENTING","VERIFICATION_READY","VERIFYING","ACCEPTANCE_PENDING","TASK_DELIVERING","TASK_RECOVERY_REQUIRED","SUCCEEDED","FAILED","CANCELED","FEATURE_AREA_RETIRING","RETIRED","DECOMPOSED","RECOVERY_REQUIRED","FEATURE_AREA_BUSY","GIT_FINALIZATION_PENDING","PROJECT_SELECTION_REQUIRED","PROJECT_READY"]},
    "summary":{"type":"string","maxLength":4096},
    "current":{"type":"object","properties":{
      "project":{"type":"object","properties":{
        "id":{"type":"string"},"name":{"type":"string"},
        "developmentContext":` + DevelopmentContextOutputSchema + `,
        "repositories":{"type":"array","maxItems":100,"items":{"$ref":"#/$defs/namedHandle"}},
        "context":{"type":"object","properties":{
          "status":{"type":"string","enum":["PREPARING","READY","BLOCKED"]},"usable":{"type":"boolean"},"refreshStatus":{"type":"string","enum":["IDLE","RUNNING","FAILED"]},"summary":{"type":"string"},"guidance":{"type":"string","enum":["FOUND","NOT_FOUND","UNKNOWN"]},
          "modules":{"type":"array","maxItems":100,"items":{"type":"object","properties":{"key":{"type":"string"},"name":{"type":"string"},"responsibility":{"type":"string"},"repository":{"type":"string"},"repositoryId":{"type":"string"},"productionRoots":{"type":"array","maxItems":128,"items":{"type":"string"}},"entrypoints":{"type":"array","maxItems":128,"items":{"type":"string"}},"relatedModules":{"type":"array","maxItems":128,"items":{"type":"string"}}},"required":["key","name","responsibility","repository","repositoryId","productionRoots","entrypoints","relatedModules"],"additionalProperties":false}}
        },"required":["status","usable","refreshStatus","summary","guidance","modules"],"additionalProperties":false}
      },"required":["id","name","repositories","context"],"additionalProperties":false},
      "latestTask":{"type":"object","properties":{"id":{"type":"string"},"status":{"type":"string"},"phase":{"type":"string"}},"required":["id","status","phase"],"additionalProperties":false},"featureArea":{"anyOf":[{"type":"null"},{"$ref":"#/$defs/featureArea"}]},
      "review":{"anyOf":[{"type":"null"},{"type":"object","properties":{"id":{"type":"string","pattern":"^review_[A-Za-z0-9_-]+$"},"requestId":{"type":"string"},"runId":{"type":"string"},"reviewBaseline":{"type":"string"},"reviewDepth":{"type":"string","enum":["FAST","DEEP"]},"reviewDisposition":{"type":"string","enum":["APPROVAL_READY","FURTHER_REVIEW_REQUIRED","BLOCKED"]},"state":{"type":"string","enum":["STARTING","RUNNING","SUCCEEDED","FAILED","BLOCKED","CANCELED"]},"summary":{"type":"string"},"resultAvailable":{"type":"boolean"}},"required":["id","reviewDepth","state","summary","resultAvailable"],"additionalProperties":false}]},
      "task":{"anyOf":[{"type":"null"},{"$ref":"#/$defs/task"}]},
      "execution":{"anyOf":[{"type":"null"},{"$ref":"#/$defs/execution"}]},
      "result":{"anyOf":[{"type":"null"},{"type":"object","properties":{"executionId":{"type":"string"},"status":{"type":"string","enum":["COMPLETED","FAILED","REJECTED","CANCELED"]},"summary":{"type":"string"},"inspectable":{"type":"boolean"}},"required":["executionId","status","summary","inspectable"],"additionalProperties":false}]}
    },"required":["project","featureArea","review","task","execution","result"],"additionalProperties":false},
    "availableFeatureAreas":{"type":"array","maxItems":100,"items":{"type":"object","properties":{"id":{"type":"string"},"title":{"type":"string"}},"required":["id","title"],"additionalProperties":false}},
    "availableModels":{"type":"array","maxItems":8,"items":` + modelIDSchema + `},
    "availableReasoningEfforts":{"type":"array","maxItems":8,"items":` + reasoningEffortSchema + `},
    "capabilities":{"type":"object","properties":{"canActivateFeatureArea":{"type":"boolean"},"canDecomposeFeatureArea":{"type":"boolean"},"canRetireFeatureArea":{"type":"boolean"},"canResetAgentContext":{"type":"boolean"},"canCreateFeatureArea":{"type":"boolean"},"canStartReview":{"type":"boolean"},"canCreateTask":{"type":"boolean"},"canExecute":{"type":"boolean"},"canCancelExecution":{"type":"boolean"},"canRequestAnotherReview":{"type":"boolean"},"canInspectResult":{"type":"boolean"},"canResolveDecision":{"type":"boolean"},"hasPendingDecision":{"type":"boolean"},"decisionInteractionAvailable":{"type":"boolean"}},"required":["canActivateFeatureArea","canDecomposeFeatureArea","canRetireFeatureArea","canResetAgentContext","canCreateFeatureArea","canStartReview","canCreateTask","canExecute","canCancelExecution","canRequestAnotherReview","canInspectResult","canResolveDecision","hasPendingDecision","decisionInteractionAvailable"],"additionalProperties":false},
    "pendingDecision":{"anyOf":[{"type":"null"},{"type":"object","properties":{"decisionId":{"type":"string","minLength":1,"maxLength":256},"decisionKind":{"type":"string","enum":["REVIEW_APPROVAL","TASK_ACCEPTANCE","RUNTIME_APPROVAL"]},"subjectKind":{"type":"string","enum":["TASK_REVIEW_GATE","EXECUTION_PROPOSAL","EXECUTION"]},"subjectId":{"type":"string"},"subjectName":{"type":"string"},"summary":{"type":"string"},"options":{"type":"array","minItems":2,"maxItems":16,"items":{"type":"object","properties":{"option":{"type":"string"},"meaning":{"type":"string"}},"required":["option","meaning"],"additionalProperties":false}}},"required":["decisionId","decisionKind","subjectKind","subjectId","subjectName","summary","options"],"additionalProperties":false}]},
    "blockers":{"type":"array","maxItems":16,"items":{"type":"object","properties":{"code":{"type":"string"},"category":{"type":"string"},"summary":{"type":"string"},"cause":{"type":"string"},"observedFacts":{"type":"array","maxItems":16,"items":{"type":"string"}},"retryable":{"type":"boolean"}},"required":["code","category","summary","cause","observedFacts","retryable"],"additionalProperties":false}},
    "guidance":{"type":"array","maxItems":8,"items":{"type":"string","maxLength":4096}},
    "recovery":{"type":"array","maxItems":16,"items":{"type":"object","properties":{"code":{"type":"string"},"summary":{"type":"string"},"condition":{"type":"string"}},"required":["code","summary","condition"],"additionalProperties":false}}
  },
  "required":["state","summary","current","availableFeatureAreas","availableModels","availableReasoningEfforts","capabilities","pendingDecision","blockers","guidance","recovery"],
  "additionalProperties":false,
  "$defs":{
    "namedHandle":{"type":"object","properties":{"id":{"type":"string"},"name":{"type":"string"}},"required":["id","name"],"additionalProperties":false},
    "featureArea":{"type":"object","properties":{"id":{"type":"string"},"title":{"type":"string"},"status":{"type":"string","enum":["DISCOVERING","ACTIVE","DECOMPOSED","RETIRING","RETIRED"]},"currentWorkspaceId":{"type":"string"},"currentReviewId":{"type":"string","pattern":"^review_[A-Za-z0-9_-]+$"},"activeReviewId":{"type":"string","pattern":"^review_[A-Za-z0-9_-]+$"},"latestReviewId":{"type":"string","pattern":"^review_[A-Za-z0-9_-]+$"},"reviewStatus":{"type":"string","enum":["NOT_STARTED","STARTING","RUNNING","SUCCEEDED","FAILED","BLOCKED","CANCELED"]},"reviewResultAvailable":{"type":"boolean"},"agentOperation":{"type":"object","properties":{"state":{"type":"string","enum":["READY","BUSY","RECOVERY_REQUIRED","UNAVAILABLE"]},"message":{"type":"string"},"operationKind":{"type":"string"},"operationId":{"type":"string"},"runId":{"type":"string"},"agentConversationId":{"type":"string"}},"required":["state","message"],"additionalProperties":false}},"required":["id","title","status","reviewStatus","reviewResultAvailable"],"additionalProperties":false},
	"task":{"type":"object","properties":{"requiredReviewDepth":{"type":["string","null"],"enum":["FAST","DEEP",null]},"allowedReviewDepths":{"type":"array","items":{"type":"string","enum":["FAST","DEEP"]}},"id":{"type":"string"},"title":{"type":"string"},"workKind":{"anyOf":[{"type":"null"},{"type":"string","enum":["FEATURE","BUG_FIX","REFACTOR","MAINTENANCE","TEST_CHANGE","DOCUMENTATION","OTHER"]}]},"model":{"anyOf":[{"type":"null"},` + modelIDSchema + `]},"reasoningEffort":{"anyOf":[{"type":"null"},` + reasoningEffortSchema + `]},"status":{"type":"string","enum":["ACTIVE","SUCCEEDED","CANCELED","FAILED"]},"phase":{"type":"string"},"confirmationRequired":{"type":"boolean"}},"required":["id","title","status","confirmationRequired"],"additionalProperties":false},
	"execution":{"type":"object","properties":{"cancellation":` + ExecutionCancellationProjectionSchema + `,"id":{"type":"string"},"status":{"type":"string","enum":["PENDING","CREATED","PREPARING","RUNNING","WAITING_APPROVAL","COLLECTING","COMPLETED","FAILED","INFRASTRUCTURE_FAILED","REJECTED","CANCELED","EXECUTING","VERIFYING","FINALIZING","UNKNOWN","UNKNOWN_ORPHANED","UNAVAILABLE"]},"userAuthorizationRequired":{"type":"boolean"}},"required":["id","status","userAuthorizationRequired"],"additionalProperties":false}
  }
}`

const projectBindingResultSchema = `{"type":"object","properties":{"aiSessionId":{"type":"string"},"boundProjectId":{"type":"string"},"state":{"type":"string","const":"PROJECT_BOUND"},"isNewBinding":{"type":"boolean"},"receipt":` + sessionRequestReceiptSchema + `,"preparationStatus":{"type":"string","enum":["COMPLETED","FAILED","NOT_REPEATED"]},"preparationError":{"type":"string"},"project":` + sessionProjectIdentitySchema + `},"required":["aiSessionId","boundProjectId","state","isNewBinding","receipt","preparationStatus","project"],"additionalProperties":false}`
const externalProjectOpenOutputSchema = `{"type":"object","properties":{"result":` + projectBindingResultSchema + `,"context":` + sessionIdentityContextSchema + `,"workflow":` + sessionIdentityWorkflowSchema + `,"presentationHint":` + surfacePresentationHintSchema + `},"required":["result","context","workflow","presentationHint"],"additionalProperties":false}`

var externalReviewOutputSchema = `{
  "type":"object",
  "properties":{
    "review":{"type":"object","properties":{"executorKind":{"type":"string","enum":["UPPER_LLM","CODING_AGENT"]},"executorMetadata":{"type":"object","properties":{"model":{"type":"string"},"provenance":{"type":"string"}},"required":["provenance"],"additionalProperties":false},"upperAccess":` + upperReviewAccessOutputSchema + `,"reportId":{"type":"string"},"stageLabel":{"type":"string"},"reviewDepth":{"type":"string","enum":["FAST","DEEP"]},"reviewDisposition":{"type":"string","enum":["APPROVAL_READY","FURTHER_REVIEW_REQUIRED","BLOCKED"]},"taskId":{"type":"string"},"ordinal":{"type":"integer","minimum":0},
      "id":{"type":"string"},
      "requestId":{"type":"string"},
      "projectId":{"type":"string"},
      "repositoryId":{"type":"string"},
      "runId":{"type":"string"},
      "operationKind":{"type":"string","const":"REVIEW"},
      "featureArea":{"type":"object","properties":{"id":{"type":"string"},"title":{"type":"string"}},"required":["id","title"],"additionalProperties":false},
      "state":{"type":"string","enum":["STARTING","RUNNING","SUCCEEDED","FAILED","BLOCKED","CANCELED"]},
      "summary":{"type":"string"},
      "resultAvailable":{"type":"boolean"},
      "model":{"anyOf":[{"type":"null"},` + modelIDSchema + `]},
      "reasoningEffort":{"anyOf":[{"type":"null"},` + reasoningEffortSchema + `]},
      "reviewInstruction":{"type":"string","maxLength":65536},
      "admittedAt":{"type":"string"},
      "completedAt":{"type":"string"},
      "completedTransition":{"type":"boolean"},
      "usage":` + tokenUsageOutputSchema + `,
      "focus":{"type":"object","properties":{"mode":{"type":"string","enum":["FOCUSED","REPOSITORY"]},"summary":{"type":"string"},"primaryModules":{"type":"array","maxItems":128,"items":{"type":"string"}},"supportingModules":{"type":"array","maxItems":128,"items":{"type":"string"}},"productionRoots":{"type":"array","maxItems":128,"items":{"type":"string"}}},"required":["mode","summary","primaryModules","supportingModules","productionRoots"],"additionalProperties":false},
      "blockers":{"type":"array","items":{"type":"string"}},
      "failure":{"type":"object","properties":{"code":{"type":"string"},"category":{"type":"string"},"summary":{"type":"string"},"cause":{"type":"string"},"observedFacts":{"type":"array","maxItems":16,"items":{"type":"string"}},"retryable":{"type":"boolean"}},"required":["code","category","summary","cause","observedFacts","retryable"],"additionalProperties":false},
      "recovery":{"type":"string"}
    },"required":["id","reviewDepth","featureArea","state","summary","resultAvailable","completedTransition","usage","focus","blockers"],"additionalProperties":false},
    "result":{"type":"object","properties":{
      "reviewDisposition":{"type":"string","enum":["APPROVAL_READY","FURTHER_REVIEW_REQUIRED","BLOCKED"]},
      "engineeringReview":{"type":"string","maxLength":65536},
      "agentClaim":{"type":"object","properties":{"available":{"type":"boolean"},"message":{"type":"string","maxLength":65536}},"required":["available"],"additionalProperties":false}
    },"required":["engineeringReview","agentClaim"],"additionalProperties":false},
    "observedEvidence":{"type":"object","properties":{"authorizationStatus":{"type":"string","enum":["GRANTED","UNKNOWN"]},"repositoryInspectionObserved":{"type":"boolean"},"observationBoundary":{"type":"string","const":"BRIDGE_HOST_TOOLS_ONLY"},"successfulHostToolOperations":{"type":"integer","minimum":0},"forbiddenHostToolOperations":{"type":"integer","minimum":0},"failedHostToolOperations":{"type":"integer","minimum":0}},"required":["authorizationStatus","repositoryInspectionObserved","observationBoundary","successfulHostToolOperations","forbiddenHostToolOperations","failedHostToolOperations"],"additionalProperties":false},
    "repositoryProvenance":{"type":"object","properties":{"repositoryId":{"type":"string"},"reviewBaseline":{"type":"string"},"repositoryHEADAtAdmission":{"type":"string"},"branchKind":{"type":"string","enum":["BRANCH","DETACHED","UNKNOWN"]},"branchRef":{"type":"string"},"workingTreeDirtyAtAdmission":{"anyOf":[{"type":"null"},{"type":"boolean"}]},"inspectionMode":{"type":"string","const":"FEATURE_AREA_WORKSPACE"},"capturedAt":{"type":"string"}},"required":["repositoryId","reviewBaseline","repositoryHEADAtAdmission","branchKind","workingTreeDirtyAtAdmission","inspectionMode","capturedAt"],"additionalProperties":false},
    "repositoryDrift":` + repositoryDriftOutputSchema + `
  },
  "required":["review","observedEvidence"],
  "additionalProperties":false
}`

// Shared Core execution facts; authorization remains with semantic owners.
const agentExecutionProjectionSchema = `{"type":"object","properties":{"userAuthorizationRequired":{"type":"boolean"},"executionId":{"type":"string"},"projectId":{"type":"string"},"taskId":{"type":"string"},"featureAreaId":{"type":"string"},"kind":{"type":"string"},"semanticOwnerKind":{"type":"string"},"semanticOwnerId":{"type":"string"},"executionProposalId":{"type":"string"},"provenanceKind":{"type":"string"},"runId":{"type":"string"},"runIds":{"type":"array","items":{"type":"string"}},"stage":{"type":"string"},"status":{"type":"string"},"agentStatus":{"type":"string"},"domainStatus":{"type":"string"},"domainResult":{"type":"string"},"backendDisposition":{"type":"string"},"cancellationState":{"type":"string"},"recoveryRequired":{"type":"boolean"},"finalizationPending":{"type":"boolean"},"readOnly":{"type":"boolean"},"updatedAt":{"type":"string"}},"required":["executionId","projectId","taskId","kind","semanticOwnerKind","semanticOwnerId","provenanceKind","runIds","stage","status","agentStatus","backendDisposition","recoveryRequired","finalizationPending","readOnly","updatedAt"],"additionalProperties":false}`
