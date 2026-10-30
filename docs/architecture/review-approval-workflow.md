# Task Review gate approval and depth (schema v46)

The [unified Review executor contract](unified-review-executors.md) extends this document with
Task-owned UPPER_LLM Reviews, strict read grants, formal report submission and
atomic result/permission/claim closure. Generic Direct File writes and Coding Agent
configuration retain their existing meanings.

The [Task-owned contract](task-execution-v0.1.md) remains authoritative for
ownership, phases, immutable TaskDefinitionRevision, Execution and Acceptance.
This document defines its orthogonal ReviewDepth contract, introduced in schema
v45 and retained in v46. The v46 next-run runtime envelope remains independent
of ReviewDepth and does not change this approval state machine.

Task exists before every Review and remains the semantic workflow owner:
`Task → Review [0:N] → Run [1:1]`. Review is a technical read-only operation.
Every attempt creates a distinct Review with one Run. `current_review_id` fences
succession; Review ordinal never determines depth or gate outcome.

## Canonical structured contract

| Field | Contract |
| --- | --- |
| `ReviewDepth` / `reviews.review_depth` | Required explicit immutable `FAST` or `DEEP`; no default for new Reviews. |
| `tasks.required_review_depth` | Nullable durable minimum, with `FAST < DEEP`; required during Review work/in-flight retry, null outside that gate. |
| `reviewDisposition` / `reviews.review_disposition` | `APPROVAL_READY` or `FURTHER_REVIEW_REQUIRED` on valid successful structured completion; absent for infrastructure failure. |
| `ReviewPlan.mode` | Independent `FOCUSED` or `REPOSITORY`; no depth inference. |
| `reasoningEffort` | Independent, validated against the selected model capability (`low` through `max`; capable models also support `ultra`). An omitted override resolves the model’s Desktop default. |
| `model` | Independently supplied configured coding-agent model. |

The existing structured Review result retains its version, summary and proposal
fields. The machine output schema additionally requires `reviewDisposition`.
Summary text, finding counts, magic prefixes and prompt keywords never determine
whether Approval is available.

FAST / DEEP describes semantic review depth only.

It does not alter:

- Review ownership
- Review-to-Run cardinality
- authorization
- ReviewPlan scope mode
- model
- reasoning effort
- Coding Agent prompt

FAST + xhigh, FAST + REPOSITORY, DEEP + medium and DEEP + FOCUSED are all valid.
Both depths retain the existing repository, FeatureArea, filesystem, Git,
mutation, network and runtime authorization boundaries.

## Durable transition table

All completion changes below commit atomically with the existing completion
transaction. `current_review_id` remains the completed Review until the caller
explicitly admits a newer one.

| Trigger | Task phase/status after transition | Required depth | Approval |
| --- | --- | --- | --- |
| `task_create` | ACTIVE / REVIEW_REQUIRED | FAST | None |
| Admit FAST when minimum FAST | ACTIVE / REVIEWING | FAST | None |
| Admit DEEP when minimum FAST or DEEP | ACTIVE / REVIEWING | DEEP | None |
| Current FAST or DEEP returns APPROVAL_READY | ACTIVE / REVIEW_APPROVAL_PENDING | null | Current Review only |
| Current FAST or DEEP returns FURTHER_REVIEW_REQUIRED | ACTIVE / REVIEW_APPROVAL_PENDING | null | Current Review confirmation permits REVISE or REJECT only |
| Review infrastructure failure | ACTIVE; existing recoverable phase preserved | Admission minimum preserved | None |
| Approval REVISE / REQUEST_RE_REVIEW | ACTIVE / REVIEWING | Inherited floor | Old Decision is historical; exact successor Review/Run is authorized |
| Approval APPROVE | ACTIVE / IMPLEMENTATION_READY | null | Exact receipt retained |
| Approval REJECT | CANCELED; existing phase retained | null | Exact receipt retained |
| Task becomes terminal | Existing terminal semantics | null | No new Approval |

A FAST Review may satisfy the Review gate directly.

A FAST Review may instead return structured FURTHER_REVIEW_REQUIRED, causing the
Task to enter Review confirmation. APPROVE is unavailable; REVISE preserves the
Review receipt and atomically creates the same Task's next Review with a DEEP
minimum and durable start authority.
Repeated DEEP attempts are allowed, including repeated
FURTHER_REVIEW_REQUIRED results. Their Runs succeed; this disposition is not
FAILED, INFRASTRUCTURE_FAILED, SCOPE_CONFLICT, UNKNOWN or recovery state.

Infrastructure failure emits no disposition and no Approval. An admitted DEEP
Review retains a DEEP retry minimum. UNKNOWN, UNKNOWN_ORPHANED, late callbacks,
response-loss replay, claim fencing and runtime epochs keep existing semantics.

## Admission and latest-only Approval

Canonical `task_review_start` requires `taskId`, `reviewDepth`,
`reviewInstruction`, `reviewPlan`, `model`, `reasoningEffort`, request identity
and the existing repository/exact-owner fields. The requested depth must satisfy
the durable Task minimum. `REVIEW_DEPTH_TOO_SHALLOW` includes `requestedDepth`,
`requiredReviewDepth`, `taskId` and `phase`; rejection creates no Review, Run,
claim or successful mutation receipt. Exact request replay retains its original
committed outcome; changed payloads conflict. FeatureArea serialization remains
unchanged, while different FeatureAreas may run in parallel.

Approval subject is the Task Review Gate. A legal Approval requires all three:
current Review, APPROVAL_READY disposition, and REVIEW_APPROVAL_PENDING Task
phase. FURTHER_REVIEW_REQUIRED creates the same Decision authority with only
REVISE and REJECT options. Stale Review approval
requests return `STALE_REVIEW_APPROVAL`; history never offers an older Review as
an alternative approval source.

APPROVE freezes the initial immutable definition and moves the existing Task
to IMPLEMENTATION_READY. The product `APPROVE_AND_START` composition supplies
`START_AUTHORIZED`, creating the Coding Execution and its start effect in that
same transaction; the continuation worker launches it after commit. For explicit
DIRECT_FILE implementation, the transaction records its revision-bound file
authorization as described below.
Runtime permission decisions and final Task Acceptance remain separate gates.

Approval receipts bind the Task gate, source Review, result/provenance hashes,
request identity and resolution. Exact receipt replay returns the committed
result and never authorizes an old Review again. Original intent and history
remain immutable across Review cycles.

## Prompt and product projections

Caller-authored `reviewInstruction` bytes equal stored `final_codex_prompt` and
actual sent prompt bytes, including whitespace, Unicode, newlines and symbols;
SHA-256 values also match. Bridge adds no depth, minimum-depth or disposition
prefix/suffix. The Upper LLM authors any engineering instructions; structured
schema is the machine protocol. Depth never rewrites the semantic Task title.

Shared client-neutral projection exposes `requiredReviewDepth` and
`allowedReviewDepths`: FAST maps to `[FAST, DEEP]`, DEEP to `[DEEP]`, and no
Review requirement to `null` / `[]`. Current Review and each history item expose
`reviewDepth` and any available `reviewDisposition`. Clients consume the shared
facts without deriving them from ordinal, summary or status.

When REVIEW_REQUIRED needs FAST, WebUI offers 快速审查 and 深度审查; when it needs
DEEP, it offers only 深度审查. History displays `Review #N · FAST/DEEP`.
FURTHER_REVIEW_REQUIRED is shown as needing deeper Review, while APPROVAL_READY
continues to show waiting for Approval. Task remains selected; Review
instruction belongs in Review detail/activity. Card, Console and JSON use the
same minimum, allowed choices, depth and disposition facts.

## v44 to v45 migration

Historical Reviews backfill as DEEP because pre-depth Review used the complete
Review behavior. Existing Review Approval Decision evidence alone backfills
APPROVAL_READY. Raw historical Review results, prompt bytes, receipt evidence
and hashes are preserved; compatibility parsing of persisted approved evidence
does not change the strict schema for new completions.

Safe quiescent REVIEW_REQUIRED Tasks retain their phase and require FAST.
Terminal failed/canceled Review attempts in REVIEWING retain that phase and
require DEEP. Pending Approval with its existing successful Review/Decision,
implementation and terminal Tasks retain their phases and clear the minimum.
Migration does not create Reviews, Runs or Approval Decisions and never advances
Task.phase.

An ambiguous active/in-flight pre-depth Review, an unprocessed successful
REVIEWING result, or pending Approval without its successful Review/Decision
fails closed with explicit development database rebuild guidance. The failed
transaction preserves schema version, data and history. Historical schema files
remain unchanged; v45 is a new canonical schema and migration.

Retire, FeatureArea Workspace/AgentConversation ownership and Task
commit/FeatureArea submission are unchanged. Production recall host integration
remains `HOST_INTEGRATION_PENDING`.

## User decision and deterministic validation boundary

The reviewing LLM explains the implementation plan, file scope, risks and any
adjustments from the original requirement in the proposal summary. The user
decides whether to accept those engineering choices. Bridge validates explicit
structure, supported configuration, exact ownership/current versions, lifecycle,
permissions, path safety and receipt identity; it does not compare natural-language
goals or constraints for equality, containment or semantic equivalence.

APPROVE freezes the visible proposal, without inserting original constraint text
or having Core rewrite engineering content. When GPT explicitly supplies final
executionInstructions with APPROVE, only the canonical instruction fields use
those supplied bytes; other proposal fields and the immutable source Review,
result/provenance hashes remain intact. The instruction source is recorded separately. Original requirements are historical source facts,
not permanently interpreted engineering permissions. A changed approved definition
requires a new explicit decision and immutable revision; retry, resume and receipt
replay cannot substitute a different definition or scope.

The compact approval area remains concise. The existing Review result and Task
details expose the actual proposal; the schema asks the LLM to explain adjustments
without adding a mandatory semantic diff gate or rewriting caller instructions.
Shared Core actions remain authoritative for every client. An exact, recognized
Core admission rejection followed by successful readback of the same approval
identity is shown as “审批未通过” with its code and reason. Transport failure,
unclassified errors, failed readback or a changed approval identity retain UNKNOWN.
The original request ID and payload remain available for explicit replay; no client
automatically retries a rejected or unknown request.

Review executor kind does not select an implementation channel. The approved
WorkSpec.implementationMode selects CODING_AGENT or DIRECT_FILE explicitly. APPROVE
does not start an agent or perform file operations; DIRECT_FILE also materializes
the approved file authority atomically. Runtime configuration and file-access
checks remain distinct from Review executor selection.

Original requirement source is projected separately as `originalRequirement`:
`SOURCE_AVAILABLE` includes the retained creation event identity, original goal
and constraints; `UNTRACEABLE` explicitly marks legacy records without reliable
creation evidence. Neither a current Task field nor a proposal is used to invent
an unavailable initial requirement. Card Details and Web Task details consume this
same source projection.

The earlier approval-boundary review recorded missing implementation-channel and
Coding backend per-path authorization. The explicit channel and Direct File authority
contract below supersedes the former gap for DIRECT_FILE. Coding Agent Implementation
PATHS remains metadata in its existing backend contract; this change does not claim to
add per-path Coding backend enforcement or rewrite existing Coding executions.

## Explicit analysis of one Review

The Card copies `Analysis review result <reviewId>` with the complete selected Review
ID. Copying does not send a message or authorize a workflow mutation. When the user
submits that line, use the conversation's existing real Session and, only if needed,
a read-only binding lookup. Read `operation_get` with `target.kind = REVIEW`, the
exact supplied `operationId`, and `include = [RESULT]`; add EVIDENCE only as needed.
Historical targets remain historical. Missing Session/binding, wrong Project or
unavailable results are reported without changing identities, creating a Session,
binding a Project or advancing work. Explicit analysis is allowed for GPT's own
report; only unsolicited repeated self-analysis is suppressed.

The Card main surface shows only Review type/ordinal/depth and status. Report
summary and proposal stay in the existing read-only details screen, opened through
“查看审查结果”; that entry and “复制分析指令” use the same explicit Review object,
Task ownership and bound Project context. Historical rows use their own Review.
Changing the current Review invalidates open report content and pending readback;
refreshing the same Review retains the existing details state.

ChatGPT Card is now a read-only component: approval, revision, rejection, Task
cancellation, runtime-permission and next-run configuration forms are removed.
Actual configuration and pending decisions remain visible as facts. Its compact
view/copy/refresh controls do not confer or revoke Core authority. WebUI/Desktop
retain their original business controls and exact-target validation.

Conversation is the ChatGPT business control path. “批准方案，暂不执行” approves
only the accurately identified proposal. “确认实施”, when the plan, target and
channel are explicit, authorizes GPT to prepare final executable instructions,
resolve the required approval and use the channel's original execution entry;
it does not require a second ceremonial “开始执行”. Real ambiguity still requires
clarification. GPT obtains fresh Core state and exact revision/Decision/target for
every command; Card browsing, copied text and old presentation state never supply
implicit authority. Showing Card or opening/analysing a report is not a prerequisite.
Runtime permissions remain separate: explain the actual request and respond only
with explicit user authorization, never automatic session-wide permission.

## Atomic Direct File Task approval

`workSpec.implementationMode` is the single proposed and immutable implementation
channel: `CODING_AGENT` or `DIRECT_FILE`. New proposals require it. Historical
missing fields remain UNKNOWN; neither Review executorKind nor natural-language
instructions select a channel. Coding approvals retain their explicit start path.

For DIRECT_FILE, APPROVE atomically freezes the exact definition, binds the original
approval receipt to one Core-owned workflow and stores the exact Session, Project,
FeatureArea, Task, revision, Decision, Repository/Workspace and approved scope. Upper
Review read grants remain separate and are not upgraded. A ready workspace returns
ACTIVE file access with allowedOperations; technical preparation is reported as
PREPARING or an accurate failure, never as execution or another approval gate.
The database transaction does not claim to roll back workspace preparation.

The model-visible decision result contains `reviewApproval.directFile` and its
original receipt/revision. `workspace_get` for the exact Task returns the shared
`taskView.implementationMode` and `taskView.directFile`, including workflowId,
state, exact owners, worktreeId/workspaceId/repositoryId, allowedOperations,
executionScope and failure facts. These are pure reads and never create grants.
After an explicit implementation request, GPT uses that original Session and ACTIVE
workflow with the original file APIs. It does not call enable, ask for an ID, choose
another Task or launch Codex. Shared actions omit Codex start for DIRECT_FILE; a ready
grant is shown as ready, with actual file activity only after a recorded operation.

Each file operation still passes through the original authority and filesystem
containment, symlink, size and whole-file operation checks. The current Task/revision,
approved scope and revocation are rechecked across the operation coordination boundary.
Closing authorization does not accept, verify or complete the Task. Receipt replay
returns the original workflow identity but never reopens a closed authority; current
readiness and historical approval are separate facts.

`direct_file_reconcile_task_authorization` is an explicit maintenance write for an
exact existing receipt and current revision, not a normal approval follow-up. It may
complete an already structured DIRECT_FILE approval idempotently. Legacy absent modes
require the user's explicitly confirmed DIRECT_FILE selection (`implementationMode`
and `confirmed=true`); this records provenance without rewriting the old proposal/hash.
It cannot infer a channel from prose, replace a Task or revive closed authorization.
`direct_file_enable` remains only for authorized standalone, non-Task file requests.

Core's existing continuation loop resumes only persisted PREPARING intents. Missing
initial workspace/worktree preparation delegates to the existing workspace manager
outside the approval transaction, then rechecks the same approval before ACTIVE.
A missing previously reviewed workspace, a replaced worktree or quarantine is a
continuity failure; preparation never substitutes a clean workspace for reviewed
content. Failures remain visible and technical retries reuse the original intent.

New Coding authorization binding requires CODING_AGENT. A legacy definition without
a channel can continue its already confirmed or consumed exact Coding Execution; it
cannot use a missing field to create a new Coding authorization. REQUEST_CHANGES in
the existing Execution acceptance contract creates Coding successors only; a new
DIRECT_FILE value is rejected there after historical receipt replay is checked.
Direct File workflow closure does not invent an Execution result or bypass the
formal verification and user acceptance requirements.

The conversational command path retains the exact typed Review Approval receipt
and rereads the same Task after success; readback failure does not retry its write.
Card only reads those facts. Fixed historical entries keep their original identity,
with read-only navigation back to the corresponding current Task protected by
Session, Project, Task, generation and response-identity checks. A current Task
entry may show its later Execution without treating an old Review as a new target.
No Card pending mutation, configuration draft, context push or completion wake
participates in this handoff. Card refresh may show a persisted result; GPT analyses
it only when the user requests analysis or continuation in conversation.

## Executable instruction handoff

Review `summary` is user-facing analysis and may describe a pending decision.
`workSpec.codexInstruction` is the complete Implementation candidate;
`verificationSpec.codexInstruction` is a separate complete Verification candidate
when mode is RUN. Producing a candidate does not authorize dispatch. Core retains
the approval gate, so conversational approval or direct WebUI/Desktop approval
can freeze usable candidates
without another message, Review or confirmation.

GPT incorporates the user's final choices, including any explicitly accepted
version differences, into self-contained instructions: role, goal, concrete work,
exact scope, preserved content, technical limits, checks and deliverables. It
removes resolved choices and redundant approval requests without erasing real
permissions, conflicts or restrictions. The executor need not obtain previous
chat messages, a user's approval quote or an approval receipt. Core does not scan
approval words, judge instruction quality or edit the text at dispatch time.

`REVIEW_APPROVAL/APPROVE` accepts optional `executionInstructions` with an
implementation string and verification text only for a RUN definition. Without
this object, existing proposal candidates are used. With it, the original Review
and its hashes stay unchanged; explicitly supplied instruction bytes are adopted
in the immutable approved definition with their own source. REVISE, REJECT and
unrelated Decisions reject this input. It cannot alter goal, scope, channel or
runtime configuration.

After conversational or WebUI/Desktop approval, executable approved instructions use the
normal start path. If a later explicit user decision needs instruction-only
finalization, the same narrow object may accompany `execution_start`, with a
stable requestId and exact observed expectedTarget, for an eligible initial
CODING_AGENT candidate that has not been dispatched. The original approved revision
is retained; the source-linked successor, Task pointer and Execution reservation
must agree before dispatch. Repeated identical requests reuse that reservation;
changed texts conflict. Dispatched or terminal work cannot use this path, and
substantive scope/goal changes retain their formal user decision route. DIRECT_FILE
continues to use file authorization and never starts Codex for prompt finalization.

Illustrative complete candidate pair for a small, explicitly scoped change:

- Implementation: “Perform this Implementation in the Core-provided workspace.
  In src/config.go, reject negative timeout values with a configuration error;
  preserve zero and positive timeout behavior and all unrelated edits. Modify
  only that file. Inspect the diff and format that file; do not run tests,
  services, Git reset/merge or commits. Deliver the changed behavior, the actual
  checks and any unresolved technical blockers.”
- Verification: “Perform Verification of src/config.go in the Core-provided
  workspace. Read the final diff and trace the timeout validation: negative
  values must return a configuration error, while zero and positive values keep
  their prior behavior. Do not modify files, run tests/services or change Git
  state. Report observed evidence for each criterion, limits of this static
  inspection and unresolved technical blockers.”

These are candidate content examples, not injected runtime text or authorization.
The actual persisted prompt/hash and emitted prompt remain derived from the chosen
revision's canonical instruction fields, separately from source Review hashes.
