package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/lazyun/devbridge/internal/agentruntime"
	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/aiprojectselection"
	"github.com/lazyun/devbridge/internal/aisession"
	"github.com/lazyun/devbridge/internal/executionproposal"
	"github.com/lazyun/devbridge/internal/mcpserver"
	"github.com/lazyun/devbridge/internal/project"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const frozenExternalInstructions = `Session-first initialization is evaluated before every first-use display rule.
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

func TestExternalWorkflowDiscoveryContract(t *testing.T) {
	if mcpserver.ExternalServerInstructions != frozenExternalInstructions {
		t.Fatal("Server instructions changed without contract update")
	}
	for _, semantic := range []string{"Session-first initialization", "session_initialize({requestId})", "a single candidate is not authorization", "same binding returns its original facts", "Analysis reads and mutations are SILENT", "Refresh the existing entry in place", "opt-in for each ChatGPT conversation", "explicit user authorization boundary", "passes those instructions verbatim", "trusted engineering backend", "authoritative Verification evidence", "project_open", "feature_area_create defaults to CREATE_ONLY", "CREATE_AND_REVIEW", "task_create", "task_review_start", "IMPLEMENTATION_READY", "START_VERIFICATION", "Verification completion is not Task Acceptance", "COLLAPSED", "EXPANDED", "never grant user approval", "CODEX_REQUEST is always an explicit current-user decision", "not user consent", "Do not infer from AGENTS.md", "runtime_request_user_decision_submit", "UNKNOWN means answer recorded", "latest or\nordering grants no authority"} {
		if !strings.Contains(mcpserver.ExternalServerInstructions, semantic) {
			t.Fatalf("instructions omit %q", semantic)
		}
	}
	_, _, baseURL := startTestServer(t, readyService(t))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "external-contract", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if session.InitializeResult().Instructions != mcpserver.ChatGPTConfirmationInstructions {
		t.Fatal("live instructions diverge")
	}
	first, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if !bytes.Equal(a, b) {
		t.Fatal("tools/list is not deterministic")
	}
	retired := map[string]bool{"project_open": true, "development_branch_select": true, "feature_area_create": true, "task_create": true, "task_review_request": true, "task_review_start": true, "execution_start": true, "direct_file_enable": true, "project_selection_show": true, "decision_respond": true}
	want := []string{"agent_context_reset", "agent_operation_recover", "bridge_diagnostics", "confirmation_commit", "confirmation_get", "confirmation_options_get", "confirmation_reprepare", "development_branch_create", "direct_file_disable", "direct_file_reconcile_task_authorization", "document_create", "execution_prepare", "feature_area_lifecycle", "feature_area_merge_reprepare", "feature_area_merge_retry_candidates", "feature_area_prepare", "feature_area_submit", "file_implementation_accept", "file_implementation_result_get", "file_implementation_result_submit", "file_list", "file_read", "file_write", "operation_cancel", "operation_get", "project_inspection_recovery_continue", "project_list", "project_setup_prepare", "recovery_purpose_repair", "recovery_status_prepare", "recovery_status_read", "recovery_status_resolve", "recovery_status_start", "repository_file_read", "repository_search", "repository_unlink_commit", "repository_unlink_prepare", "repository_unlink_read", "review_access_get", "review_access_prepare", "review_evidence_get", "review_prepare", "review_result_get", "review_result_submit", "review_worker_claim", "review_worker_get", "run_analysis_get", "run_analysis_submit", "runtime_request_recovery", "runtime_request_respond", "runtime_request_user_decision_prepare_hash", "runtime_request_user_decision_submit", "session_binding_get", "session_initialize", "successor_execution_prepare", "task_commit_reprepare", "task_commit_retry", "task_commit_retry_candidates", "task_prepare", "task_stop_and_revise", "verification_continue", "work_list", "workspace_get", "panel_focus", "panel_show"}
	sort.Strings(want)
	got := []string{}
	byName := map[string]*sdkmcp.Tool{}
	for _, tool := range first.Tools {
		byName[tool.Name] = tool
		ui, _ := tool.Meta["ui"].(map[string]any)
		visibility := testToolVisibility(ui)
		if strings.Join(visibility, ",") != "app" {
			got = append(got, tool.Name)
		} else if ui["resourceUri"] != nil || tool.Meta["openai/outputTemplate"] != nil {
			t.Fatalf("app-only read/action renders %s", tool.Name)
		}
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) || len(got) != len(want) {
		t.Fatalf("actual model catalog %v", got)
	}
	for _, name := range want {
		tool := byName[name]
		if tool.Description == "" {
			t.Fatalf("missing description %s", name)
		}
		ui, _ := tool.Meta["ui"].(map[string]any)
		show := name == "panel_show"
		if (ui["resourceUri"] != nil) != show {
			t.Fatalf("only show tools render: %s %#v", name, tool.Meta)
		}
		for label, raw := range map[string]any{"input": tool.InputSchema, "output": tool.OutputSchema} {
			encoded, _ := json.Marshal(raw)
			var schema map[string]any
			if err := json.Unmarshal(encoded, &schema); err != nil {
				t.Fatal(err)
			}
			if schema["type"] != "object" || schema["additionalProperties"] != false || schema["properties"] == nil {
				t.Fatalf("%s %s is not closed: %#v", name, label, schema)
			}
			var typed jsonschema.Schema
			if err := json.Unmarshal(encoded, &typed); err != nil {
				t.Fatal(err)
			}
			if _, err := typed.Resolve(nil); err != nil {
				t.Fatalf("unresolvable %s %s: %v", name, label, err)
			}
		}
	}
	for _, name := range []string{"workflow_get", "review_get", "execution_get", "featureArea_create", "featureArea_commit", "execution_cancel", "file_create", "file_replace", "file_delete", "file_move", "review_approval_respond", "task_acceptance_respond", "codex_request_resolve", "feature_area_card_show", "development_context_card_show"} {
		if byName[name] != nil {
			t.Fatalf("old model alias retained %s", name)
		}
	}
	for _, name := range []string{"feature_area_lifecycle", "file_write"} {
		if byName[name].Annotations.ReadOnlyHint || byName[name].Annotations.DestructiveHint == nil || !*byName[name].Annotations.DestructiveHint {
			t.Fatalf("composite risk annotation unsafe %s", name)
		}
	}
	for name := range retired {
		if byName[name] != nil {
			t.Fatalf("retired ChatGPT write %s retained", name)
		}
	}
	for _, name := range []string{"runtime_request_user_decision_submit", "runtime_request_user_decision_prepare_hash", "runtime_request_respond"} {
		if byName[name] == nil {
			t.Fatalf("runtime decision boundary missing %s", name)
		}
	}
	if !byName["runtime_request_user_decision_prepare_hash"].Annotations.ReadOnlyHint || byName["runtime_request_user_decision_submit"].Annotations.ReadOnlyHint {
		t.Fatal("hash/submit mutation classification incorrect")
	}
	if !strings.Contains(session.InitializeResult().Instructions, mcpserver.RuntimeUserDecisionInstructions) {
		t.Fatal("live model instructions omit mandatory user-response policy")
	}
	resources, err := session.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	cardCount := 0
	for _, r := range resources.Resources {
		if r.URI == "ui://devbridge/card" {
			cardCount++
		}
		if r.URI == "ui://devbridge/decision-card" || r.URI == "ui://devbridge/feature-area-card" || r.URI == "ui://devbridge/development-context-card" {
			t.Fatalf("duplicate Card resource %+v", r)
		}
	}
	if cardCount != 1 {
		t.Fatalf("production Card resource count %d", cardCount)
	}
	resource, err := session.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: "ui://devbridge/card"})
	if err != nil || len(resource.Contents) == 0 {
		t.Fatalf("production Card resource unavailable %v", err)
	}
}

type agentRuntimeContractService struct {
	mcpserver.BridgeService
	snapshot agentruntime.Snapshot
}

func (s *agentRuntimeContractService) InitializeSession(_ context.Context, _ aiprojectselection.ClientBindingReference, _ string, id string) (aicontext.InitializationResult, error) {
	if id != "session-1" {
		return aicontext.InitializationResult{}, errors.New("session not found")
	}
	projectID := "project-1"
	return aicontext.InitializationResult{AISessionID: id, State: "PROJECT_BOUND", BoundProjectID: &projectID, AvailableProjects: []aicontext.ProjectIdentity{}, AvailableProjectsStatus: "NOT_APPLICABLE", AvailableProjectsComplete: true}, nil
}
func (s *agentRuntimeContractService) BindProject(context.Context, aiprojectselection.ClientBindingReference, string, string, string) (aicontext.ProjectBindingResult, error) {
	return aicontext.ProjectBindingResult{}, errors.New("fixture binding is already established")
}

func (s *agentRuntimeContractService) GetSession(context.Context, string) (aicontext.SessionContext, error) {
	return aicontext.SessionContext{Session: aisession.Session{ID: "session-1", ProjectID: "project-1"}, Project: project.Project{ID: "project-1"}}, nil
}
func (s *agentRuntimeContractService) GetTask(context.Context, string, string) (task.Task, error) {
	return task.Task{ID: "task-1", ProjectID: "project-1", FeatureAreaID: "area-1"}, nil
}
func (s *agentRuntimeContractService) GetExecutionProposal(context.Context, string, string) (executionproposal.Proposal, error) {
	return executionproposal.Proposal{ID: "execution-1", ProjectID: "project-1", TaskID: "task-1", RunID: "run-1"}, nil
}
func (s *agentRuntimeContractService) GetRunForSession(context.Context, string, string) (run.Run, error) {
	return run.Run{ID: "run-1", ProjectID: "project-1", TaskID: "task-1", FeatureAreaID: "area-1", ExecutionProposalID: "execution-1", AgentRuntime: &s.snapshot}, nil
}
func (s *agentRuntimeContractService) AgentRuntimeStateForSession(context.Context, string, string) (agentruntime.Snapshot, error) {
	return s.snapshot, nil
}

func (s *agentRuntimeContractService) ResolveAgentRequestForSession(context.Context, string, agentruntime.Resolution) (agentruntime.Snapshot, error) {
	return s.snapshot, nil
}

func TestAgentRuntimeMetadataExternalContract(t *testing.T) {
	longMarker := strings.Repeat("metadata-marker-", 2048)
	commandMetadata, err := json.Marshal(map[string]any{
		"id": "command-1", "type": "commandExecution", "status": "completed",
		"command": "go test ./...", "cwd": "/test/repository", "aggregatedOutput": longMarker,
	})
	if err != nil {
		t.Fatal(err)
	}
	contextMetadata := json.RawMessage(`{"id":"compact-1","type":"contextCompaction"}`)
	contextItem := agentruntime.Item{ID: "compact-1", Type: "contextCompaction", Status: "completed", Metadata: contextMetadata}
	now := time.Date(2026, time.September, 11, 8, 0, 0, 0, time.UTC)
	snapshot := agentruntime.Snapshot{
		Agent:       agentruntime.Agent{Name: "codex", SessionName: "contract", ProcessAvailability: "AVAILABLE"},
		AgentHealth: "ACTIVE",
		Identity: agentruntime.Identity{
			ProjectID: "project-1", FeatureAreaID: "area-1", TaskID: "task-1", RunID: "run-1",
			ThreadID: "thread-1", TurnID: "turn-1", ConnectionID: "connection-1",
		},
		Availability:    "AVAILABLE",
		Sequence:        7,
		UpdatedAt:       now,
		LastActivityAt:  now,
		CurrentItem:     &contextItem,
		WaitingState:    "RUNNING",
		ConnectionState: "CONNECTED",
		Items: []agentruntime.Item{
			contextItem,
			{ID: "command-1", Type: "commandExecution", Status: "completed", Metadata: commandMetadata},
			{ID: "message-1", Type: "agentMessage", Status: "completed", Metadata: json.RawMessage(`{"id":"message-1","type":"agentMessage","text":"done","phase":"final_answer"}`)},
			{ID: "array-1", Type: "futureArray", Status: "completed", Metadata: json.RawMessage(`[{"future":"value"}]`)},
			{ID: "null-1", Type: "futureNull", Status: "completed", Metadata: json.RawMessage(`null`)},
		},
	}
	service := &agentRuntimeContractService{BridgeService: readyService(t), snapshot: snapshot}
	_, _, baseURL := startTestServer(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "agent-runtime-contract", Version: "1"}, nil)
	session, err := client.Connect(ctx, &sdkmcp.StreamableClientTransport{Endpoint: baseURL + mcpserver.MCPPath, HTTPClient: &http.Client{Timeout: 2 * time.Second}, MaxRetries: -1, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools := make(map[string]*sdkmcp.Tool)
	for _, tool := range listed.Tools {
		tools[tool.Name] = tool
	}
	stateTool := tools["operation_get"]
	resolveTool := tools["runtime_request_respond"]
	if stateTool == nil || resolveTool == nil {
		t.Fatal("consolidated runtime entry points absent")
	}
	rawResolve, _ := json.Marshal(resolveTool.OutputSchema)
	var envelope map[string]any
	if err := json.Unmarshal(rawResolve, &envelope); err != nil {
		t.Fatal(err)
	}
	advertisedSchema := envelope["properties"].(map[string]any)["result"].(map[string]any)["properties"].(map[string]any)["runtime"].(map[string]any)
	stateSchemaJSON, err := json.Marshal(advertisedSchema)
	if err != nil {
		t.Fatal(err)
	}
	stateDeclaration, _ := json.Marshal(stateTool.OutputSchema)
	if !strings.Contains(string(stateDeclaration), "Official App Server JSON") {
		t.Fatal("operation_get loses official metadata contract")
	}
	properties := advertisedSchema["properties"].(map[string]any)
	currentItemSchema := properties["currentItem"].(map[string]any)
	itemsItemSchema := properties["items"].(map[string]any)["items"].(map[string]any)
	for path, itemSchema := range map[string]map[string]any{
		"currentItem": currentItemSchema,
		"items[]":     itemsItemSchema,
	} {
		metadataSchema := itemSchema["properties"].(map[string]any)["metadata"].(map[string]any)
		if got, want := metadataSchema["type"], []any{"object", "array", "null"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s metadata types = %#v, want %#v", path, got, want)
		}
		if metadataSchema["additionalProperties"] != true {
			t.Fatalf("%s metadata object is not open: %#v", path, metadataSchema)
		}
		if _, hasItems := metadataSchema["items"]; hasItems {
			t.Fatalf("%s metadata incorrectly exposes byte-array items: %#v", path, metadataSchema)
		}
		for _, required := range itemSchema["required"].([]any) {
			if required == "metadata" {
				t.Fatalf("%s still requires metadata: %#v", path, itemSchema["required"])
			}
		}
	}

	var schema jsonschema.Schema
	if err := json.Unmarshal(stateSchemaJSON, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	metadataCases := []struct {
		name    string
		value   any
		omitted bool
		valid   bool
	}{
		{name: "object", value: map[string]any{"future": "value"}, valid: true},
		{name: "array", value: []any{map[string]any{"future": "value"}}, valid: true},
		{name: "null", value: nil, valid: true},
		{name: "omitted", omitted: true, valid: true},
		{name: "string", value: "not production metadata"},
		{name: "number", value: float64(7)},
		{name: "boolean", value: true},
	}
	for _, path := range []string{"currentItem", "items[]"} {
		for _, tc := range metadataCases {
			t.Run("schema/"+path+"/"+tc.name, func(t *testing.T) {
				var candidate map[string]any
				if err := json.Unmarshal(snapshotJSON, &candidate); err != nil {
					t.Fatal(err)
				}
				var item map[string]any
				if path == "currentItem" {
					item = candidate["currentItem"].(map[string]any)
				} else {
					item = candidate["items"].([]any)[0].(map[string]any)
				}
				if tc.omitted {
					delete(item, "metadata")
				} else {
					item["metadata"] = tc.value
				}
				err := resolved.Validate(candidate)
				if tc.valid && err != nil {
					t.Fatalf("valid %s metadata rejected at %s: %v", tc.name, path, err)
				}
				if !tc.valid && err == nil {
					t.Fatalf("scalar %s metadata accepted at %s", tc.name, path)
				}
			})
		}
	}

	calls := []struct {
		name      string
		arguments map[string]any
	}{
		{name: "operation_get", arguments: map[string]any{"aiSessionId": "session-1", "target": map[string]any{"kind": "EXECUTION", "operationId": "execution-1", "ownerTaskId": "task-1", "runId": "run-1"}, "include": []string{"ACTIVITY"}}},
	}
	for _, call := range calls {
		t.Run("tool/"+call.name, func(t *testing.T) {
			result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: call.name, Arguments: call.arguments})
			if err != nil {
				t.Fatalf("%s failed schema validation and may expose metadata: %v", call.name, err)
			}
			if result.IsError {
				encoded, _ := json.Marshal(result.Content)
				t.Fatalf("%s returned error and may expose metadata: %s", call.name, encoded)
			}
			encoded, err := json.Marshal(result.StructuredContent)
			if err != nil {
				t.Fatal(err)
			}
			var output map[string]any
			if err := json.Unmarshal(encoded, &output); err != nil {
				t.Fatal(err)
			}
			output = output["result"].(map[string]any)["activity"].([]any)[0].(map[string]any)["telemetry"].(map[string]any)
			if _, ok := output["currentItem"].(map[string]any)["metadata"].(map[string]any); !ok {
				t.Fatalf("%s currentItem metadata lost object type", call.name)
			}
			items := output["items"].([]any)
			if command, ok := items[1].(map[string]any)["metadata"].(map[string]any); !ok || command["aggregatedOutput"] != longMarker {
				t.Fatalf("%s command metadata lost long object marker", call.name)
			}
			if _, ok := items[2].(map[string]any)["metadata"].(map[string]any); !ok {
				t.Fatalf("%s agentMessage metadata lost object type", call.name)
			}
			if _, ok := items[3].(map[string]any)["metadata"].([]any); !ok {
				t.Fatalf("%s array metadata lost JSON array type", call.name)
			}
			if value := items[4].(map[string]any)["metadata"]; value != nil {
				t.Fatalf("%s null metadata = %#v", call.name, value)
			}
		})
	}
}

func TestCurrentDocumentationFreezesFeatureAreaReviewVocabulary(t *testing.T) {
	files := map[string][]string{
		"../../README.md":                      {"Alpha", "FeatureArea", "Task", "Review", "Document", "MIT"},
		"../../docs/interfaces.md":             {"Project → FeatureArea → Task", "Model-facing GPT MCP discovery registers the shared business tools below", "document_create", "preserves authored instruction bytes", "Approval response includes the exact"},
		"../../docs/user-guide/README.md":      {"Requirement → FeatureArea → Task → Review → Approval", "explicit start"},
		"../../docs/user-guide/workflow.md":    {"A Review", "Review Approval", "immutable initial definition", "Task Acceptance"},
		"../../docs/user-guide/terminology.md": {"| Review | A read-only engineering operation owned by an existing Task", "| Task |", "| Document | Explicitly requested durable Project knowledge"},
	}
	for path, required := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read current product documentation %s: %v", path, err)
		}
		text := string(content)
		for _, phrase := range required {
			if !strings.Contains(text, phrase) {
				t.Fatalf("current product documentation %s omits %q", path, phrase)
			}
		}
		for _, forbidden := range []string{"ReviewKind", "ReviewTarget", "Project -> Topic -> Review -> Task Preparation", "Project → Topic → Review → Task Preparation", "Topic Review", "Topic-level engineering", "TaskPreparation"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("current product documentation %s exposes ambiguous vocabulary %q", path, forbidden)
			}
		}
	}
}

func assertClosedObjectSchemas(t *testing.T, label string, value any, paths ...string) {
	t.Helper()
	path := ""
	if len(paths) > 0 {
		path = paths[0]
	}
	switch current := value.(type) {
	case map[string]any:
		// Only these protocol-owned JSON envelopes are intentionally open.
		// Bridge identities, state and every other tool remain closed. Core
		// validates answers against the generated Codex response contracts.
		if current["additionalProperties"] == true {
			if (strings.HasPrefix(label, "agent_runtime_state ") || strings.HasPrefix(label, "codex_request_resolve ")) && (path == "/properties/currentItem/properties/metadata" || path == "/properties/items/items/properties/metadata") {
				wantTypes := []any{"object", "array", "null"}
				if !reflect.DeepEqual(current["type"], wantTypes) {
					t.Fatalf("%s metadata schema at %s is not the exact opaque JSON contract: %#v", label, path, current)
				}
				if _, hasItems := current["items"]; hasItems {
					t.Fatalf("%s metadata schema at %s constrains RawMessage as a byte array: %#v", label, path, current)
				}
				return
			}
			if strings.HasPrefix(label, "agent_runtime_state ") || strings.HasPrefix(label, "codex_request_resolve ") {
				for _, opaque := range []string{
					"/properties/resolution/properties/response",
					"/properties/requests/items/properties/payload",
					"/properties/requests/items/properties/resolution",
					"/properties/requests/items/properties/availableResponses/items",
					"/properties/lastError/properties/details",
					"/properties/lastWarning/properties/details",
					"/properties/diagnostics/items/properties/details",
				} {
					if path == opaque && current["type"] == "object" {
						return
					}
				}
			}
			presentationTool := false
			for _, name := range []string{"feature_area_presentation_get ", "feature_area_card_show ", "task_presentation_get ", "dev_bridge_card_get ", "development_workspace_get "} {
				presentationTool = presentationTool || strings.HasPrefix(label, name)
			}
			if presentationTool {
				for _, suffix := range []string{
					"/properties/taskView/properties/activity/properties/runtime/properties/currentItem/properties/metadata",
					"/properties/taskView/properties/activity/properties/runtime/properties/items/items/properties/metadata",
					"/properties/latestTaskView/properties/activity/properties/runtime/properties/currentItem/properties/metadata",
					"/properties/latestTaskView/properties/activity/properties/runtime/properties/items/items/properties/metadata",
				} {
					if strings.HasSuffix(path, suffix) {
						wantTypes := []any{"object", "array", "null"}
						if !reflect.DeepEqual(current["type"], wantTypes) {
							t.Fatalf("%s presentation metadata schema at %s is not exact: %#v", label, path, current)
						}
						if _, hasItems := current["items"]; hasItems {
							t.Fatalf("%s presentation metadata schema at %s constrains RawMessage as bytes: %#v", label, path, current)
						}
						return
					}
				}
			}
			t.Fatalf("%s contains an unapproved open object schema at %s: %#v", label, path, current)
		}
		if current["type"] == "object" {
			if current["additionalProperties"] != false {
				t.Fatalf("%s contains an open object schema: %#v", label, current)
			}
			if _, ok := current["properties"].(map[string]any); !ok {
				t.Fatalf("%s contains an object schema without explicit properties: %#v", label, current)
			}
		}
		for key, nested := range current {
			assertClosedObjectSchemas(t, label, nested, path+"/"+key)
		}
	case []any:
		for _, nested := range current {
			assertClosedObjectSchemas(t, label, nested, path)
		}
	}
}

func callToolMap(t *testing.T, ctx context.Context, session *sdkmcp.ClientSession, name string, arguments map[string]any) map[string]any {
	t.Helper()
	originalName := name
	name, arguments = surfaceMutationTestArgs(name, arguments)
	result, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		encodedContent, _ := json.Marshal(result.Content)
		t.Fatalf("%s returned error: %s", name, encodedContent)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(encoded, &output); err != nil {
		t.Fatal(err)
	}
	return surfaceMutationTestResult(originalName, output)
}
