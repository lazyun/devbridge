package mcpserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/controlapi"
	"github.com/lazyun/devbridge/internal/featurearea"
	productreview "github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type featureAreaReviewPlanResolution struct {
	repositoryID    string
	primaryModuleID string
	scope           run.ReviewScope
	plan            run.ReviewPlan
	blockers        []string
}

func externalReviewStartBlocked(featureAreaValue featurearea.FeatureArea, input externalReviewInput, blockers ...string) externalReviewOutput {
	mode := input.ReviewPlan.Mode
	if mode == "" {
		mode = string(productreview.FocusFocused)
	}
	return externalReviewOutput{Review: externalReviewProduct{
		ExecutorKind: input.ExecutorKind.Effective(), ReviewDepth: productreview.ReviewDepth(input.ReviewDepth), RequestID: input.RequestID, ProjectID: featureAreaValue.ProjectID, RepositoryID: input.RepositoryID, OperationKind: "REVIEW", Topic: externalTopicReference{ID: featureAreaValue.ID, Title: boundedUTF8(featureAreaValue.Title, 256)}, State: "BLOCKED", Model: input.Model, ReasoningEffort: input.ReasoningEffort, ReviewInstruction: boundedUTF8(input.ReviewInstruction, 64<<10),
		Summary: "The Feature Area Review could not start with the requested production context.", ResultAvailable: false,
		Focus:    externalReviewFocus{Mode: mode, Summary: "Requested Feature Area Review focus could not be resolved.", PrimaryModules: []string{}, SupportingModules: []string{}, ProductionRoots: []string{}},
		Blockers: append([]string(nil), blockers...), Failure: &externalBlocker{Code: "REVIEW_CONTEXT_UNAVAILABLE", Category: "REVIEW", Summary: "The requested Feature Area Review context is unavailable.", Cause: "The Repository, configuration, or Feature Area Review focus could not be resolved mechanically.", ObservedFacts: append([]string(nil), blockers...), Retryable: true}, Recovery: "Resolve the reported context blocker, then start another Feature Area Review for the same Feature Area.", Usage: TokenUsageOutput{Status: string(run.TokenUsageUnavailable)},
	}, ObservedEvidence: externalReviewObservedEvidence{AuthorizationStatus: "UNKNOWN", ObservationBoundary: "BRIDGE_HOST_TOOLS_ONLY"}}
}

func resolveTopicReviewPlan(ctx context.Context, service BridgeService, input externalReviewInput) (featureAreaReviewPlanResolution, error) {
	plan := controlapi.InitialReviewPlan{Mode: input.ReviewPlan.Mode}
	for _, selected := range input.ReviewPlan.SelectedModules {
		plan.SelectedModules = append(plan.SelectedModules, run.ReviewModuleSelection{Key: selected.Key, Role: run.ReviewModuleRole(selected.Role)})
	}
	value, err := controlapi.ResolveInitialReviewPlan(ctx, service, input.AISessionID, input.RepositoryID, plan)
	return featureAreaReviewPlanResolution{plan: value.Plan, scope: value.Scope, blockers: value.Blockers, repositoryID: value.RepositoryID, primaryModuleID: value.PrimaryModuleID}, err
}

// ExternalServerInstructions contains only stable, global product rules.
const ExternalServerInstructions = `Session-first initialization is evaluated before every first-use display rule.
Selecting the Dev Bridge connector (for example, "use devbridge-test") is not selecting
a Project or authorizing engineering work. Apply these rules in order:

1. If this conversation has no accurate real aiSessionId, the MODEL directly calls
session_initialize({requestId}) with a new stable random initialization intent ID.
This creates one real UNBOUND Session and returns that exact ID in structuredContent,
with state=PROJECT_SELECTION_REQUIRED and boundProjectId=null. It does not choose a
Project, prepare a workspace, create a Task/Review, or start an agent. If the creation
response is lost, reuse the original requestId; never silently use a new requestId.

2. With the returned Session, explain the availableProjects candidates in conversation
and ask the user to choose. Zero, one and multiple Projects all remain candidates;
a single candidate is not authorization to bind it. On an explicit request for a Card,
call project_selection_show({aiSessionId,presentationRequestId}) to display those facts
and a conversational selection hint. Card has no selection or binding controls and
never calls session_initialize, creates a handle or exchanges identities. The model
can use project_list when more inventory is needed.
Inventory errors are independent of the committed Session; retain its ID and report
the inventory error instead of declaring no Projects or creating a replacement.

3. On explicit Project choice, call project_open({aiSessionId,projectId,requestId})
with the SAME Session and a stable binding intent ID. Even an explicitly named unique
Project requires initialization first, then binding this Session; ambiguous names need
a choice, never the first match. project_open cannot create another Session. Repeating
the same binding returns its original facts, and binding another Project conflicts.
Binding and any preparation failure remain separate: a committed binding is not
PROJECT_READY or permission to rerun preparation through repeated project_open.

4. For a later user requirement, call session_initialize({aiSessionId}) when current
binding is needed. This is a pure read of that known Session. PROJECT_BOUND returns
its actual boundProjectId; then workspace_get(aiSessionId) obtains the necessary Project
context and original Core interfaces perform the user's already-authorized steps.
No selectionHandle, host metadata or successful context push is needed. A missing or
invalid Session is a real blocker: never guess recent Sessions, replace IDs, or ask
the user to copy internal identifiers. A lost binding response is first resolved by
reading this same Session; any explicit retry must preserve the original project_open
requestId and payload. UNBOUND alone does not prove an earlier request was never accepted.

5. Presentation: project_selection_show starts EXPANDED and only reads its supplied
Session. A bound workspace_show defaults COLLAPSED; an explicit show/open uses EXPANDED.
Refresh the existing entry in place instead of inserting a Card for every step.
Analysis reads and mutations are SILENT; present the final accurate state once. Exact
Area/Task/Decision/Operation targets remain explicit and cannot be inferred from recent
work, candidate inventory, Console state or stale widget data. Display, refresh, remount
never initialize or bind a Session. Card navigation changes only the component's view;
it does not select a conversational command target or push model context. No sync gate.

For a modification or continuation request, route FeatureArea first. Verify this
conversation's actual Session and bound Project, then use work_list(kind=FEATURE_AREA,
limit,offset) to locate the responsible area, work_list(kind=TASK,featureAreaId,limit,
offset) to narrow semantic Tasks including terminal history, and work_list(kind=OPERATION,
featureAreaId,taskId,limit,offset) for their Review/Execution inventory. workspace_get
include NAVIGATION is a view-oriented snapshot, not a bounded search substitute. Lists
have hasMore and nextOffset: an incomplete page or read error never proves absence.
Names and keywords only narrow candidates. Never default to the first, newest, unique,
currently displayed or last browsed Task. If the user gives an exact Task/Review ID,
validate its ownership directly instead of scanning all areas.

Separate engineering Review sufficiency (GPT judgement), the user's current authorization,
and deterministic Core execution eligibility. None proves either of the others. After
narrowing candidates, workspace_get(exact taskId,include=[TASK]) returns taskSources with
current Review/Execution/revision pointers and the selected immutable definition's actual
approval source. An optional definitionRevisionId selects an exact historical revision
of that same Task. operation_get(exact target,include=[SOURCE]) returns Review summary,
disposition and decision receipt plus current Task sources, or an Execution's exact
adopted definition; add RESULT/EVIDENCE only when its full report/evidence is needed.
Prefer the current definition's source Review and later explicit revisions over a
convenient older conclusion. Review completion, APPROVAL_READY and user APPROVE are
separate facts. SOURCE failures are unavailable evidence, never an empty successful
approval. Missing historical source records remain unknown; do not infer approval.

When source or current code needs checking, locate necessary production files with
existing bounded search, then use repository_file_read through the Project/Repository
read authority. Do not create a Review, Run or Inspection, enable Direct File, revive
an old grant or prepare a workspace merely to read. Keep actual source kind, repository/
workspace identity, observed HEAD or explicit commit and content SHA-256 distinct.
Continue a file range with expectedContentSHA256 where applicable; do not combine
changed fragments or claim ordinary cross-file reads are atomic. HEAD/path differences
are evidence for GPT, never an automatic NEW_REVIEW_REQUIRED rule. Stop once evidence
is sufficient; broad uncertain cross-module questions may justify a formal Review.

Explain the routing conclusion briefly: responsible FeatureArea/Task, the source Review,
why it is sufficient or which incremental question is missing, and the exact next action.
CONTINUE_EXISTING_TASK is for the same ACTIVE Task and sufficient prior review. Read
actual Execution/Run/finalization facts, not Task.phase alone. An eligible undispatched
candidate is reused. Active or unresolved UNKNOWN writers block a second writer; do not
automatically cancel/recover them. For a safely finished failed/interrupted predecessor,
use the exact TASK_ACCEPTANCE RETRY_IMPLEMENTATION option when Core offers it, preserve
the requestId/payload, read the successor receipt, then use execution_start on the exact
confirmed successor. Unchanged canonical instructions reuse the approved revision;
explicit instruction-only corrections use executionInstructions and the immutable
revision handoff. Substantive user-authorized changes use full REQUEST_CHANGES successor
specifications, not a hidden prompt override. No new Review is implied by failure.
If Core supplies requiredInputs=["nextRunRuntimeConfig"], the safe continuation requires
an explicit complete allowed model/reasoning/Codex Fast Mode selection. Otherwise an
omitted configuration inherits the predecessor exactly; never substitute Review defaults.
A legacy UNKNOWN implementation channel does not authorize Coding Agent retry.

REVIEW_EXISTING_TASK means the same goal needs an incremental Review. With explicit user
authorization, read the shared REQUEST_TASK_REVIEW action and copy its exact observed
phase, revision, current Review/Execution and pending Decision (empty IDs assert absence).
Call task_review_request with the full purpose/new questions and a stable requestId.
Core safely supersedes old candidate/acceptance and this Task's old file authority in
one transition to REVIEW_REQUIRED; no Review or Run has started yet. Then call the
original task_review_start on this same Task with explicit executorKind/reviewDepth and
a complete incremental reviewInstruction. Two internal calls do not require two user
confirmations. A replay receipt describes a historical transition: re-read current
identity before a new start; never restore its old candidate or overwrite newer work.

IMPLEMENTATION_READY without a reservation and implementation/verification phases with
an exact safely finalized Execution can request this transition. CREATED/PREPARING Run,
active writer, unsettled UNKNOWN, file I/O or preparation block; do not cancel/recover
automatically. REVIEW_REQUIRED already has a pending-start fact, not a running Review;
REVIEWING does not admit a concurrent Review. REVIEW_APPROVAL_PENDING uses the original
REVISE decision. Supplemental failure or further revision never restores old dispatch.
A new successful Review needs its own user approval and immutable definition; its new
Execution identity is distinct from previous runs. Preserve actual Review workspace
provenance separately from development-ref baseline, and do not reset/merge to hide drift.
This supplemental route is optional: a sufficient existing Review still permits eligible
RETRY_IMPLEMENTATION without a new Review. CREATE_TASK_IN_AREA is for independent
goals or new work after a terminal Task: preserve exact source Review/Task/version IDs
in the authored problem statement or constraints, but do not borrow their approval or
reopen that Task. CREATE_FEATURE_AREA requires no suitable existing responsibility and
a clear user goal, not pagination, a failed read or relevance to multiple areas. Choose
one primary area and read related areas as context; split only independent deliverables.
NEED_MORE_EVIDENCE means a necessary bounded read or one material clarification, not
mandatory ScopeAssessment approval or an automatic new container.

A clear authorized "continue implementation" does not need mechanical second confirmation;
"analyze whether we can continue" authorizes reads/analysis only. Keep executable prompts
self-contained, incorporate decided choices and retain real technical fences. DIRECT_FILE
uses its actual approved file authority and never the Coding Agent retry/start path.
Use explicit or documented inherited runtime configuration and report the actual model,
reasoning effort and Fast Mode; do not borrow Review settings or pick a model by position.
Card remains read-only and its browsing never supplies a conversational write target.

An explicit user message "Analysis review result <reviewId>" requests read-only analysis
of that complete, exact Review ID, including a historical Review. Use this conversation's
accurate existing aiSessionId; if binding facts are needed, read only
session_initialize({aiSessionId}). For this exact-object analysis request, do not use
first-use Session creation or Project binding as a fallback. If the real Session or
its binding is unavailable or the target conflicts with its Project, report that blocker.
Call operation_get({aiSessionId,target:{kind:"REVIEW",operationId:<reviewId>},
include:["RESULT"]}); include EVIDENCE only when necessary. Do not shorten or replace
the supplied Review ID with the latest Review, guess an owner Task or Run, switch
Project/Session, create or mutate workflow objects, or automatically present a new Card.
Analyze the returned actual result, explaining findings, proposed scope, risks and
limitations; distinguish unavailable results from successful findings. This explicit
user-requested analysis is permitted even when GPT authored that report: the rule against
automatically requesting repeated analysis of one's own submitted Review does not block it.

Historical compatibility is read-only: an exact old BOUND handle can return its original
Session via session_binding_get, then use session_initialize(aiSessionId). An old UNBOUND
handle requires the new model-led initialization flow; it cannot create or exchange for
a Session. Old session_open, project_selection_record and no-Session project_open are
closed creation paths. Do not expand host mappings or infer a binding from history.

Caller scope is enforced by the actual authentication/data-instance boundary. An
anonymous local deployment does not authenticate each ChatGPT conversation. A random
requestId is an idempotent intent key, not authentication: deliberately reusing it in
the same scope is indistinguishable from an exact retry. Never treat connection IDs,
self-reported metadata, timestamps or caller firstUse/confirmed flags as authorization.

presentationHint describes an optional read-only presentation opportunity in the exact
returned scope. A pending user decision or runtime request is a business fact, not a
requirement to show a Card or wait for a Card click. It must not be inferred across
unrelated Tasks. If the user already made an accurate decision in conversation, proceed
through the formal command after a fresh exact-target read. Opening details, copying
analysis instructions or showing a Card is never an approval or execution prerequisite.
An explicit request to show a Card still calls the real show tool; do not substitute a
text drawing or imagined interface. A returned show result must not recursively show.

The single ChatGPT panel is the interaction surface, never a business authority.
Navigation, progress, reports, diagnostics and history remain read-only. Its composer
collects user requirements and feedback; GPT silently prepares exact immutable intents,
and only their isolated formal confirmation children submit business confirmations.
Runtime approvals and original Codex questions have separate exact-request controls:
read current original content, require an explicit user selection and final answer
confirmation, then use canonical approval or UserDecision contracts. No default answers,
model-authored answers, auto-resends, permission expansion or historical reconfirmation.
Panel Session refresh uses dev_bridge_card_get with readMode=SESSION, never dual-mode
session_initialize. All stages stay in this panel; do not emit additional conversation
Cards. Core capabilities and domain readOnly facts remain authoritative. Widget state,
last browsed objects and old Cards never supply a default write target. WebUI/Desktop
retain their existing business controls.

Before every business command, GPT reads the exact current Session, Project and object
state as needed, uses the observed revision/Decision/action target and preserves the
original requestId, payload, receipt and reservation replay rules. Explain actual command
results; a successful write with failed readback is not permission to repeat the write.
When the user says "批准方案，暂不执行", approve the exact proposal only; do not start
Implementation, Verification or file operations. When the user says "确认实施" and the
Task, plan, channel and authorization are unambiguous, incorporate the latest confirmed
decisions into complete executable instructions, approve if still required, then start
through the channel's existing formal entry. Do not mechanically ask for a second
"开始执行". Clarify real target/scope/authorization ambiguity; never interpret "确认"
as the last browsed or most recently updated object. A historical investigation request
does not approve a newly generated proposal.

Dev Bridge is opt-in for each ChatGPT conversation. Use it only after the user
explicitly invokes Dev Bridge in that conversation, and stop using it if the user asks.

Project selection is an explicit user authorization boundary. A unique match to the
Project the user explicitly named is permitted; connector-name similarity, availability,
Console state and historical state are not Project choices.

Bridge durable facts are authoritative. GPT owns semantic intent and decides whether to
create a Feature Area, continue an explicitly identified Feature Area, or create no Feature Area. Bridge does
not infer Feature Area identity, importance, similarity, or continuity.

Engineering instructions supplied for backend operations must be complete GPT-authored
instructions suitable for direct delivery. Bridge passes those instructions verbatim;
role and profile metadata select execution configuration only. Within an authorized
Project and Repository, the trusted engineering backend owns inspection strategy and
engineering reasoning.

Executable instruction handoff applies to every proposed or successor definition.
Review summary explains findings, risks and unresolved user choices. WorkSpec.codexInstruction
is a complete standalone Implementation candidate; VerificationSpec.codexInstruction is a
separate complete Verification candidate for mode RUN. A Review may prepare both before
approval: Core still prevents unapproved dispatch. Put waiting-for-approval discussion in
summary/workflow, not in those future executable instructions. This allows a conversation
approval or direct WebUI/Desktop approval to use complete candidates without a second
Review or user confirmation. ChatGPT Card itself does not submit approval.

When the user confirms or adjusts the plan, GPT incorporates the actual latest decisions
into a self-contained instruction: state the Implementation or Verification role, concrete
goal and changes, exact authorized scope, content to preserve, technical limits, checks
and deliverables. Resolve already-decided choices and duplicate approval requests. Do not
say "see the previous plan", require access to this chat, or ask the executor to locate an
approval receipt. If the user accepted a version difference, express the agreed workspace
and preservation plan; do not retain a stop condition solely for that accepted difference.
Actual HEAD, file conflicts, access restrictions and other unresolved limits remain real;
do not invent identity facts or authorize reset, merge or deletion of others' changes.

For REVIEW_APPROVAL/APPROVE, GPT may submit executionInstructions with implementation
and, only for Verification mode RUN, verification. These are final complete instruction
texts, not summary edits. Omission preserves the proposal candidates. REVISE/REJECT and
other decisions cannot carry this handoff. Original Review evidence/hash remain unchanged;
canonical WorkSpec/VerificationSpec instruction fields in the adopted immutable revision
are the only dispatch source. Executor tool isolation is mechanical: delegated Codex does not receive Apps, plugins
or inherited MCP control tools. Do not put aiSessionId/taskId, receipt-search instructions,
or conditional Core-authorization language into engineering instructions. The executor
uses its assigned workspace and native permission requests; Core owns admission and GPT
owns workflow decisions. Existing frozen instruction text is not rewritten.
Core does not scan words, judge completeness, add summary,
rewrite instructions or synthesize an approval statement.

After conversation or WebUI/Desktop approval and an implementation request, read the exact approved
revision and current authority. If its instructions are already executable, use the normal
start without a redundant revision. If explicit user decisions require instruction-only
finalization, execution_start may carry that same executionInstructions plus a stable
requestId and the complete observed expectedTarget. This is limited to the original,
currently eligible, not-yet-dispatched CODING_AGENT candidate; it preserves the approved
source revision and creates a provenance-linked immutable successor before reservation.
Do not use it to change goal, scope, implementationMode, model or substantive tradeoffs.
Substantive changes need the user's new decision through the existing formal update path;
Core does not judge prose equivalence. Preserve the original requestId, payload and texts
on response-loss replay. Never use this path for a dispatched/terminal Execution or to
reopen a failed Run. DIRECT_FILE keeps its approved file-authorization route and never
enters execution_start merely to finalize instructions.

Documents record explicitly requested durable Project knowledge authored by GPT. Bridge
persists Document title and content verbatim. Ordinary conversation, transcripts, hidden
memory, and automatic discussion summaries are not persisted.

Bridge owns durable workflow, routing, meaningful authorization boundaries, and
deterministic product state. Deterministic Git, filesystem, process, command, test, and
static-analysis observations remain authoritative Verification evidence; backend
narrative cannot replace missing or failing deterministic evidence.

When Bridge-owned asynchronous work is running and no semantic action or user decision
is pending, yield the current interaction. Bridge owns waiting; repeated status reads are
not required.

UPPER_LLM Reviews use task_create (if needed), then task_review_start with explicit
executorKind=UPPER_LLM. Reuse the returned upperAccess.target for file_list/file_read;
only review_result_submit with a structured report can finish this Review.
workflowComplete closes reads and means AWAITING_REPORT, never success. Review
completion does not authorize implementation. Never automatically ask GPT to re-analyze
its own submitted report; explicit user-requested analysis follows the exact-ID rule above. The CREATE_AND_REVIEW composition remains CODING_AGENT-only.

Use the same actual aiSessionId from session_initialize before and after Project binding.
The next conversational turn reads session_initialize(aiSessionId) when binding needs
verification. Card does not push binding/decision context or automatically wake GPT;
its browsing never selects a FeatureArea, Task, Decision or Runtime Request for a write.
feature_area_create defaults to CREATE_ONLY:
record-only requests such as “create an area” or “save this for later” create
only the Feature Area. nextAction=CREATE_TASK is guidance, not automatic
authorization. Do not silently supply a mode, requestId, Task, Review, Repository,
or engineering instruction that the caller omitted.

When the user has already authorized development investigation and has supplied
an exact engineering goal, Repository and read-only Review scope, use
feature_area_create with explicit CREATE_AND_REVIEW, stable requestId, complete
initialTask and complete initialReview. This is one authorized composition:
create the Feature Area, its visible semantic Task and its first read-only Review,
then return after admission. Do not stop after the Feature Area merely to ask again
for a mechanical already-authorized start. Do not wait for Review completion,
approve a Review, start Implementation or Verification, or grant runtime permission.
ReviewDepth and model reasoning effort remain independent choices. The combination
tool boundary does not replace a human confirmation boundary: actual Review Approval
and Task Acceptance still require the exact user decision.

For an existing Feature Area, use task_create followed by task_review_start for a
new problem; do not create another Feature Area just to use the composition. For an
existing exact Task that needs its first Review, use task_review_start and do not
create another Task. At Review Approval or Task Acceptance, explain the exact pending
decision in conversation and obtain the user's decision if it has not already been given.
An optional read-only Card may show the same facts; no Card click is required. A historical
“start fixing it” request does not approve the actual Review proposal. If authorization, required parameters, Repository, focus, model or
current development context is missing, state the specific blocker; do not guess or
expand authority. Queries, displays, reconnects and completion notifications never
create a Feature Area or start a Review. nextAction.required is business guidance,
not a host auto-execution mechanism. These are upper-layer orchestration rules and
do not change, add to, or rewrite the Coding Agent engineering prompt.

On a user's request to analyze a completed Review or continue its workflow, use
operation_get for that exact result and workspace_get for current decisions as needed.
Explain scope, risks and recommendations from the actual report and baseline. Offer or
show a read-only Card when appropriate; request only decisions that remain unprovided.
Core persists completion and Card may refresh it, but refresh/completion never sends a
chat message, starts GPT analysis or continues development. Do not promise automatic
wake, add background notification tasks, or require a completion follow-up to proceed.
Initial APPROVE freezes the proposal's non-instruction fields and the canonical
candidate or explicitly supplied executionInstructions, including the proposal's explicit
WorkSpec implementationMode. Review executorKind and implementationMode are independent.
For CODING_AGENT, default user approval uses execution_prepare(action=APPROVE_AND_START):
the formal user confirmation freezes the plan and configuration, then the service prepares
and starts the exact approved Execution without a second startup confirmation. Use
APPROVE only when the user explicitly requests approval without startup; it enters
IMPLEMENTATION_READY. An existing historical APPROVE uses START_APPROVED to start its
exact frozen Execution, never a replacement approval. For DIRECT_FILE, the same APPROVE atomically records the Task revision's
file authorization and returns its real workflowId, scope, workspace and readiness.
On a later implementation request use the conversation's original real Session and
workspace_get for the exact Task. Read taskView.implementationMode and taskView.directFile;
only an ACTIVE grant with matching Task/revision permits the original file_list/file_read/
file_write calls. Do not ask the user for workflowId, call direct_file_enable, click a
start button, create a replacement Session/Task, borrow another Task's workflow or launch
Codex. PREPARING/UNAVAILABLE is an approved authorization with technical readiness not yet
established; report its actual status, not another approval request. GET and file calls
never create grants. Closed authorization cannot be revived by old approval replay.
Standalone direct_file_enable remains only for explicitly authorized non-Task requests.
Legacy approved Tasks with no structured mode remain UNKNOWN: do not infer Direct File
from text or default to Codex. Explicit maintenance may reconcile the exact original
approval and revision, with the user's confirmed channel when missing; it is not the
normal post-approval path. Authorization is not execution, verification or acceptance. START_VERIFICATION
also uses execution_start for the current candidate's existing verification
reservation. Verification completion is not Task Acceptance. Runtime permission
uses the separate typed runtime_request_respond, never a business approval. Read the
exact pending runtime request, explain its permissions in conversation and respond only
with the user's actual authorization. The unified panel's exact runtime permission
controls require a current original request and explicit user choice; missing buttons
never mean consent, APPROVE_FOR_SESSION or wider permissions.
CODEX_REQUEST is always an explicit current-user decision, never permission policy.
Read the exact persisted runtimeRequestId from operation_get RUNTIME_REQUESTS before
presenting its unmodified question and original options. A wakeup is only a notification,
not user consent. Do not infer from AGENTS.md, project documents, historical preferences,
or recommended/default options. Keep explanation separate. Until the user answers,
never submit a UserDecision, runtime response, replacement Run or StartTurn.
Use runtime_request_user_decision_submit to preserve the user's exact answer; raw
model-authored responses are prohibited. A draft must be shown and explicitly confirmed
before submission. Multiple requests require an unambiguous exact requestId; latest or
ordering grants no authority. STOP_AND_REVISE cancels first and needs a separate formal
successor confirmation. UNKNOWN means answer recorded but backend outcome unknown,
not unanswered: read exact recovery facts, do not ask again or blindly resend.
WAIT means no advancement write and no continuous GPT polling.

project_selection_show is only a display of an already initialized real Session.
It creates no Session or handle and cannot borrow another entry's identity. The model
already knows the Session before the user chooses in conversation; same-entry remount
reads the original presentation and retains its exact Session identity. It never restores
old binding/approval/configuration drafts or replays an old mutation intent. workspace_show requires PROJECT_BOUND
and is the user-facing workspace read plus presentation entry. details_show is an exact read-only detail view:
explicit user open defaults to EXPANDED, it exposes no workflow-mutation controls, and
it never substitutes a historical target with the latest target. New replies may have
new entries; same presentation request retries must reuse identical parameters. Host
mount and visibility are UNCONFIRMED until observed. Presentation, refresh, expand and
completion notifications never grant user approval. Follow each business, file, runtime
and presentation request's own retry contract. A successful command with
refreshRequired remains successful: retry its read, not its write.`

const (
	externalBridgeDiagnosticsDescription = "Report Dev Bridge service identity, readiness/health, protocol, application/server build version, and transport diagnostics.\nThis operation is read-only and diagnostic only."
	externalProjectListDescription       = "Read bounded candidate Project inventory within the existing access scope. Candidates are not the Session's boundProjectId; zero, one or many never authorizes automatic selection. The model first obtains a real Session with session_initialize, then asks for selection or uniquely resolves the Project the user explicitly named. Inventory read failure does not erase that Session. Read-only; no Session or Project binding is created."
	externalProjectOpenDescription       = "Bind the user's explicitly chosen Project to the EXISTING aiSessionId returned by session_initialize. Requires aiSessionId, projectId and a stable binding requestId; no-Session requests fail and never create a Session. UNBOUND binds once atomically with selection consumption and a receipt; already BOUND to the same Project returns the original binding without another Session, consumption or preparation, while another Project conflicts. Preserve the original requestId/payload for retries. After a lost response, first read session_initialize(aiSessionId); UNBOUND alone is not proof the old request was never accepted. Preparation status is separate from committed identity, and failure cannot authorize a replacement Session or repeated bootstrap. Binding selects no Area/Task/Decision/actionTarget and starts no engineering work."
	externalWorkflowDescription          = "Read the canonical current Dev Bridge product state, capabilities, pending decision, blockers, stage guidance, and recovery for one Project-bound AI interaction."
	externalTopicCreateDescription       = "Create a durable DISCOVERING FeatureArea. CREATE_ONLY records only the FeatureArea. CREATE_AND_REVIEW also creates the supplied formal Task and admits its first technically read-only Review; it may start read-only Agent work and returns without waiting for Review completion. This is a state-changing operation, not a read."
	externalDocumentCreateDescription    = "Persist a GPT-authored Project document exactly as supplied, optionally associated with an explicitly selected Feature Area in the bound Project. This records durable knowledge only and starts no backend engineering operation, Task, Review, Execution, or authorization flow."
	externalRepositorySearchDescription  = "Search tracked repository content for a fixed string within an authorized Project and Repository boundary.\nReturns bounded location facts without semantic ownership judgment or modifying files."
	externalReviewStartDescription       = "Authorize and start a technically read-only code Review for an existing semantic Task in the selected Project and FeatureArea, with its Repository, baseline, model, and reasoning effort. The operation may inspect the authorized Repository and produce Review findings; its runtime ceiling forbids repository/workspace mutation, write-permission escalation, implementation changes, git commit/merge/push, and automatic Execution. reviewInstruction is detailed engineering content delivered verbatim and does not expand this authorization. For CODING_AGENT, PRIMARY/SUPPORTING focus is relevance metadata. For UPPER_LLM, selected production roots are the read boundary; forbiddenPaths explicitly denies paths and descendants. UPPER_LLM requires no Codex model; after reads, submit review_result_submit to finish the Review. Closing file access alone does not complete it."
	externalReviewGetDescription         = ExternalReviewGetDescription
	externalTaskCreateDescription        = "Create a durable semantic Task in DISCOVERING or ACTIVE FeatureArea. Returns ACTIVE / REVIEW_REQUIRED and START_TASK_REVIEW; creates no Review, revision or Execution."
	externalScopeDescription             = "Create a pending proposal for a genuine additional Feature Area Review authorization boundary.\nIt is not required for traversal among production modules in the selected Repository. An allowed expansion never widens Task modification scope, ExecutionScope, or Implementation authorization."
	externalExecutionDescription         = "Read one Execution's detailed result, stage summaries, bounded Evidence, AgentClaim, failures, changed files, and accounting."
)

type externalTopicInput struct {
	AISessionID   string `json:"aiSessionId"`
	FeatureAreaID string `json:"featureAreaId"`
}

type externalTopicCreateInput struct {
	AISessionID      string                      `json:"aiSessionId"`
	Title            string                      `json:"title"`
	ProblemStatement string                      `json:"problemStatement"`
	Mode             string                      `json:"mode,omitempty"`
	RequestID        string                      `json:"requestId,omitempty"`
	InitialTask      *externalInitialTaskInput   `json:"initialTask,omitempty"`
	InitialReview    *externalInitialReviewInput `json:"initialReview,omitempty"`
}

type externalInitialTaskInput struct {
	Title            string   `json:"title"`
	ProblemStatement string   `json:"problemStatement"`
	Constraints      []string `json:"constraints,omitempty"`
	RepositoryID     string   `json:"repositoryId"`
}

type externalInitialReviewPlanInput struct {
	Mode            string                      `json:"mode"`
	SelectedModules []run.ReviewModuleSelection `json:"selectedModules,omitempty"`
}

type externalInitialReviewInput struct {
	ReviewDepth       string                         `json:"reviewDepth"`
	ReviewPlan        externalInitialReviewPlanInput `json:"reviewPlan"`
	ReviewInstruction string                         `json:"reviewInstruction"`
	Model             string                         `json:"model"`
	ReasoningEffort   string                         `json:"reasoningEffort,omitempty"`
}

type externalDocumentCreateInput struct {
	AISessionID   string `json:"aiSessionId"`
	FeatureAreaID string `json:"featureAreaId,omitempty"`
	Title         string `json:"title"`
	Content       string `json:"content"`
}

type externalDocumentOutput struct {
	ID            string `json:"id"`
	ProjectID     string `json:"projectId"`
	FeatureAreaID string `json:"featureAreaId,omitempty"`
	Title         string `json:"title"`
	Content       string `json:"content"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
}

type externalTopicOutput struct {
	Retirement       *featurearea.Retirement     `json:"retirement,omitempty"`
	ID               string                      `json:"id"`
	ProjectID        string                      `json:"projectId"`
	Title            string                      `json:"title"`
	ProblemStatement string                      `json:"problemStatement"`
	Status           string                      `json:"status"`
	CreatedAt        string                      `json:"createdAt"`
	UpdatedAt        string                      `json:"updatedAt"`
	Startup          *externalFeatureAreaStartup `json:"startup,omitempty"`
}

// externalFeatureAreaStartup reports durable composition facts only. It never
// predicts Review completion, approval, or external Agent acknowledgement.
type externalFeatureAreaStartup struct {
	Mode            string                `json:"mode"`
	RequestID       string                `json:"requestId,omitempty"`
	CompletedSteps  []string              `json:"completedSteps"`
	FeatureAreaID   string                `json:"featureAreaId"`
	TaskID          string                `json:"taskId,omitempty"`
	ReviewID        string                `json:"reviewId,omitempty"`
	RunID           string                `json:"runId,omitempty"`
	ReviewAdmitted  bool                  `json:"reviewAdmitted"`
	RunStatus       string                `json:"runStatus,omitempty"`
	FailedStep      string                `json:"failedStep,omitempty"`
	Error           *externalStartupError `json:"error,omitempty"`
	Retryable       bool                  `json:"retryable"`
	Retry           *externalStartupRetry `json:"retry,omitempty"`
	RefreshRequired bool                  `json:"refreshRequired"`
}

type externalStartupError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type externalStartupRetry struct {
	Action    string `json:"action"`
	RequestID string `json:"requestId"`
}

type externalTopicListOutput struct {
	FeatureAreas []externalTopicOutput `json:"featureAreas"`
}

type externalProjectOpenInput struct {
	AISessionID string `json:"aiSessionId"`
	ProjectID   string `json:"projectId"`
	RequestID   string `json:"requestId"`
}

type externalWorkflowInput struct {
	FeatureAreaID string `json:"featureAreaId,omitempty"`
	AISessionID   string `json:"aiSessionId"`
}

type externalReviewInput struct {
	ForbiddenPaths    []string                           `json:"forbiddenPaths,omitempty"`
	ExecutorKind      productreview.ExecutorKind         `json:"executorKind,omitempty"`
	ReviewDepth       string                             `json:"reviewDepth"`
	TaskID            string                             `json:"taskId"`
	AISessionID       string                             `json:"aiSessionId"`
	FeatureAreaID     string                             `json:"featureAreaId,omitempty"`
	RequestID         string                             `json:"requestId"`
	ReviewInstruction string                             `json:"reviewInstruction"`
	ReviewPlan        externalReviewPlanInput            `json:"reviewPlan"`
	RepositoryID      string                             `json:"repositoryId,omitempty"`
	Backend           *codexconfig.BackendSelectionInput `json:"backend,omitempty"`
	Model             string                             `json:"model"`
	ReasoningEffort   string                             `json:"reasoningEffort"`
}

type externalReviewGetInput struct {
	AISessionID string `json:"aiSessionId"`
	ReviewID    string `json:"reviewId"`
}

type externalReviewPlanInput struct {
	Mode            string                         `json:"mode"`
	SelectedModules []externalModuleSelectionInput `json:"selectedModules,omitempty"`
}

type externalModuleSelectionInput struct {
	Key  string `json:"key"`
	Role string `json:"role"`
}

type externalTaskCreateInput struct {
	AISessionID      string   `json:"aiSessionId"`
	FeatureAreaID    string   `json:"featureAreaId"`
	RequestID        string   `json:"requestId"`
	Title            string   `json:"title"`
	ProblemStatement string   `json:"problemStatement"`
	Constraints      []string `json:"constraints,omitempty"`
	RepositoryID     string   `json:"repositoryId"`
	SkipReview       bool     `json:"skipReview,omitempty"`
}

type externalTaskListInput struct {
	AISessionID   string `json:"aiSessionId"`
	FeatureAreaID string `json:"featureAreaId,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Offset        int    `json:"offset,omitempty"`
}

type externalTaskGetInput struct {
	AISessionID string `json:"aiSessionId"`
	TaskID      string `json:"taskId"`
}

type externalExecutionInput struct {
	AISessionID string `json:"aiSessionId"`
	ExecutionID string `json:"executionId"`
	Detail      string `json:"detail,omitempty"`
}

type externalContextInput struct {
	AISessionID        string          `json:"aiSessionId"`
	RepositoryID       string          `json:"repositoryId,omitempty"`
	CodexConfigID      string          `json:"codexConfigId,omitempty"`
	AuthorizedScope    probeScopeInput `json:"authorizedScope"`
	ContextInstruction string          `json:"contextInstruction"`
}

type externalScopeExpansionInput struct {
	AISessionID     string           `json:"aiSessionId"`
	FeatureAreaID   string           `json:"featureAreaId,omitempty"`
	ReviewID        string           `json:"reviewId"`
	CurrentScope    reviewScopeInput `json:"currentScope"`
	AdditionalScope reviewScopeInput `json:"additionalScope"`
	Reason          string           `json:"reason"`
}

const (
	externalObjectOutputSchema                = `{"type":"object"}`
	externalBridgeInfoOutputSchema            = `{"type":"object","properties":{"businessCatalogSHA256":{"type":"string","pattern":"^[a-f0-9]{64}$"},"unifiedReviewSchemaSHA256":{"type":"string","pattern":"^[a-f0-9]{64}$"},"applicationName":{"type":"string"},"applicationVersion":{"type":"string"},"ready":{"type":"boolean"},"protocol":{"type":"string","const":"MCP"},"protocolVersion":{"type":"string"},"serverName":{"type":"string"},"serverVersion":{"type":"string"},"transport":{"type":"string"}},"required":["applicationName","applicationVersion","ready","protocol","protocolVersion","serverName","serverVersion","transport","businessCatalogSHA256","unifiedReviewSchemaSHA256"],"additionalProperties":false}`
	externalStartupErrorSchema                = `{"type":"object","properties":{"code":{"type":"string","minLength":1,"maxLength":128},"message":{"type":"string","minLength":1,"maxLength":2048}},"required":["code","message"],"additionalProperties":false}`
	externalStartupRetrySchema                = `{"type":"object","properties":{"action":{"type":"string","enum":["RETRY_SAME_REQUEST","REFRESH_RESULT"]},"requestId":{"type":"string","minLength":1,"maxLength":128}},"required":["action","requestId"],"additionalProperties":false}`
	externalFeatureAreaStartupSchema          = `{"type":"object","properties":{"mode":{"type":"string","enum":["CREATE_ONLY","CREATE_AND_REVIEW"]},"requestId":{"type":"string","minLength":1,"maxLength":128},"completedSteps":{"type":"array","maxItems":4,"items":{"type":"string","enum":["FEATURE_AREA_CREATED","TASK_CREATED","REVIEW_ADMITTED","DISPATCH_OBSERVED"]}},"featureAreaId":{"type":"string","minLength":1,"maxLength":128},"taskId":{"type":"string","minLength":1,"maxLength":128},"reviewId":{"type":"string","pattern":"^review_[A-Za-z0-9_-]+$","maxLength":256},"runId":{"type":"string","minLength":1,"maxLength":128},"reviewAdmitted":{"type":"boolean"},"runStatus":{"type":"string","enum":["CREATED","PREPARING","RUNNING","WAITING_APPROVAL","COLLECTING","SUCCEEDED","FAILED","INFRASTRUCTURE_FAILED","CANCELED"]},"failedStep":{"type":"string","enum":["FEATURE_AREA_CREATE","TASK_CREATE","REVIEW_ADMISSION","DISPATCH","READBACK"]},"error":` + externalStartupErrorSchema + `,"retryable":{"type":"boolean"},"retry":` + externalStartupRetrySchema + `,"refreshRequired":{"type":"boolean"}},"required":["mode","completedSteps","featureAreaId","reviewAdmitted","retryable","refreshRequired"],"additionalProperties":false}`
	externalFeatureAreaOutputSchema           = `{"type":"object","properties":{"retirement":` + retirementOutputSchema + `,"id":{"type":"string"},"projectId":{"type":"string"},"title":{"type":"string","maxLength":256},"problemStatement":{"type":"string","maxLength":16384},"status":{"type":"string","enum":["DISCOVERING","ACTIVE","DECOMPOSED","RETIRING","RETIRED"]},"createdAt":{"type":"string"},"updatedAt":{"type":"string"},"startup":` + externalFeatureAreaStartupSchema + `},"required":["id","projectId","title","problemStatement","status","createdAt","updatedAt"],"additionalProperties":false}`
	externalDocumentOutputSchema              = `{"type":"object","properties":{"id":{"type":"string"},"projectId":{"type":"string"},"featureAreaId":{"type":"string"},"title":{"type":"string","maxLength":256},"content":{"type":"string","maxLength":262144},"createdAt":{"type":"string"},"updatedAt":{"type":"string"}},"required":["id","projectId","title","content","createdAt","updatedAt"],"additionalProperties":false}`
	externalTaskOutputSchema                  = `{"type":"object","properties":{"id":{"type":"string"},"featureAreaId":{"type":"string"},"title":{"type":"string","maxLength":256},"workKind":{"anyOf":[{"type":"null"},{"type":"string","enum":["FEATURE","BUG_FIX","REFACTOR","MAINTENANCE","TEST_CHANGE","DOCUMENTATION","OTHER"]}]},"model":{"anyOf":[{"type":"null"},` + modelIDSchema + `]},"reasoningEffort":{"anyOf":[{"type":"null"},` + reasoningEffortSchema + `]},"goal":{"type":"string","maxLength":16384},"constraints":{"type":"array","maxItems":128,"items":{"type":"string","maxLength":4096}},"executionScope":` + executionScopeOutputSchema + `,"implementationInstruction":{"type":"string","maxLength":65536},"verificationSpec":` + verificationSpecSchema + `,"status":{"type":"string","enum":["PREPARING","READY_FOR_CONFIRMATION","CONFIRMED","READY","REJECTED","CANCELED"]},"confirmationRequired":{"type":"boolean"},"execution":{"anyOf":[{"type":"null"},{"type":"object","properties":{"id":{"type":"string"},"status":{"type":"string"},"userAuthorizationRequired":{"type":"boolean"}},"required":["id","status","userAuthorizationRequired"],"additionalProperties":false}]},"createdAt":{"type":"string"},"updatedAt":{"type":"string"}},"required":["id","featureAreaId","title","model","reasoningEffort","goal","constraints","executionScope","implementationInstruction","verificationSpec","status","confirmationRequired","execution","createdAt","updatedAt"],"additionalProperties":false}`
	externalScopeOutputSchema                 = `{"type":"object","properties":{"id":{"type":"string"},"targetKind":{"type":"string","enum":["TOPIC","TASK"]},"targetId":{"type":"string"},"currentScope":` + reviewScopeSchema + `,"additionalScope":` + reviewScopeSchema + `,"reason":{"type":"string","maxLength":4096},"status":{"type":"string","enum":["PENDING_CONFIRMATION","ALLOWED","REJECTED"]},"decisionRequired":{"type":"boolean"}},"required":["id","targetKind","targetId","currentScope","additionalScope","reason","status","decisionRequired"],"additionalProperties":false}`
	externalVerificationCheckOutputSchema     = verificationCheckReportOutputSchema
	externalExecutionOutputSchema             = `{"type":"object","properties":{"executionId":{"type":"string"},"projectId":{"type":"string"},"featureAreaId":{"type":"string"},"kind":{"type":"string"},"semanticOwnerKind":{"type":"string"},"semanticOwnerId":{"type":"string"},"executionProposalId":{"type":"string"},"runId":{"type":"string"},"stage":{"type":"string"},"agentStatus":{"type":"string"},"domainStatus":{"type":"string"},"domainResult":{"type":"string"},"backendDisposition":{"type":"string"},"cancellationState":{"type":"string"},"recoveryRequired":{"type":"boolean"},"finalizationPending":{"type":"boolean"},"readOnly":{"type":"boolean"},"runIds":{"type":"array","items":{"type":"string"}},"cancellation":` + ExecutionCancellationProjectionSchema + `,"taskDefinitionRevisionId":{"type":"string"},"provenanceKind":{"type":"string","enum":["NATIVE","BACKFILLED","LEGACY_UNKNOWN"]},"id":{"type":"string"},"taskId":{"type":"string"},"status":{"type":"string","enum":["PENDING","CREATED","PREPARING","RUNNING","WAITING_APPROVAL","COLLECTING","COMPLETED","FAILED","INFRASTRUCTURE_FAILED","REJECTED","CANCELED","EXECUTING","VERIFYING","FINALIZING","UNKNOWN","UNKNOWN_ORPHANED","UNAVAILABLE"]},"userAuthorizationRequired":{"type":"boolean"},"stages":{"type":"array","maxItems":3,"items":{"type":"object","properties":{"stage":{"type":"string","enum":["SCOPE_REVIEW","IMPLEMENTATION","VERIFICATION","INSPECTION"]},"status":{"type":"string"},"startedAt":{"type":"string"},"finishedAt":{"type":"string"},"durationMillis":{"type":"integer","minimum":0}},"required":["stage","status"],"additionalProperties":false}},"changedFiles":{"type":"array","maxItems":4096,"items":{"type":"string"}},"evidence":{"type":"array","maxItems":3,"items":{"type":"object","properties":{"stage":{"type":"string"},"git":{"type":"object","properties":{"baseCommit":{"type":"string"},"headCommit":{"type":"string"},"startSnapshot":{"type":"string"},"endSnapshot":{"type":"string"},"dirty":{"type":"boolean"},"changedFiles":{"type":"array","maxItems":4096,"items":{"type":"string"}},"diffArtifactId":{"type":"string"}},"required":["baseCommit","headCommit","startSnapshot","endSnapshot","dirty","changedFiles"],"additionalProperties":false}},"required":["stage","git"],"additionalProperties":false}},"agentClaim":{"type":"object","properties":{"available":{"type":"boolean"},"message":{"type":"string","maxLength":65536}},"required":["available"],"additionalProperties":false},"goalProgress":{"type":"array","maxItems":16,"items":{"type":"object","properties":{"goalId":{"type":"string"},"position":{"type":"integer"},"title":{"type":"string","maxLength":512},"status":{"type":"string","enum":["PENDING","RUNNING","COMPLETED","FAILED","SKIPPED"]},"updatedAt":{"type":"string"}},"required":["goalId","position","title","status","updatedAt"],"additionalProperties":false}},"failures":{"type":"array","maxItems":3,"items":{"type":"object","properties":{"stage":{"type":"string"},"kind":{"type":"string"},"code":{"type":"string"},"message":{"type":"string","maxLength":2048}},"required":["stage","kind","code","message"],"additionalProperties":false}},"tokenUsage":` + tokenUsageOutputSchema + `,"verificationReport":` + verificationReportOutputSchema + `,"createdAt":{"type":"string"},"updatedAt":{"type":"string"}},"required":["executionId","kind","semanticOwnerKind","semanticOwnerId","runIds","stage","agentStatus","backendDisposition","readOnly","id","taskId","status","userAuthorizationRequired","stages","changedFiles","evidence","agentClaim","goalProgress","failures","tokenUsage","createdAt","updatedAt"],"additionalProperties":false}`
	externalTopicInputSchema                  = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","featureAreaId"],"additionalProperties":false}`
	ExternalAgentOperationRecoverInputSchema  = `{"type":"object","properties":{"expectedUpperVersion":` + UpperVersionSchema + `,"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","minLength":1,"maxLength":128},"operationKind":{"type":"string","enum":["REVIEW","EXECUTION"]},"operationId":{"type":"string","minLength":1,"maxLength":128},"runId":{"type":"string","minLength":1,"maxLength":128},"agentConversationId":{"type":"string","minLength":1,"maxLength":128},"requestId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","featureAreaId","operationKind","operationId","runId","requestId"],"if":{"properties":{"operationKind":{"const":"EXECUTION"}}},"then":{"required":["agentConversationId"]},"additionalProperties":false}`
	ExternalAgentOperationRecoverOutputSchema = `{"type":"object","properties":{"expectedUpperVersion":` + UpperVersionSchema + `,"authorizedUpperVersion":` + UpperVersionSchema + `,"grantState":{"type":"string"},"backendDisposition":{"type":"string"},"state":{"type":"string","enum":["READY","UNAVAILABLE","REAUTHORIZED"]},"featureAreaId":{"type":"string"},"operationKind":{"type":"string","enum":["REVIEW","EXECUTION"]},"operationId":{"type":"string"},"runId":{"type":"string"},"oldAgentConversationId":{"type":"string"},"newAgentConversationId":{"type":"string"},"boundary":{"type":"string","enum":["UNKNOWN","NO_ADVANCE"]},"contextAbandoned":{"type":"boolean"},"freshWorkAllowed":{"type":"boolean"}},"required":["state","featureAreaId","operationKind","operationId","runId","oldAgentConversationId","newAgentConversationId","boundary","contextAbandoned","freshWorkAllowed"],"additionalProperties":false}`
	// These typed fragments are deliberately shared by standalone Task/Review
	// tools and CREATE_AND_REVIEW. Mode-specific required fields live only in
	// their enclosing schema, so the contracts cannot silently drift.
	externalTaskIntentProperties        = `"title":{"type":"string","minLength":1,"maxLength":256},"problemStatement":{"type":"string","minLength":1,"maxLength":16384},"constraints":{"type":"array","maxItems":128,"items":{"type":"string","maxLength":4096}},"repositoryId":{"type":"string","minLength":1,"maxLength":128}`
	externalTaskIntentRequired          = `"title","problemStatement","repositoryId"`
	externalReviewModuleSelectionSchema = `{"type":"object","properties":{"key":{"type":"string","minLength":1,"maxLength":128},"role":{"type":"string","enum":["PRIMARY","SUPPORTING"]}},"required":["key","role"],"additionalProperties":false}`
	externalReviewPlanFocusedSchema     = `{"type":"object","properties":{"mode":{"type":"string","const":"FOCUSED"},"selectedModules":{"type":"array","minItems":1,"maxItems":128,"items":` + externalReviewModuleSelectionSchema + `}},"required":["mode","selectedModules"],"additionalProperties":false}`
	externalReviewPlanRepositorySchema  = `{"type":"object","properties":{"mode":{"type":"string","const":"REPOSITORY"}},"required":["mode"],"additionalProperties":false}`
	externalReviewPlanSchema            = `{"oneOf":[` + externalReviewPlanFocusedSchema + `,` + externalReviewPlanRepositorySchema + `]}`
	externalReviewBackendSchema         = `{"type":"object","properties":{"configId":{"type":"string","minLength":1,"maxLength":128},"configFingerprint":{"type":"string","pattern":"^[a-f0-9]{64}$"}},"required":["configId","configFingerprint"],"additionalProperties":false}`
	externalReviewInputProperties       = `"forbiddenPaths":{"type":"array","maxItems":128,"items":{"type":"string","minLength":1,"maxLength":1024}},"executorKind":{"type":"string","enum":["CODING_AGENT","UPPER_LLM"]},"reviewDepth":{"type":"string","enum":["FAST","DEEP"]},"reviewInstruction":{"type":"string","minLength":1,"maxLength":65536},"reviewPlan":` + externalReviewPlanSchema + `,"backend":` + externalReviewBackendSchema + `,"model":` + modelIDSchema + `,"reasoningEffort":` + reasoningEffortSchema
	externalReviewInputRequired         = `"reviewDepth","reviewPlan","reviewInstruction"`
	externalInitialTaskSchema           = `{"type":"object","properties":{` + externalTaskIntentProperties + `},"required":[` + externalTaskIntentRequired + `],"additionalProperties":false}`
	externalInitialReviewPlanSchema     = externalReviewPlanSchema
	externalReviewExecutorCondition     = `"if":{"required":["executorKind"],"properties":{"executorKind":{"const":"UPPER_LLM"}}},"then":{"not":{"anyOf":[{"required":["model"]},{"required":["reasoningEffort"]},{"required":["backend"]}]}},"else":{"required":["model","backend"],"not":{"required":["forbiddenPaths"]}}`
	externalInitialReviewSchema         = `{"type":"object","properties":{` + externalReviewInputProperties + `},"required":[` + externalReviewInputRequired + `,"model"],"allOf":[{"properties":{"executorKind":{"const":"CODING_AGENT"}},"not":{"required":["forbiddenPaths"]}}],"additionalProperties":false}`
	externalTopicCreateSchema           = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"title":{"type":"string","minLength":1,"maxLength":256},"problemStatement":{"type":"string","minLength":1,"maxLength":16384},"mode":{"type":"string","enum":["CREATE_ONLY","CREATE_AND_REVIEW"]},"requestId":{"type":"string","minLength":1,"maxLength":128},"initialTask":` + externalInitialTaskSchema + `,"initialReview":` + externalInitialReviewSchema + `},"required":["aiSessionId","title","problemStatement"],"allOf":[{"oneOf":[{"allOf":[{"not":{"required":["mode"]}},{"not":{"anyOf":[{"required":["initialTask"]},{"required":["initialReview"]}]}}]},{"properties":{"mode":{"const":"CREATE_ONLY"}},"required":["mode"],"not":{"anyOf":[{"required":["initialTask"]},{"required":["initialReview"]}]}},{"properties":{"mode":{"const":"CREATE_AND_REVIEW"}},"required":["mode","requestId","initialTask","initialReview"]}]}],"additionalProperties":false}`
	externalDocumentCreateSchema        = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","minLength":1,"maxLength":128},"title":{"type":"string","minLength":1,"maxLength":256},"content":{"type":"string","minLength":1,"maxLength":262144}},"required":["aiSessionId","title","content"],"additionalProperties":false}`
	externalProjectOpenSchema           = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128,"pattern":"^\\S+$"},"projectId":{"type":"string","minLength":1,"maxLength":128,"pattern":"^\\S+$"},"requestId":{"type":"string","minLength":1,"maxLength":128,"pattern":"^\\S+$"}},"required":["aiSessionId","projectId","requestId"],"additionalProperties":false}`
	externalWorkflowInputSchema         = `{"type":"object","properties":{"featureAreaId":{"type":"string","minLength":1,"maxLength":128},"aiSessionId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId"],"additionalProperties":false}`
	externalExecutionSchema             = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"executionId":{"type":"string","minLength":1,"maxLength":128},"detail":{"type":"string","enum":["SUMMARY","EVIDENCE"]}},"required":["aiSessionId","executionId"],"additionalProperties":false}`
	externalTaskGetSchema               = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"taskId":{"type":"string","minLength":1,"maxLength":128}},"required":["aiSessionId","taskId"],"additionalProperties":false}`
	externalTaskListSchema              = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","maxLength":128},"limit":{"type":"integer","minimum":1,"maximum":100},"offset":{"type":"integer","minimum":0,"maximum":1000000}},"required":["aiSessionId"],"additionalProperties":false}`
	externalTaskCreateSchema            = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","minLength":1,"maxLength":128},"requestId":{"type":"string","minLength":1,"maxLength":128},"skipReview":{"type":"boolean"},` + externalTaskIntentProperties + `},"required":["aiSessionId","featureAreaId","requestId",` + externalTaskIntentRequired + `],"additionalProperties":false}`
	externalReviewStartSchema           = `{"type":"object","properties":{"taskId":{"type":"string","minLength":1,"maxLength":128},"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"featureAreaId":{"type":"string","minLength":1,"maxLength":128},"requestId":{"type":"string","minLength":1,"maxLength":128},"repositoryId":{"type":"string","maxLength":128},` + externalReviewInputProperties + `},"required":["aiSessionId","taskId","requestId",` + externalReviewInputRequired + `],` + externalReviewExecutorCondition + `,"additionalProperties":false}`
	externalReviewGetSchema             = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"reviewId":{"type":"string","pattern":"^review_[A-Za-z0-9_-]+$","maxLength":256}},"required":["aiSessionId","reviewId"],"additionalProperties":false}`
	externalContextInputSchema          = `{"type":"object","properties":{"aiSessionId":{"type":"string","minLength":1,"maxLength":128},"repositoryId":{"type":"string"},"codexConfigId":{"type":"string"},"authorizedScope":{"type":"object","properties":{"kind":{"type":"string","enum":["PATHS","REPOSITORY"]},"paths":{"type":"array","maxItems":128,"items":{"type":"string"}}},"required":["kind"],"additionalProperties":false},"contextInstruction":{"type":"string","minLength":1,"maxLength":65536}},"required":["aiSessionId","authorizedScope","contextInstruction"],"additionalProperties":false}`
	externalScopeExpansionSchema        = `{"type":"object","properties":{"aiSessionId":{"type":"string"},"featureAreaId":{"type":"string"},"reviewId":{"type":"string"},"currentScope":` + reviewScopeSchema + `,"additionalScope":` + reviewScopeSchema + `,"reason":{"type":"string","minLength":1,"maxLength":4096}},"required":["aiSessionId","currentScope","additionalScope","reason"],"additionalProperties":false}`
)

func addExternalTool[In any, Out any](server *sdkmcp.Server, name, title, description, in, out string, annotations *sdkmcp.ToolAnnotations, handler sdkmcp.ToolHandlerFor[In, Out]) {
	tool := &sdkmcp.Tool{Name: name, Title: title, Description: description, InputSchema: json.RawMessage(in), OutputSchema: json.RawMessage(out), Annotations: annotations}
	sdkmcp.AddTool(server, tool, handler)
}

func registerExternalWorkflowTools(server *sdkmcp.Server, service BridgeService, reviewStartSchema string) {
	addExternalTool(server, "bridge_diagnostics", "Bridge diagnostics", externalBridgeDiagnosticsDescription, emptyInputSchema, externalBridgeInfoOutputSchema, toolAnnotations("Bridge diagnostics", true, true), func(ctx context.Context, request *sdkmcp.CallToolRequest, _ bridgeInfoInput) (*sdkmcp.CallToolResult, BridgeInfoOutput, error) {
		out, err := BridgeDiagnosticsCore(ctx, service, request.ProtocolVersion())
		return nil, out, err
	})

	addExternalTool(server, "project_list", "List Projects", externalProjectListDescription, projectListInputSchema, projectListOutputSchema, toolAnnotations("List Projects", true, true), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in projectListInput) (*sdkmcp.CallToolResult, ProjectListOutput, error) {
		out, err := ProjectListCore(ctx, service, in)
		return nil, out, err
	})

	addExternalTool(server, "project_open", "Open Project", externalProjectOpenDescription, externalProjectOpenSchema, externalProjectOpenOutputSchema, stateChangingAnnotations("Open Project", false), func(ctx context.Context, request *sdkmcp.CallToolRequest, in externalProjectOpenInput) (*sdkmcp.CallToolResult, externalProjectOpenOutput, error) {
		ctx, err := withSessionRequest(ctx, request)
		if err != nil {
			return nil, externalProjectOpenOutput{}, publicToolError(err)
		}
		out, err := ProjectOpenCore(ctx, service, in)
		return nil, out, err
	})

	addExternalTool(server, "feature_area_create", "Create Feature Area", externalTopicCreateDescription, externalTopicCreateSchema, externalFeatureAreaOutputSchema, stateChangingAnnotations("Create Feature Area", false), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in externalTopicCreateInput) (*sdkmcp.CallToolResult, externalTopicOutput, error) {
		out, err := TopicCreateCore(ctx, service, in)
		return nil, out, err
	})
	addExternalTool(server, "agent_operation_recover", "Recover Agent Operation", "Recover the exact durable operation. Coding recovery abandons its unsafe context. Upper recovery requires the observed expectedUpperVersion and reauthorizes the same Review/Run to PREPARING; this does not mean file access is ACTIVE. Explicit review_access_prepare can activate that recorded authorized version using the exact Upper target. Replay the original recovery requestId and payload without replacing its version; receipt replay does not prepare access.", ExternalAgentOperationRecoverInputSchema, ExternalAgentOperationRecoverOutputSchema, stateChangingAnnotations("Recover agent operation", false), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in AgentOperationRecoverInput) (*sdkmcp.CallToolResult, AgentOperationRecoveryOutput, error) {
		out, err := AgentOperationRecoverCore(ctx, service, in)
		return nil, out, err
	})

	addExternalTool(server, "document_create", "Create Document", externalDocumentCreateDescription, externalDocumentCreateSchema, externalDocumentOutputSchema, stateChangingAnnotations("Create Document", false), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in externalDocumentCreateInput) (*sdkmcp.CallToolResult, externalDocumentOutput, error) {
		out, err := DocumentCreateCore(ctx, service, in)
		return nil, out, err
	})

	addExternalTool(server, "repository_search", "Search Repository", externalRepositorySearchDescription, repositorySearchInputSchema, repositorySearchOutputSchema, toolAnnotations("Search Repository", true, true), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in repositorySearchInput) (*sdkmcp.CallToolResult, RepositorySearchOutput, error) {
		out, err := RepositorySearchCore(ctx, service, in)
		return nil, out, err
	})

	addExternalTool(server, "task_review_start", "Authorize read-only Feature Area Review", externalReviewStartDescription, reviewStartSchema, externalReviewOutputSchema, stateChangingAnnotations("Authorize read-only Feature Area Review", false), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in externalReviewInput) (*sdkmcp.CallToolResult, externalReviewOutput, error) {
		out, err := StartTopicReviewCore(ctx, service, in)
		if err == nil && out.Review.ID != "" {
			AttachReviewDrift(ctx, service, in.AISessionID, out.Review.ID, &out)
		}
		return nil, out, err
	})

	addExternalTool(server, "task_create", "Create Task", externalTaskCreateDescription, externalTaskCreateSchema, SemanticTaskOutputSchema, stateChangingAnnotations("Create Task", false), func(ctx context.Context, _ *sdkmcp.CallToolRequest, in externalTaskCreateInput) (*sdkmcp.CallToolResult, SemanticTaskOutput, error) {
		out, err := TaskCreateCore(ctx, service, in)
		return nil, out, err
	})

}

func isControlNotFound(err error) bool {
	var apiErr *controlapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == controlapi.CodeNotFound
}

func currentExecutionRuns(ctx context.Context, service BridgeService, aiSessionID, taskID, executionID string, detailed bool) ([]run.Run, error) {
	values := make([]run.Run, 0, 2)
	for _, purpose := range []run.Purpose{run.PurposeImplementation, run.PurposeVerification} {
		value, err := service.LatestRunForTask(ctx, aiSessionID, taskID, purpose, executionID)
		if isControlNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if detailed {
			value, err = service.GetRunForSession(ctx, aiSessionID, value.ID)
			if err != nil {
				return nil, err
			}
		}
		values = append(values, value)
	}
	return values, nil
}

func externalTopicOutputFor(value featurearea.FeatureArea) externalTopicOutput {
	return externalTopicOutput{Retirement: value.Retirement, ID: value.ID, ProjectID: value.ProjectID, Title: boundedUTF8(value.Title, 256), ProblemStatement: boundedUTF8(value.ProblemStatement, 16<<10), Status: string(value.Status), CreatedAt: value.CreatedAt.UTC().Format(timeFormat), UpdatedAt: value.UpdatedAt.UTC().Format(timeFormat)}
}

const retirementOutputSchema = `{"type":"object","properties":{"requestId":{"type":"string"},"requestedAt":{"type":"string"},"completedAt":{"type":"string"},"summary":{"type":"string"},"remainingTasks":{"type":"integer"},"engineeringAvailable":{"type":"boolean"},"lastError":{"type":"string"}},"required":["requestId","requestedAt","summary","remainingTasks","engineeringAvailable"],"additionalProperties":false}`

// ExternalReviewGetDescription is shared by GPT and Local model-facing catalogs.
const ExternalReviewGetDescription = "Read the exact Feature Area Review findings, evidence, failures and recovery without writing workflow state. On an explicit analysis request, read the user's exact Review and verify Session/Project ownership; use workspace_get when current Decision or Task facts are needed. Explain actual findings, scope, changes, risks, baseline and evidence limits, distinguishing Agent claims from deterministic verification. A historical result stays historical; never substitute the newest Review. A read-only workspace_show is optional and is not approval. If the user has already made an accurate decision in conversation, the model may use the original formal command with fresh exact target and existing receipt rules; otherwise ask for the outstanding decision. Do not expect a Card refresh or completion notification to wake GPT, automatically analyze the report, grant approval or start development."

// ChatGPT uses purpose-specific confirmation; Local/Desktop retain the canonical
// command instructions above. Component visibility is not caller authentication.
const ChatGPTConfirmationInstructions = `Dev Bridge separates engineering authorship, user confirmation, and Core execution admission.
Use the conversation's existing real aiSessionId. If none exists, session_initialize with an explicit requestId returns it; never manufacture or replace a Session to bypass scope or a failure. After obtaining that existing Session, call panel_show ONCE to open the single persistent right-side panel. Exception: when the user explicitly asks to reopen a hidden or closed workbench, call panel_show again with the SAME existing aiSessionId and a fresh presentationRequestId; preserve its original Task/history and never initialize another Session, prepare/consume a confirmation or start a Run merely to restore visibility. Do not reopen automatically after ordinary replies. When the user asks to view or continue an existing Task, resolve its exact identity using work_list/workspace_get, then call panel_focus with this Session and bound Project to switch the EXISTING panel silently. A supplied FeatureArea is an assertion, not a guess; ambiguous names require user selection. Manual panel navigation uses the same focus. Focus only changes display, never authorizes or starts work. For an unbound Session, the user clicks 选择项目 in that panel to open a formal SELECT_PROJECT form, chooses a Project, and confirms there. The panel itself calls project_setup_prepare only for that explicit click; it does not require a GPT message, another Card, or an automatic single-candidate choice. First binding selects only the logical Project; repository development-branch configuration is a later separate formal operation. If explicitly preparing by conversation, use the same original Session and SELECT_PROJECT without bypassing user confirmation. Do not call workspace_show, details_show or confirmation_show for subsequent phases; those separate presentation tools are legacy app-only endpoints. Keep ordinary conversations supported through the same panel. Later reads, preparation and analysis remain SILENT and the panel polls the Session operation feed, including proposals before a Task exists. Do not post additional inline Cards or remount the panel merely because a phase or selected Task changed. Options reads do not bind or create branches. No automatic single-candidate choice. After the user confirms, confirmation_get of that exact intent returns binding and branch results separately.

Route requirements FeatureArea-first: bounded work_list FEATURE_AREA, then TASK/OPERATION in the exact area; inspect current immutable definition and exact source Review/Execution with workspace_get and operation_get SOURCE/RESULT. A pending CREATE_TASK/CREATE_AREA proposal elsewhere in the Session is not a prerequisite for the selected Task's Review or execution. Continue the exact selected Task using its authoritative required action; never ask the user to confirm an unrelated old creation proposal merely because it appears in the Session feed. The panel keeps other proposals in history while a Task is selected, without consuming or canceling them. Reuse historical knowledge without borrowing historical authorization. Necessary code reads use repository_search and repository_file_read; they never require creating a placeholder Review. Actual workspace HEAD/content hashes remain distinct from registered development baselines. Do not reset, merge, create Sessions, or mutate objects to make evidence fit.

Prepare complete, directly executable engineering content before showing a confirmation:
- feature_area_prepare: create an area only, or create its explicit initial Task and start the one prepared Review.
- task_prepare: create a Task only, or create it and start its prepared first Review.
- review_prepare: first/current-revised Review or same-Task supplemental Review. Read the exact existing Task requiredReviewDepth and allowedReviewDepths; for supplemental preparation use these fields on the REQUEST_TASK_REVIEW capability, which accounts for the previous Review when approval cleared the Task-level floor. Supplemental scope never lowers this minimum. Supplemental confirmation internally runs the canonical request and start steps with the exact five-field observed target; one user choice covers both steps. For an already pending Review approval, REVISE or REJECT is also prepared here as a dedicated Card with the exact Task/FeatureArea/Review/Decision identity and a neutral decision variant only—no replacement review content, instruction or runtime configuration. Do not invoke removed direct-start handlers or decision_respond.
- execution_prepare: for an explicitly Review-skipped Task use CONFIRM_AND_START_IMPLEMENTATION with the exact Task revision and full single-repository Coding workSpec/verificationSpec; the user selects backend/runtime and confirms once to freeze and start. Do not add a later START_APPROVED confirmation. Legacy CONFIRM_IMPLEMENTATION remains definition-only. Otherwise approve and start the prepared implementation, start an already-approved candidate, or create/start an authorized technical successor. RETRY_IMPLEMENTATION is independent of supplemental Review when existing engineering evidence remains sufficient. At a verification checkpoint, the primary choices are CONTINUE_TASK, COMPLETE_TASK and DEFER_TASK, prepared with exact Task/FeatureArea/Execution/revision/Decision identity and one decision-only variant. COMPLETE_TASK freezes acceptance, commit and per-repository integration targets; the Task completes only after every merge succeeds. DEFER_TASK requires execution.note with the checkpoint and remaining work. CONTINUE_TASK preserves the same Task and starts no Run. ACCEPT is legacy acceptance-only compatibility and does not establish integrated completion; use it only for an explicit compatibility request. REJECT and REQUEST_REVERIFICATION retain their exact decision-only contracts. These choices carry no replacement WorkSpec, VerificationSpec, instructions or configuration. REQUEST_REVERIFICATION inherits its source Execution snapshot in Core. After a completion merge plan is explicitly released, prepare a fresh MERGE_FEATURE_AREA confirmation with featureArea.completionTaskId naming the original Task for its accepted commits and current development targets; never repeat acceptance or mutate the old plan.
If an older supplemental confirmation is PARTIAL and confirmation_get returns reviewStartPreparation, keep its consumed receipt unchanged. Re-read the same Task, then prepare a NEW review_prepare(action=START_REVIEW) using that exact current expectedTarget and an allowed reviewDepth. Do not repeat the recorded supplemental request, replay or replace the consumed intent, or treat the old step as success. The corrected depth is shown for a new user confirmation; reads/preparation do not start work.
No formal Task/Review/Execution exists merely because a creation intent was prepared. Prepare stores an immutable pending intent only. confirmation_show renders an existing exact intent; show/get/refresh never confirm or dispatch. Read intentId/version/purpose from structuredContent.intent and keep that identity for subsequent conversation reads. Do not infer the latest object or a Card-browsed Task as the write target. A sourceIntentId replacement yields a separate pending intent with its own identity and never revives or rebinds the superseded confirmation. Before confirmation_commit succeeds, report only this intent's identity, frozen state and counts; never state a recreated Task or Review, an existing successor Review, a started re-review, or that a Card was shown. Each intent is its own Card instance, addressed by the devbridge/presentationInstanceId derived from aiSessionId, intentId, version and contentHash, so a replacement is presented as its own isolated child inside the SAME persistent panel and never as the old Card becoming current again; an old Card stays historical and uncommittable.

For each new variant, optionally provide displaySummary.summary: one or two sentences about the current engineering change, at most 160 Unicode characters; and displaySummary.keyConstraints: at most three explicit decision-relevant boundaries, each at most 60 Unicode characters. Keep identifiers, historical SHAs, prior repair ledgers, routing rationale and full file/check lists in the full proposal or conversation explanation. Use label for a neutral proposal difference, not repeated Codex/file mode prefixes. Do not invent no-test/no-commit constraints; include only explicitly prepared boundaries. The short summary is optional presentation content, not an approval prerequisite, and must never replace complete instructions. Old intents without it remain readable and confirmable under their original rules; do not reprepare them for appearance.
The result envelope may additionally include display: exact, caller-authorized live object/environment names outside intent.content. Names may be renamed or unavailable; this metadata is not a frozen baseline, content revision or new operation target. Never use it to rewrite hashes or replace the exact intent. Confirmation consequences are determined by action/mode, not by the short summary.

Provide complete canonical reviewInstruction or Implementation/Verification instructions and applicable typed content variants. Summaries are labelled summaries; the Card can expose the exact full content. Do not embed a competing model/configuration authority in Markdown. Recommendations belong in recommendedRuntimeConfig only. If the user changes engineering scope/content, prepare a new immutable version or replacement intent; never patch a confirmed version or hide a prompt override. Core checks deterministic protocol/identity/authority, not semantic coverage or keyword approval phrases.

An already open Task panel keeps progress fixed and lists same-Session, exact-Task prepared confirmations beneath it. Each list item is its own immutable formal Card instance with independent input, selections, requestId and receipt readback. When the user is using this panel, prepare the next intent and let its operation list refresh; do not emit an additional standalone confirmation_show widget. Use confirmation_show for clients without the unified panel or an explicitly requested standalone Card. Progress reads never commit, and model orchestration must never call confirmation_commit for the user.
Every independent new Codex Review or Execution in this ChatGPT flow requires its dedicated Card confirmation, even if the user stated a model or said 'start' in conversation. Prefill only a recommendation; never use a default or a retired direct-start tool to bypass the Card. No suitable UI host means pending confirmation with an accurate reason, not automatic conversation fallback. Local/Desktop retain their own explicit authorized controls.
Review executor is independent: CODING_AGENT = Codex审查; UPPER_LLM = GPT文件审查. Execution implementation mode is CODING_AGENT = Codex实施; DIRECT_FILE = GPT文件实施. Only prepared applicable variants are selectable. GPT file Review carries no Codex configuration. DIRECT_FILE implementation with RUN verification requires an explicit Codex verification backend and runtime configuration, frozen by the user before file work. The Core starts that authorized independent read-only verification after file_implementation_result_submit; do not prepare or start a duplicate Run. NONE verification requires its original explicit user waiver and carries no Codex verification configuration. FAST/DEEP Review depth is separate from Codex Fast Mode.
Review freezes its own selected runtime configuration. Execution freezes one configuration for both Implementation and Verification. verification_continue accepts only an exact existing VERIFICATION_READY Execution and no configuration/instructions. REQUEST_REVERIFICATION inherits the exact source Execution snapshot. Model/capability changes block rather than silently selecting replacements. Existing operations and same-turn/reconnection recovery do not require duplicate configuration confirmation.

confirmation_commit is the single public submission entry shared by the Card and by explicit user instruction: one stored intent/version/contentHash, the exact action, a prepared variant and structured selections. It accepts no outcome, consumed flag, Decision status or version, successor identity, retryDisposition, reprepareAllowed, force, assumeNotConsumed or retryAnyway. Call it only when the user confirmed this exact operation - by one Card submission or by unambiguous text naming this intent - and never on your own initiative, never to bypass a Card and never to continue work the user did not confirm. ui.visibility controls exposure, not cryptographic proof of a human click; the server still checks the original caller/Session, target, content and receipts. Never invent verified-host evidence. The model may legitimately submit results and perform explicitly authorized cancellation/recovery through the original narrowly scoped tools.
Confirmation tools return a strict structuredContent envelope with optional intent, options and failure. isError retains the actual code/source/stage and does not erase a proven submission or completed business steps. submission.outcome is separate from intent.state and step results: CONSUMED is unique selection consumption, not business success; REJECTED_NOT_CONSUMED is a durable terminal result for that exact request; UNKNOWN or a missing record is never permission to retry. If a requestId is known, pass that original requestId to confirmation_get; do not invent one. A recorded rejection only permits editing when the current intent remains PENDING, unconsumed and its nextAction allows it; fresh content needs a new prepared version. Exact prepare replay uses the original requestId and identical raw input without re-deriving current content. Historical inputs whose original hash cannot be proven return an explicit readback restriction; read the known exact intent instead.
After a Card action, read confirmation_get for that exact initial intent to obtain actual Review/Execution/workflow IDs, actual configuration and each original command's result. Do not ask the user to copy internal IDs or describe the Task again. Partial success is not rollback: a created Task may have no started Review, an approved plan may have a failed reservation/dispatch, and a file authorization may still be preparing. Unknown results require exact receipt readback; never generate new request IDs or blindly repeat a start. Historical receipts cannot restore revoked grants or current revision pointers.

When an Implementation fails, inspect that exact Execution and Implementation Run result plus actual successor eligibility. Do not ask for a Verification Report unless this Execution has a real Verification Run/result; distinguish missing or failed result reads from a completed report. Analysis never creates a successor or authorizes a business command.

Implementation SCOPE_CONFLICT is a known stop, not an automatic re-review request. Preserve all changed files and read the exact current Task, Execution, failed Implementation Run, frozen definition, Git evidence and Core actions. Never start Verification, accept/merge, blindly retry, expand scope in place, reset/clean files or create a replacement Task. A missing native result remains UNKNOWN independently. When Core offers engineering recovery, use recovery_status_prepare/start/read/resolve with the exact source; absence of that capability is a blocker, not permission to manufacture a Review or continuation. After a resolved RECOVERED_PARTIAL assessment, an eligible RETRY_IMPLEMENTATION may continue the same approved revision with an exact continuation instruction and recoveryStatusId; this does not require a new Review. A scope-changing REVISION_REQUIRED assessment requires the exact RECOVERY_PURPOSE confirmation and its frozen successor proposal, never a plain retry or an ordinary REQUEST_CHANGES bypass. This formal recovery revision can create a new definition/Execution without an automatic Review. If further engineering review is needed, use REQUEST_TASK_REVIEW only when Core offers that exact same-Task eligibility; review_prepare then prepares REQUEST_SUPPLEMENTAL_REVIEW. CONFLICT/INSUFFICIENT facts cannot authorize continuation. Report the concrete blocker and the offered stop/revision/cancellation choices. Review-result SCOPE_CONFLICT uses its separate invalid-review-result resolution contract.

For Codex Runs with resultProtocol=NATURAL_V1, native SUCCEEDED means execution completion only. If analysisState=REQUIRED or nextAction=ANALYZE_RUN_RESULT, call run_analysis_get, analyze its immutable sources, and submit run_analysis_submit using sourceHash and reportSchema. Do not ask Codex for JSON, reexecute a successful Run to fix report formatting, or treat execution success as user acceptance. Implementation analysis is optional; the required Verification analysis precedes acceptance. For GPT file Review, confirmation readback must show the actual Upper Review and read grant readiness. Then read only the granted files and submit via review_result_submit. Grant closure is not Review completion. For GPT file implementation, use the approved revision's ACTIVE Direct File workflow, never direct_file_enable or a fake Codex context. Each file operation still goes through its original authority. Submit its formal implementation/verification result; unsupported command execution is NOT_RUN, never an unconfirmed Codex verification. Closing a file workflow is not engineering success or user acceptance.
Use file_implementation_result_submit for the exact file Task/revision/workflow's formal result. After response loss or for history, file_implementation_result_get reads that exact result and separate acceptance state without replaying submission. file_implementation_accept requires a separate explicit user acceptance, unchanged recorded snapshot and the original approved NONE verification contract; NOT_RUN cannot satisfy required RUN verification. Acceptance and the original Task Git commit status are distinct. No implicit waiver or unconfirmed Codex follow-up is allowed. For an already confirmed DIRECT_FILE + RUN plan, the Core-owned verification follow-up is authorized and its exact Run/result must be analyzed before COMPLETE_TASK.
File-mode Card confirmation records authorization intent; work/permissions are ready only when current Core readback explicitly reports readiness. It never means GPT has already acted. The unified panel may use ui/message or sendFollowUpMessage for explicit preparation requests and exact authorized GPT handoffs after a consumed confirmation or reauthorization receipt. These messages do not grant new business authority. A message acknowledgement means sent, not processed or completed. Preserve queued handoffs across read failures, revalidate restored receipts, and never automatically resend a message with an unknown delivery outcome. If the host cannot deliver, expose an exact manual continuation reference in the same panel.

The outer single panel keeps navigation, status, results, details and history read-only. Its request composer collects requirements and revision feedback for GPT to prepare silently. Runtime approval controls re-read the exact original Approval before deciding once; original Codex questions require unselected original options, final user confirmation and canonical UserDecision checksum/submission, never GPT-authored answers or automatic resend. A terminal Task does not prevent explicit creation of separate new work. Exact new creation proposals are correlated by their prepare requestId; older proposals stay read-only history. Project binding, FeatureArea/Task creation and all later approvals are isolated formal confirmation children inside it. Each child always remains attached to its own immutable intent and purpose; a later user decision adds an operation to the same panel, never a new conversation Card. Only the exact child user click submits its own confirmation. Host PiP support and its placement remain negotiated; never claim that requesting the panel proves it opened.
Explicit user text 'Continue confirmation <intentId> version <version> contentHash <contentHash>' names one exact intent and is routed through the same confirmation_get and confirmation_commit services the Card uses. First read confirmation_get with that exact five-field identity (aiSessionId, intentId, version, purpose, contentHash); intentId and version alone cannot address the read. Then act on the durable submission the read returns: CONSUMED with completed steps means return the actual business objects already created, not a resubmission. A recorded REJECTED_NOT_CONSUMED whose retryDisposition is REPREPARE_REQUIRED means report the typed rejection verbatim and prepare the replacement with confirmation_reprepare; never call commit on it. UNKNOWN, PARTIAL or an unrecorded receipt means read the original requestId receipt only - never resend, never declare the write absent, and never call it a failure. SUPERSEDED, ABANDONED, EXPIRED or a committed intent means read and describe current state without any command. Call confirmation_commit only when that exact read shows PENDING, no recorded submission, options available and the identity unchanged. After a Host transport error, perform that exact read-back before concluding anything, and never downgrade a typed rejection to recorded=false, OUTCOME_UNKNOWN or READ_ORIGINAL_RECEIPT: a recorded rejection is a definite result. If no confirmation_commit tool is discoverable on this surface, return the actual CONFIRMATION_COMMIT_TOOL_UNAVAILABLE condition and state no outcome at all - do not invent one and do not relabel the missing tool as UNKNOWN. Outer wording must stay literal: for the rejection report 本次重新审查确认未被消费。原因：冻结的审查处置状态已发生变化。需要重新准备新的确认。 and for the prepared replacement 新的重新审查确认已准备; only a committed replacement with durable successor identity may be described as successor Review 已创建并开始准备. Never describe either as 当前结果未知 or 无法确认是否已提交.
Explicit user text 'Analysis review result <reviewId>' requests read-only analysis of that exact Review via operation_get target.kind=REVIEW, target.operationId=<complete ID>, include=[RESULT]; EVIDENCE only when needed. Explain actual scope, changes, risk, validation gaps and baseline. Not found/cross-Project/unreadable stays an accurate error, never a latest-Review substitution. Analysis is not approval/start. Do not demand analysis or copying as an extra approval prerequisite.
Retain original exact cancellation, recovery, runtime permission, UNKNOWN/quiescence, receipt/reservation and immutable revision contracts. Never claim a format check or static control-flow review is a real host, dispatch, race, replay or business verification.` + "\n\n" + RuntimeUserDecisionInstructions
