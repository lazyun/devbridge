package localmcp

import mcpserver "github.com/lazyun/devbridge/internal/mcpserver"
import wp "github.com/lazyun/devbridge/internal/workflowprogression"

// local MCP tool schemas. The input schemas of the twelve product tools are
// the shared product contract and are reused verbatim from the adapter core;
// the descriptions, the workspace_get output schema (which adds the durable
// decision handle), and decision_respond are the Local MCP adapter's own presentation.

const LocalMCPServerInstructions = `Dev Bridge Local MCP Adapter is a durable development control plane that coordinates
AI planning, Codex execution, Git workspaces, user decisions, evidence, and
development state. It serves this local MCP-capable AI client deployment directly over local
MCP; it never routes through the OpenAI Tunnel and never exposes Bridge
publicly.

Its user-facing workflow is:

Project → Project Context → Feature Area → Task → Review gate → Execution → Task Acceptance

Session-first initialization precedes every first-use workflow. With no accurate
Session in this conversation, the model calls session_initialize({requestId}) using
a fresh stable random intent ID. It returns a real durable UNBOUND aiSessionId with
PROJECT_SELECTION_REQUIRED and boundProjectId=null; initialization neither selects a
Project nor runs project preparation or engineering work. If the response is lost,
retry only the original requestId rather than creating a replacement intent.

Candidates in availableProjects are distinct from the boundProjectId. Use project_list
for bounded pagination; an inventory error retains the committed Session and is not an
empty inventory. Zero, one or multiple candidates still require explicit selection.
When the user names an unambiguous Project, initialize first and then bind that same
Session; ambiguous names require a choice. Connector names, recent work and other
conversations cannot supply the selection. This text client does not require a Card.

project_open requires {aiSessionId,projectId,requestId} and only binds the existing
Session. It never creates a second Session, changes an existing Project binding, or
uses a handle exchange. Exact retries preserve requestId and payload; same-Project
confirmation repeats neither selection consumption nor preparation. Preparation
failure does not undo a committed binding and cannot be repaired by repeated binding.
After a lost binding response, first read session_initialize({aiSessionId}); if still
unknown, preserve the original payload/requestId and identity rather than guessing.

For the next user requirement, session_initialize({aiSessionId}) purely reads the
same actual Session; PROJECT_BOUND supplies its real boundProjectId. Then read
workspace_get(aiSessionId) and continue the user's authorized original Core steps.
Missing/invalid Sessions fail, never select the latest or replace the ID. No push,
selectionHandle or manual synchronization is required. Initialization and Project
binding do not approve a Review, create a Task or authorize implementation.

Only an old already-BOUND handle is supported by session_binding_get as a historical
pure read; an old UNBOUND entry cannot create/exchange for a Session. Legacy creating
session_open/project_selection_record/no-Session project_open paths are closed.
Caller/data-instance scope is not proof of a particular conversation. Request IDs are
idempotent keys, not credentials; do not borrow another conversation's known Session.

Project, Feature Area, Document, Review, Task, and Execution state is durable
Project-owned state shared across conversations and adapters; several
conversations and other authorized clients may work with the same Project.

Use natural-language conversation with the user to understand semantics and
to steer. When the durable workflow presents a pending user decision (Task Review gate, per-Execution Task acceptance, runtime approval),
explain the choice to the user in natural language, and when the user decides,
encode exactly their decision by calling decision_respond with kind=REVIEW_APPROVAL or TASK_ACCEPTANCE, the decisionId
from the current workflow state and one of the listed valid options. Never
invent decision options and never resolve a decision without the user's
choice. Review Approval and Task acceptance require a stable requestId and
return a durable exact request receipt. REQUEST_CHANGES also requires the model
to author complete nextWorkSpec and nextVerificationSpec; an incomplete user
intent must not be sent as a Core mutation. For runtime approval use runtime_request_respond with kind=RUNTIME_APPROVAL, omit requestId;
only authoritative entity state is returned. Retry the same decisionId and option
to read back the recorded choice without repeating runtime handoff or delivery.
A changed decisionId is a different choice, with no caller request binding.
Scope expansion actions are legacy-only and cannot be decided.

GPT (or the the local client model) owns semantic intent: complete engineering
instructions are authored by the model and passed to the backend verbatim.
Bridge owns durable workflow state, routing, authorization boundaries, and
deterministic product state. Codex is the trusted engineering backend that
performs the work; its narrative output is AgentClaim while Git, filesystem,
process, command, and other Bridge-observed facts are authoritative Evidence.

Dev Bridge is opt-in for each conversation: enter this workflow only when the
user explicitly asks to use Dev Bridge. Documents persist exactly the content
the user or model explicitly asked to record and start no backend work. When
Bridge-owned asynchronous work is running and no user decision is pending,
yield the interaction; Bridge owns waiting. The user can return later and ask
for the current state; read it with workspace_get instead of guessing. work_list distinguishes semantic
Tasks from Operations. operation_get accepts an exact projected target and closed
include values for detailed results, evidence and runtime requests. feature_area_create
defaults to CREATE_ONLY: record-only requests create no Task, Review or Run, and
CREATE_TASK guidance is not automatic authority. For an explicitly authorized initial
investigation with complete Task, Repository and read-only Review parameters, use
CREATE_AND_REVIEW with a stable requestId to create the Feature Area, visible Task and
first Review admission as one composition. Do not invent missing parameters or stop
between already-authorized mechanical steps. For an existing Feature Area, use
task_create then task_review_start; for an existing exact Task, use task_review_start
without another Task. Review Approval and Task Acceptance remain exact human decision
boundaries. Reads, reconnects and completion notifications never start work, and
nextAction is guidance rather than client auto-execution. ReviewDepth and model
reasoning effort are separate choices. Initial APPROVE
only freezes the definition into IMPLEMENTATION_READY. execution_start starts
implementation or the current candidate START_VERIFICATION reservation.
For a v2 Coding definition with no current Execution, read workspace_get with
the exact taskId and include=["TASK","RUNTIME_CONFIG"]. Present the current
runtimeOptions and backendCandidates, then submit the user's explicit selection
using execution_start with requestId, expectedTarget copied verbatim from
result.taskView.executionStartTarget (including primaryMemberId and
approvedWorksetHash), runtimeConfig and backend. These four
fields belong to one exact start command; preserve them on response-loss replay.
Do not reuse the Review's runtime or combine this branch with executionInstructions.
Existing frozen Executions, legacy starts and Verification retain the unchanged
execution_start branch without new runtimeConfig or backend selection.
Verification completion is not Task Acceptance. WAIT authorizes no write or
continuous polling. Results use result/context/workflow/presentationHint; a
committed result with refreshRequired must not be retried as a new write.
This adapter exposes the shared business schema without UI resources or shows.` + "\n\n" + mcpserver.RuntimeUserDecisionInstructions

var localMCPWorkflowOutputSchema = mcpserver.ExternalWorkflowStateOutputSchema

var localMCPDecisionRespondInputSchema = `{"type":"object","properties":{"executionId":{"type":"string","minLength":1},"featureAreaId":{"type":"string","minLength":1},"taskDefinitionRevisionId":{"type":"string","minLength":1},"taskId":{"type":"string","minLength":1,"maxLength":128},"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"decisionId":{"type":"string","minLength":1,"maxLength":256},"option":{"type":"string","minLength":1,"maxLength":64},"requestId":{"type":"string","minLength":1,"maxLength":128},"nextWorkSpec":` + mcpserver.WorkSpecInputJSONSchema + `,"nextVerificationSpec":` + mcpserver.VerificationSpecInputJSONSchema + `},"required":["aiSessionId","decisionId","option"],"oneOf":[{"properties":{"decisionId":{"pattern":"^REVIEW_APPROVAL:"}},"required":["requestId","taskId"],"not":{"anyOf":[{"required":["nextWorkSpec"]},{"required":["nextVerificationSpec"]}]}},{"properties":{"decisionId":{"pattern":"^TASK_ACCEPTANCE:"},"option":{"enum":["ACCEPT","REJECT","REQUEST_REVERIFICATION"]}},"required":["requestId","taskId","featureAreaId","executionId","taskDefinitionRevisionId"],"not":{"anyOf":[{"required":["nextWorkSpec"]},{"required":["nextVerificationSpec"]}]}},{"properties":{"decisionId":{"pattern":"^TASK_ACCEPTANCE:"},"option":{"const":"REQUEST_CHANGES"}},"required":["requestId","taskId","featureAreaId","executionId","taskDefinitionRevisionId","nextWorkSpec","nextVerificationSpec"]},{"properties":{"decisionId":{"pattern":"^RUNTIME_APPROVAL:"}},"not":{"anyOf":[{"required":["requestId"]},{"required":["nextWorkSpec"]},{"required":["nextVerificationSpec"]}]}}],"additionalProperties":false}`

var localMCPDecisionRespondOutputSchema = `{"type":"object","properties":{"reviewApproval":` + wp.ResolutionJSONSchema + `,"kind":{"type":"string"},"option":{"type":"string"},"guarantee":{"type":"string","enum":["DURABLE_REQUEST_RECEIPT","AUTHORITATIVE_ENTITY_STATE"]},"entityDecision":` + mcpserver.EntityDecisionOutputSchema + `,"resolved":{"type":"boolean"},"accepted":{"type":"boolean"},"replay":{"type":"boolean"},"alreadyResolved":{"type":"boolean"},"conflict":{"type":"boolean"},"resolutionReceipt":{"type":"object","properties":{"decisionId":{"type":"string"},"decisionKind":{"type":"string"},"subjectKind":{"type":"string"},"subjectId":{"type":"string"},"option":{"type":"string"},"requestId":{"type":"string"},"resultKind":{"type":"string"},"resultId":{"type":"string"},"resolvedAt":{"type":"string"}},"required":["decisionId","decisionKind","subjectKind","subjectId","option","requestId","resultKind","resultId","resolvedAt"],"additionalProperties":false},"state":{"type":"string"},"pendingDecisionId":{"type":"string"},"workflowRefreshRequired":{"type":"boolean"},"message":{"type":"string"}},"required":["kind","option","guarantee"],"oneOf":[{"properties":{"kind":{"const":"REVIEW_APPROVAL"},"guarantee":{"const":"DURABLE_REQUEST_RECEIPT"}},"required":["reviewApproval"]},{"properties":{"kind":{"enum":["TASK_ACCEPTANCE"]},"guarantee":{"const":"DURABLE_REQUEST_RECEIPT"}},"required":["resolutionReceipt","resolved","accepted","replay","alreadyResolved","conflict","workflowRefreshRequired"],"not":{"required":["entityDecision"]}},{"properties":{"kind":{"enum":["RUNTIME_APPROVAL"]},"guarantee":{"const":"AUTHORITATIVE_ENTITY_STATE"}},"required":["entityDecision"],"not":{"anyOf":[{"required":["resolutionReceipt"]},{"required":["resolved"]},{"required":["accepted"]},{"required":["replay"]},{"required":["alreadyResolved"]},{"required":["conflict"]},{"required":["workflowRefreshRequired"]}]}}],"additionalProperties":false}`
