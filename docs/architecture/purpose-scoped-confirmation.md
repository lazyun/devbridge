# Purpose-scoped confirmation and ChatGPT MCP

本轮独立工程状态恢复的合同、实施与未闭合边界见 [Recovery Status 记录](codex-recovery-status-reconciliation-v1.md)。新 Recovery Turn 不证明旧回答被消费；旧 UNKNOWN 与普通 finalization guard 保留。

本轮 CODEX_REQUEST v1 的权威状态、UNKNOWN 阻断、取消/successor、统一客户端投影及验收见 [回答消费状态合同](codex-request-unknown-disposition-v1.md)。该合同保留固定 0.149.0 的 `CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED`，历史报告中的阶段性状态不代表本轮验收结论。

> Project-centric update (schema v77): [current contracts and remaining boundaries](project-centric-multi-repository-closure.md). Multi-member Upper Review, CODING_AGENT Review, v2 NO_START approval, explicit Coding Implementation/Verification, Acceptance, and recoverable per-repository Task commits and FeatureArea merges are complete under Trusted Executor Model v1. Workspace membership never grants access; the legacy single-repository descriptions below remain compatibility behavior.

> Coding runtime update: [actual binary/schema and offline isolation evidence](coding-runtime-capability-evidence.md). Multi-repository CODING_AGENT start, required Verification.RUN, and Coding Review use the durable RuntimeWorkset and production Client wire. Hostile sandbox hardening remains deferred; the installed helper did not prove complete temporary-directory denial. Confirmation reads/options do not create Execution, claim or handoff, and no waiver or alternate Thread bypass is used.

GPT prepares complete engineering content; the dedicated Card confirms a stored
version and structured choices; Core validates and executes its original commands.
The existing results Card remains read-only. A pending intent is not a Task,
Review, Execution, approval, claim, or file grant.

## Multi-repository Direct File confirmation

`EXECUTION` additionally owns `START_DIRECT_FILE`, `RECOVER_DIRECT_FILE` and
`CANCEL_DIRECT_FILE`, bound to the exact approved Task/revision/decision. Recovery
and cancellation include the observed workflow/epoch/generation. Only a consumed
CONFIRMING intent with the exact persisted UNKNOWN command may invoke the writer.
Prepare/options/GET are read-only. APPROVE remains NO_START; APPROVE_AND_START does
not silently approve or start this v2 path. Unsupported RUN verification blocks start.
An optional USER_WAIVED selection requires the user's checked choice and reason;
it is omitted by default and cannot be inferred by Core or the model.

`FEATURE_AREA / MERGE_FEATURE_AREA` freezes a complete per-repository delivery plan:
source commit/branch, target developmentRef and expected target HEAD, cycle identity,
and exact accepted Task commit provenance. Commit rechecks drift. Recovery reuses
the original stored plan and receipts, even after development mappings change.
Task acceptance and Task commit do not authorize this merge. Partial completion
remains visible until every frozen required member is complete.

## Purpose and command ownership

| Purpose | Model preparation | Confirmed operation |
| --- | --- | --- |
| PROJECT_SETUP | `project_setup_prepare` | Bind the existing Session to an authorized Project and register an existing Repository branch. Each subresult is separate. |
| FEATURE_AREA | `feature_area_prepare` | Create the stated area only, or its complete initial Task and one prepared Review. |
| TASK | `task_prepare` | Create the stated Task in its exact area; start a Review only when prepared and selected. |
| REVIEW | `review_prepare` | Start that Task's first/revised Review, or use `task_review_request` then admission for the exact supplemental target. |
| EXECUTION | `execution_prepare` | Approve/start the selected proposal, start an immutable approved candidate, or use the original retry/changed-implementation successor protocol. |

Every successful prepare returns `structuredContent.intent` with a durable `intentId`, immutable `version`, `purpose`,
`contentHash`, complete typed content, and known target identities. `confirmation_show`
only renders that existing intent at `ui://devbridge/confirmation/v5`.
`confirmation_get` and `confirmation_options_get` are pure reads. Every public read
carries the exact identity `aiSessionId`, `intentId`, `version`, `purpose` and the
required frozen `contentHash`; Core re-verifies the digest against durable content and
refuses a mismatch as a read miss that changes nothing. Options expose
Core model capabilities and their inventory source/revision; project candidates
are authorized before reading Repository/branch facts. A branch `headCommit` is
an observation, not a branch lock or a promise that its HEAD cannot change.
No default Project, branch, model, or reasoning effort constitutes confirmation.

An intent is immutable after prepare, so `version` is never incremented to express a
successor: replacing a still-pending intent allocates a new `intentId` that is also
`version: 1` and records the prior id in `sourceIntentId`. `version` is not a
replacement ordinal, lineage generation, Decision version or Task Review ordinal, and
replacement order comes only from the `sourceIntentId` chain.

## Optional Review at Task creation

The TASK Card exposes **开启审查** as optional `selection.startReview` on
`confirmation_commit`, not as an edit to prepared content:

| Prepared action | Omitted selection (legacy) | `false` | `true` |
| --- | --- | --- | --- |
| `CREATE_TASK` | Task only | Task only | Refused: `TASK_REVIEW_NOT_PREPARED` |
| `CREATE_TASK_AND_REVIEW` | Task + prepared Review | Task only | Task + prepared Review |

Preparation still requires `TaskBody.startReview` to match the explicit action.
A Review-capable preparation freezes complete instructions, executor variants,
FAST/DEEP depth and resolved scope. The selection can only remove its optional
Review step; it cannot add an unprepared operation. The frozen action, contentHash,
variant identity and original Task definition remain unchanged when unchecked.
This selection is rejected outside TASK creation; FEATURE_AREA composition and
independent REVIEW confirmations retain their existing contracts.

`confirmation_options_get` accepts an optional top-level `startReview` and echoes
its effective value in TASK options. A false choice qualifies only the original
Task prerequisites, without requiring Review scope/executor/runtime/backend
availability. Core repeats that qualification at commit, schedules only
`CREATE_TASK`, and refuses unused backend/runtime selections. The Card refreshes
options on a toggle and again before commit, and refuses options for another choice.
The regular receipt GET never accepts a new selection.

Explicit false, true and omission are distinct request/hash identities. The
optional field is omitted for legacy requests, including exact replay; a consumed
or unknown request cannot be changed by toggling the draft. Prepared Task-and-Review
Cards default to checked; Task-only Cards show the disabled unchecked control and
request a new preparation to enable Review. Task creation alone still leaves the
Task requiring Review and does not waive implementation approval. This does not
activate the unavailable standalone WebUI operator task-create command or borrow
an AI Session as operator authority.

## Caller context and exact Session handoff

The actual MCP server registers `SessionScopeMiddleware` through
`RegisterSurfaceBusinessTools`. For every tool call it first establishes the
caller using the same `withSessionRequest` rule as `session_initialize`, then
lets confirmation tools perform their own Session admission. Skipping ordinary
BOUND-only admission or workflow enrichment does not skip caller establishment.
The confirmation handlers retain this context without parsing identity again.

Scope generation is unchanged: the adapter uses `authenticated:` plus the
original `bindingDigest` of SDK-authenticated `TokenInfo.UserID`, or the existing
`data-instance-local` contract at the anonymous adapter boundary. Ordinary
arguments, headers, widget state, `openai/session`, and tool visibility do not
select an owner. If an already established caller scope or binding reference
disagrees with the request's authenticated scope, admission rejects the conflict
instead of overwriting one source. Tool names do not participate in the scope.

The neutral `confirmationCallerReference` helper consumes only
`aisession.CallerScopeFromContext`. It validates a
`ClientBindingReference{CallerScope: scope}` using the existing `ref.Valid()`;
it neither supplies a default scope nor imports MCP metadata parsing into Core.
Host reference and selection handle are unnecessary for exact Session reads.
There is no database-owner impersonation or WebUI `LOCAL_OPERATOR` fallback.

`confirmationSession` calls the existing initialization service with that
reference, **empty initialization request ID**, and the exact original Session
ID. This selects `ReadInitialization`; it never creates a Session. A prepare
request ID remains the separate intent idempotency key, so prepare legitimately
contains both its request ID and Session ID. Do not delete that request ID or
forward it into Session creation to work around a handoff error.

| Entry | Caller and Session admission after the shared middleware |
| --- | --- |
| `project_setup_prepare` | Reads the exact existing Session; UNBOUND is permitted for Project selection. |
| `feature_area_prepare` | Reads the exact Session and requires BOUND before the original area validation. |
| `task_prepare` | Reads the exact Session and requires BOUND before the original Task validation. |
| `review_prepare` | Reads the exact Session and requires BOUND before the original Review/target validation. |
| `execution_prepare` | Reads the exact Session and requires BOUND before the original immutable variant/target validation. |
| `confirmation_get` | Reads the Session, then the exact Session/intent, purpose/version and required frozen contentHash; authorization readback remains pure. |
| `confirmation_options_get` | Uses the same exact five-field read identity before candidate and current-capability reads. |
| `confirmation_show` | Uses the same exact read identity to display an existing intent; no implicit prepare. |
| `confirmation_commit` | Uses that read before original consumption, replay/resume checks and canonical child commands. |

`BIND_PROJECT` uses the same helper and context, the intent's original Session,
the explicitly selected Project and the original stable child request ID.
Binding and branch selection keep separate recorded outcomes. The existing
binding receipt path does not repeat bootstrap for an already bound readback.
All child commands and Upper/Direct File authorization reads retain the verified
context. `context.WithoutCancel(ctx)` is used only to record a command's outcome
and preserves caller values; it does not authenticate from historical receipts.

Missing or invalid neutral caller context returns the typed
`CONFIRMATION_CALLER_CONTEXT_REQUIRED` error before Session-service invocation.
An adapter identity conflict or a durable Session-owner mismatch retains
`SESSION_SCOPE_MISMATCH`; a missing Session retains `AI_SESSION_NOT_FOUND`.
Non-Setup UNBOUND input returns `SESSION_PROJECT_REQUIRED`. Genuine external
initialization shape errors retain `INVALID_SESSION_INITIALIZATION`; intent,
version, target and business validation errors retain their own contracts.
New Session-first rows have durable caller scope; the pre-existing SQL-NULL
legacy-owner compatibility is unchanged, not a new confirmation bypass.

This handoff correction does not prove prepare's business validation will pass,
authorize retry of UNKNOWN work, change an UNKNOWN implementation mode, or
revive closed file grants. No migration or Card change is needed.

The Card has five purpose views, one primary confirmation action, compact controls,
folded complete content, and a separate readback. It does not edit engineering text.
Review offers Codex or GPT file review. Execution offers Codex or GPT file
implementation only for prepared, currently supported variants. File modes hide
Codex controls. Review depth and Codex Fast Mode are independent fields.

## Stored version, exact content, and original receipts

`internal/confirmation` defines the transport-neutral intent contract. The SQLite
store binds it to the existing caller and Session, immutable content and selection,
and stable child command request IDs. A replacement creates a new intent linked
by `sourceIntentId`; only a still-pending prior intent may be superseded. Confirmed
content is never overwritten. Closing a Card has no business cancellation effect.

Because an intent is frozen at prepare, its `version` always reads `1` and never
advances: `version` is neither a replacement ordinal nor a lineage generation, and it
does not mirror a Decision version or a Task Review ordinal. Each replacement is a new
`intentId` with its own `contentHash` at `version: 1`, so `intentId + version + contentHash`
is the complete immutable identity of one proposal and lineage is expressed only by
`sourceIntentId`. Sorting or ordering replacements by `version` is never valid.

A public read is answered only for that exact identity. `contentHash` is a required
read field rather than a filter: the schema rejects absent, empty and malformed
digests, Core compares the supplied digest with durable content, and any difference
returns `CONFIRMATION_READ_IDENTITY_MISMATCH` from `INPUT_IDENTITY` before a write,
receipt, Decision consumption or Card mount. `get`, `options_get` and `show` share that
one validator, and no id-only public path exists that could later gain commit authority.

`confirmation_commit` accepts only the exact intent/version/purpose/action/hash,
request ID, an existing variant ID, and structured selection. It cannot carry
arbitrary commands, new Markdown, or a self-reported approval flag. Fresh choices
are checked against current capabilities and precise target facts before the
original command is called. The original admission/approval/reservation transactions
still enforce lifecycle, claim, authority, current revision, and expected target.

Review admission checks the confirmed exact instruction, executor, configuration,
and Task target. Supplemental targets retain all five fields, including explicit
empty values. Preparation resolves the declared scope, and admission rechecks it.
The original Review startup records its actual development baseline and workspace
snapshot at startup; preparation does **not** freeze a Git HEAD. Execution variants
carry full WorkSpec/VerificationSpec. Any explicit instruction replacement must
produce exactly those displayed specs. `START_APPROVED` cannot rewrite an existing
definition or configuration; changing them requires the original successor flow.

Confirmation orchestration records PENDING/UNKNOWN/completed child steps around
canonical commands. The first durable consumption fixes the variant and choices.
Same-request changed payload conflicts; an equal confirmed choice from another
Card returns the original result. Historical receipts precede fresh eligibility,
and do not re-enable grants or revive a terminal Run. Runtime capability changes
cannot turn an equal historical replay into a new command.

Reads can reconcile original canonical receipts without running their commands.
An explicit continuation is offered only when Core proves that all already-started
steps have receipts and only untouched steps remain. An UNKNOWN step without an
original receipt remains blocked; lack of evidence is not permission to redispatch.
Database state cannot roll back external workspace or process effects. Area/Task
creation, approval, reservation, file readiness, and actual dispatch therefore have
separate outcomes. Historical confirmation completion and current associated
business/authorization state are separate readback facts.

A submission rejected before consumption is the one state that returns the Card to
user editing. `retryDisposition = EDIT_SELECTION_AND_RESUBMIT` requires all of
`businessOutcome = REJECTED_NOT_CONSUMED`, `consumptionDisposition = NOT_CONSUMED`,
`consumed = false`, `nextAction = EDIT_SELECTION`, a durable receipt, and that
receipt's request ID and payload digest matching the pending commit exactly, **and** an
intent that can still accept a command: `state = PENDING`, no frozen `commit`, and an
`expiresAt` in the future — an expired or already-committed rejection re-derives to
`REPREPARE_REQUIRED` and unlocks nothing. Any
other combination keeps the Card locked. The Card calls the state that this single
predicate reaches `EDITABLE_RETRY_READY`, and that predicate is the only path into it.
Entering it clears only the pending commit
and its in-flight markers so a changed selection can be submitted, while the last
submission, receipt, rejected request ID and payload digest, failure and event
history remain visible. The retry is a new user command with a fresh `requestId` and a
fresh payload digest, never a replay of the original call, and `redispatchSameIntent`
stays `false`. Resending the original request ID returns only the original rejection
receipt. `OUTCOME_UNKNOWN` maps to `READ_ORIGINAL_RECEIPT` and never unlocks, `STALE`
maps to `REPREPARE_REQUIRED`, and `REQUEST_CONFLICT` unlocks nothing. A Host transport
error, a timeout, a Card refresh, an unchanged Task or the absence of a successor
Review is not evidence of non-consumption.

## Configuration ownership

Codex Review stores its own immutable `runtimeConfig` and confirmation source.
Upper Review carries no Coding conversation or Codex runtime configuration.
The selected Execution stores one normalized runtime configuration through the
original approval/proposal materialization. Implementation and Verification use
that exact snapshot. `verification_continue` accepts an exact existing
VERIFICATION_READY target, with no independent instruction/configuration fields.
Verification-only successors from `REQUEST_REVERIFICATION` inherit the exact
source Execution snapshot. A disabled or missing configuration blocks dispatch;
it never silently falls back to Task, Review, or global defaults.

Legacy empty snapshots remain unknown. Reading history does not populate them or
restart an active Run. A future dispatch of an old reservation with no complete
configuration is blocked. Local/Web/Desktop canonical command entry points remain
available, while their new physical runtime dispatch uses the same frozen-config
checks. Current capability validation also runs before physical backend startup.

## File channels and conversation continuation

Upper Review uses the original read grant, baseline, generation/epoch fencing,
evidence and `review_result_submit` contract. A completed confirmation does not
mean GPT has already read files or submitted a report. Exact authorization state
must be read before continuing in the conversation.

Direct File confirmation authorizes the exact approved definition/workflow without
creating a fake Codex Thread/Turn. `file_implementation_result_submit` records the
actual result and pinned workspace snapshot, closes its grant, and moves the Task
through the formal acceptance boundary. Command verification is `NOT_RUN`; notes
must not claim tests or builds executed through this channel. Closing a grant alone
is not result submission, verification, or acceptance.

`file_implementation_accept` requires a separate explicit user acceptance, exact
result/revision/workflow, unchanged snapshot, and canonical authority. A required
RUN verification with NOT_RUN cannot pass. Only a genuinely approved NONE
verification contract can use this file-only acceptance path. No automatic waiver
or unconfirmed Codex verification is inserted. Accepted work uses the original
Task commit receipt/retry path; a commit error is reported separately.

`file_implementation_result_get` is the pure exact Session/Task/revision/workflow
read of the immutable formal result and its separate acceptance receipt. It remains
available for the owned historical revision after grant closure or current revision
changes. It never replays submission, validates a new grant, or accesses workspace
files. Missing data is a read error. `accepted=true` does not assert that the Task's
separate Git commit succeeded; read the original commit status for that fact.

The Card does not send GPT messages or automatically invoke analysis. Local copy
produces `Continue confirmation <intentId> version <version> contentHash <contentHash>`.
GPT can also use the intent already present in the conversation, obtain its exact
purpose and frozen `contentHash` from the original prepare result, and call
`confirmation_get` using the current real Session. `intentId` and `version` alone
do not address that read; the digest must match the durable intent or Core refuses
with `CONFIRMATION_READ_IDENTITY_MISMATCH` without changing anything.
Do not select the latest Task or infer confirmation from the user's return to chat.
Pending/unknown is not authorization. An explicit `Analysis review result <reviewId>`
continues to mean only exact, read-only Review analysis.

## ChatGPT catalog and transport limits

ChatGPT registers the shared business handlers, removes legacy creation/start
handlers, and installs purpose-specific confirmation. Removed actual handlers are
`project_open`, `development_branch_select`, `feature_area_create`, `task_create`,
`task_review_request`, `task_review_start`, `execution_start`, `direct_file_enable`,
and `project_selection_show`. `decision_respond` is replaced: it permits Review
REVISE/REJECT and Task ACCEPT/REJECT/REQUEST_REVERIFICATION, and rejects new approval,
implementation retry, replacement instructions/specs, and new runtime parameters.
Local registration retains its original canonical handlers.

Prepare/show are model tools; get/options are model and component tools;
`confirmation_commit` is component-visible. No discard command is registered.
`verification_continue` and formal file result/acceptance tools are model tools
with explicit narrow contracts. Runtime approval, cancellation, context recovery,
file operations, pure source reads, and the old read-only Card keep their separate
responsibilities. New independent Codex work does not originate from initialize,
GET, resource load, project selection, or ordinary Task creation. The legacy
ContextProbe and Run starter APIs are not registered in ChatGPT's server.

The installed Go MCP SDK v1.7.0 removes handlers from the same tool map consulted
by `tools/call`; remembered old names cannot bypass removal. `ui.visibility` is
host exposure metadata, **not** authenticated evidence of a user click. This
implementation does not invent host credentials, caller-origin proof, or a hidden
approved flag. Original authenticated caller/Session/Project ownership remains
mandatory. Host enforcement of component visibility requires separate acceptance.

ChatGPT `bridge_diagnostics.businessCatalogSHA256` fingerprints the filtered,
sorted business catalog including confirmation and continuation tools. It excludes
presentation-only tools. Local retains the shared canonical business fingerprint.
The unified Review schema fingerprint is separate from the business-name catalog.

The component follows the [official ChatGPT UI integration contract](https://developers.openai.com/plugins/build/chatgpt-ui)
and [MCP server integration guidance](https://developers.openai.com/plugins/build/mcp-server).
SDK/source inspection and syntax checking do not establish host behavior.

## Safety and acceptance limits

Markdown is rendered with DOM text and a restricted subset supporting headings,
lists, tables, code and ordinary HTTP(S) links. Raw HTML and dangerous URL schemes
are not executed; external images/content are not fetched. Diagnostics are hidden
behind Settings, display in a modal, and can be copied without full prompts,
Session credentials, or raw server responses. The exact component script hash is
available for deployment identity checks.

Same Session/Project/intent/version, content hash, generation, sequence and
single-flight checks prevent stale responses from replacing another target.
Clipboard success is reported only after success; failure shows selectable text.
No card state or widget snapshot is business authority.

Migration V56 only adds confirmation records/receipt linkage, immutable Review
runtime fields, and formal Direct File result/acceptance records and guards. Source
changes do not apply it. Validate on an explicitly authorized host later: first-use
Project/branch selection and readback; uncreated Task before confirmation; selected
Codex runtime at actual Review/Execution; same Execution Verification inheritance;
supplemental/retry identity; file grant handoff and formal result; duplicate/lost
responses; old cards; disabled configurations; and cross-object rejection. No
automatic GPT message should be needed. Existing routing and successor runtime
acceptance remain separate from static confirmation review.


## Exact request outcome and typed errors

Confirmation tools use one closed `structuredContent` envelope: an authorized
`intent`, an `options` result, and/or a typed `failure`. Failure-only responses omit
inaccessible or zero-value intents. `isError=true` remains a real call failure;
clients decode and validate the structured facts before handling that flag. Caller
middleware rejection uses the same failure-only schema without relaxing scope.

Three layers are independent:

| Layer | Meaning |
| --- | --- |
| `failure` / `intent.submission` | This call's code, source, stage and the exact submitted request's outcome. |
| `intent.state` | Original confirmation lifecycle; `PENDING` alone proves no request outcome. |
| `steps`, business IDs and current access | Original canonical command receipts, reservations and current permissions. Confirmation completion is not Task success or GPT activity. |

`submission.request` binds Session, intent, purpose, version, action, contentHash,
requestId and original structured selection. Its submitted-payload fingerprint is
separate from the normalized committed choice. `recorded=false` means UNKNOWN;
no row, timeout or an unchanged PENDING intent is not non-consumption proof.
`CONSUMED` proves only unique selection consumption. `REJECTED_NOT_CONSUMED`
proves this exact request is terminally rejected before business steps began.

V58 appends nullable original-input/outcome columns to the existing confirmation
request storage. `RejectCommit` and `BeginCommit` use the same writer transaction,
original Session/request unique key and immutable request rows. A same-payload
replay returns the stored rejection or consumption; a changed payload conflicts.
A rejected request cannot later become consumption after capabilities recover.
A different request already consuming the intent prevents an edit-safe rejection.
No durable rejection is returned before transaction commit; new consumption also
publishes its accepted snapshot only after commit. Uncertain storage commits stay
unknown until read back. No second dispatch or receipt registry is introduced.

`confirmation_get` accepts optional `requestId` to read exactly one submission,
even when `intent.commit` is absent. The read validates caller, Session, intent,
purpose/version and request ownership. It uses a read-only transaction, never
writes an outcome or runs a worker. Immutable request evidence remains available
when child receipt recovery fails; that read failure is still returned explicitly.

The Card correlates the original request hash and full identity before releasing
pending protection. A recorded rejection only allows a fresh user choice when the
latest intent is PENDING, unconsumed, unexpired and `nextAction=EDIT_SELECTION`.
It preserves valid drafts, rereads options and never submits automatically. A new
explicit attempt gets a new request ID. Replacement content requires a new intent.
Widget state saves correlation only. Unknown, missing records and malformed
responses retain original-request readback protection across remounts. Old
intent/version/request/generation responses cannot unlock a new attempt.
Already verified consumption survives local rendering failure in a simple failure
view; it cannot be converted back into a submit form.

| Typed error category | Source / stage | Legal routing |
| --- | --- | --- |
| Session scope, missing caller or Session | CORE or CONFIRMATION / INPUT_IDENTITY | ACCESS_BLOCKED; no inaccessible intent. |
| Model/reasoning/Fast selection unsupported | RUNTIME / CONFIRMATION_PREFLIGHT | EDIT_SELECTION only with durable unconsumed rejection and current eligible intent. |
| Frozen mismatch, unknown frozen config, missing backend | RUNTIME / CONFIRMATION_PREFLIGHT | PREPARE_NEW_VERSION or READ_CURRENT_TARGET; never edit a frozen snapshot. |
| Original domain/lifecycle/file error | DOMAIN or CORE / actual validation or business stage | Preserve typed code/cause; READ_CURRENT_TARGET or PREPARE_NEW_VERSION. |
| Payload conflict or historical replay evidence missing | STORAGE / current operation stage | READ_ORIGINAL_RECEIPT; no declaration that the original request failed to execute. |
| Canonical command / reserved dispatch error | Original source / BUSINESS_STEP or DISPATCH | Keep CONSUMED, prior completed steps and reservation; unknown effects stay unknown. |
| Receipt read error | Original source / RECEIPT_READBACK | READ_ORIGINAL_RECEIPT with available immutable facts. |
| Host, decode or local render failure | HOST or CARD / separate local stage | Preserve numeric RPC codes separately; no invented Core outcome. |

Choice failures retain the first priority reason plus a bounded typed set. Error
routing does not parse message substrings. Only an explicit pre-command marker
proves `NOT_STARTED`; target IDs or broad error classes do not prove side effects.
Steps marked `UNKNOWN` before canonical invocation retain original child IDs.
Recorded binding with preparation failure keeps the Project but does not claim
preparation completed. `canResume` is still derived solely from original receipts,
remaining untouched steps and worker fencing, never from a generic retry flag.

## Prepare replay and executor applicability

Prepare first authorizes the existing Session, reads its original request and
compares `submitted_sha256`; only a genuinely absent request enters current
validation and content derivation. The write transaction repeats this lookup before
source-intent replacement. Raw structured hashing preserves field presence, null,
empty values and array order; frozen `contentHash` and legacy `prepare_hash` retain
their original meanings. Replay does not derive today's scope, replace another
intent or reopen old authorization. Historical rows without provable original
input return `PREPARE_REPLAY_EVIDENCE_UNAVAILABLE`; reading the known exact intent
remains available. No historical hash is fabricated or rewritten. Legacy commit
receipts can still prove consumption from their stored normalized payload, but a
raw-input replay that cannot match that evidence remains conservatively blocked.

Create-only variants cannot carry unrequested executor/runtime content. Options
and Task argument preparation share a pure prerequisite reader for exact
Session/Project/Repository, modules and development baseline. Only an actual
Coding Review additionally requires an enabled backend. Empty Task Codex config
means not applicable; V59 removes only the old insertion predicate requiring a
nonempty config, while retaining the nullable foreign key and all other guards.
Explicit configs remain validated. Upper Review/create-only Tasks gain no implied
future Coding authority: Coding Execution options read that exact Task backend,
and canonical dispatch still checks it. Direct File RUN verification, Upper
read grants, file writes, result submission and user acceptance remain distinct.

ChatGPT's actual first-use route is `session_initialize` with requestId, then
`project_setup_prepare`, `confirmation_show`, user confirmation and exact
`confirmation_get`. Engineering uses the appropriate prepare tool after scoped
reads. Local/Desktop retain canonical direct controls. Implementation failure
with no Verification Run points to the exact Implementation result and current
successor eligibility. Verification language is selected only from the exact
Execution's recorded Verification Run; unavailable facts use neutral readback text.

## Separate manual acceptance

Source review, gofmt, JavaScript syntax and diff checks do not execute migration,
Card recovery, dispatch or races. When deliberately validating a later loaded
version in an authorized environment, retain the original Session and exact intent:

1. Make a controlled new confirmation whose configuration is explicitly rejected
   before consumption. Verify its request is durably REJECTED_NOT_CONSUMED while
   the eligible intent remains PENDING; correct a valid draft and explicitly click
   once for a new request. No automatic model/Fast substitution should occur.
2. For response loss, remount/read the original request ID. A saved rejection can
   release protection; missing records remain UNKNOWN. A consumed result retains
   its original steps and IDs. Do not replace unknown requests to force progress.
3. Read/replay the identical prepare after lifecycle advancement: it must return
   the original intent; changed payload conflicts. Historical missing hashes must
   report the stated evidence limit. Do not redo source replacement.
4. In a separately authorized file-mode scenario without a Codex backend, confirm
   creation/Upper prerequisites and exact grant readiness; retain RUN verification
   blocking. Verify actual Coding still requires its real backend.
5. Inspect explicit original continuation, configuration inheritance, failed
   Implementation versus actual Verification, and original child receipt/fencing
   behavior only under the corresponding separate user authorization. Do not
   retry the previously failed SCOPE_CONFLICT Execution as part of this repair.

Pending: CONFIRMATION_CARD_HOST_ACCEPTANCE_PENDING;
CARD_CONFIRMATION_TO_CODEX_DISPATCH_ACCEPTANCE_PENDING;
FILE_MODE_CONFIRMATION_HANDOFF_ACCEPTANCE_PENDING;
EXECUTION_VERIFICATION_CONFIG_ACCEPTANCE_PENDING;
IMPLEMENTATION_SUCCESSOR_RUNTIME_ACCEPTANCE_PENDING.


## Confirmation information hierarchy (V4 main card, V5 reader)

The dedicated Card is a decision surface. Its normal MAIN view shows DEV BRIDGE,
exact available Project/current registered development-branch names and status,
then a user-language action and accurate Task/FeatureArea title. The selected
variant's optional short engineering summary and explicit critical boundaries
precede runtime selection, one complete-plan entry, and the actual primary action.
No protocol IDs, hashes, internal status enums or runtime inventory revisions fill
the normal header. Missing names have neutral placeholders, never guessed IDs or
latest objects. Important errors and unavailable reasons remain beside controls.

| Purpose/action | Main object and decision |
| --- | --- |
| PROJECT_SETUP | Choose the Project, Repository and existing branch explicitly; confirmation binds/selects them under the original protocol. |
| FEATURE_AREA | Exact prepared area title; create only or create its explicit initial Task and prepared Review. |
| TASK | Exact prepared Task title in its area; create only or also start the prepared first Review. |
| REVIEW / REQUEST_SUPPLEMENTAL_REVIEW | Exact authorized Task title; start the specified Review or the canonical same-Task supplemental sequence. |
| EXECUTION | Exact authorized Task title; approve/start, start the approved candidate or create the authorized implementation successor. File permission and Codex dispatch have distinct labels. |

Action/consequence text comes only from purpose/action/selected executor, never
keywords in summary. RUN is the existing verification contract, not evidence that
tests will execute. No-test/no-build/no-commit boundaries require explicit prepared
content. Confirmation consumption, completed steps, actual runtime work and user
acceptance remain separate facts in the compact post-confirmation summary.

### Optional short author content

`variant.displaySummary` is the only short-display field family. When present it
has required `summary` (1–160 Unicode code points) and optional `keyConstraints`
(up to 3 strings, each 1–60 code points). JSON Schema and Go utf8.RuneCountInString
use the same character unit; Core checks size/shape only. Missing is valid, not an
approval blocker. Labels name a proposal difference instead of repeating executor
prefixes. GPT supplies these fields, never an in-browser/Core summarizer.

The new optional JSON field follows the unchanged raw submitted-input hash and
frozen contentHash serialization. Validation occurs only after exact prepare
replay lookup. Absent pointers omit the field, preserving legacy serialization;
no historical content, summary, version, hash or request row is backfilled. Old
long summaries remain complete in the plan reader; MAIN omits an unavailable short summary
instead of showing the first 320 characters. This does not require preparing a
replacement for an existing intent.

### Optional name projection

The closed result envelope optionally adds `display` alongside an authorized
`intent`, never in `intent.content`. It binds Session/intent/purpose/version and
immutable intent Project identity, plus exact target references and names.
`objectNameSource` distinguishes PREPARED_CONTENT, CURRENT_AUTHORIZED_OBJECT and
UNAVAILABLE; `status` describes only metadata completeness.

Prepared Task/area names use those exact definitions. Existing Review/Execution
Task names use the exact intent Task and matching Project/FeatureArea/Repository.
Project lookup follows GetOwned Session admission; area and repository reads
verify exact ownership. Repository may be derived from the exact Task when absent
from the Execution body. `developmentcycle.ReadSelected` reads only the registered
cycle for that Project/Repository, with no project list, Git discovery or workspace
preparation. Its branch is current registered display metadata, not proof of a live
Git ref/HEAD and not a frozen approved baseline. Rename metadata never changes
operation targets. Missing/unreadable optional metadata degrades to placeholders;
core identity and content errors retain their original blocking behavior.

A historical PROJECT_SETUP intent can keep empty immutable projectId after binding.
Post-confirmation names are shown only when its exact Commit selection matches the
current owned Session; selection draft names before confirmation come from the
existing authorized options. This adds no implicit Project/Repository/branch choice.

### Full content, technical facts and view state

One MAIN link opens a stable, in-component native dialog outside the root card.
The V5 reader separates proposal explanation, exact prepared instructions and
structured definitions by purpose. The V4 main-card appearance is retained.

| Exact prepared purpose | Reader tabs | First open |
| --- | --- | --- |
| EXECUTION | Overview / Implementation instruction / Verification instruction / Configuration and constraints | Implementation instruction, including an explicit source/missing-content state when needed |
| REVIEW | Overview / Review instruction / Configuration and scope | Review instruction |
| TASK or FEATURE_AREA, create only | Overview / Definition and scope | Overview |
| TASK or FEATURE_AREA, explicitly starts Review with a complete prepared Review instruction | Overview / Review instruction / Definition and scope | Review instruction |
| PROJECT_SETUP | Work-environment explanation / Selection details | Work-environment explanation |

These tabs describe this prepared operation, never a future Implementation inferred
from summary text. PROJECT_SETUP mirrors the original main-card selection read-only;
it does not bind or select within the reader. Opening tabs, viewing raw text and
copying are not prerequisites for confirmation.

| Content | Destination and meaning |
| --- | --- |
| Complete variant summary, actual prepared goal/change, supplemental purpose | Overview, explicitly labeled explanation rather than instruction text |
| Prepared Review / Implementation / Verification raw strings | Their instruction page, with source and state in a separate fixed toolbar |
| WorkSpec / VerificationSpec modes, configuration, constraints, scope, numbered checks, criteria, waiver and source | Read-only structured field groups, preserving false, zero, empty arrays and explicit null distinctly from missing data |
| Other authorized engineering definition fields | Other definition fields, retaining exact keys/types; never injected into an instruction |
| Exact object IDs, hashes, requests, receipts and sensitive diagnostics | Existing technical details, not ordinary configuration groups |

The classifications do not prove authorship. A stored proposal is not automatically
Bridge-authored or GPT-authored, and a prepared instruction is not a captured final
Run prompt. No operation_get or Run-input read is introduced. Recommended config,
current user selection, consumed selection and frozen Execution config are labeled
separately; no proposal field proves actual Run configuration. Verification retains
the original Execution configuration inheritance and gains no independent selector.

Instruction pages render only the selected raw string through the original restricted
Markdown renderer or textContent. The toolbar's content type, source path, view mode
and copy controls never enter the document or copied value. Existing Markdown
headings remain with distinct h1/h2/h3 typography. No prefix, trim, newline rewriting,
JSON serialization or semantic deduplication changes the raw instruction. External
images, executable HTML, dangerous URLs and remote rendering remain unsupported.

The original instruction-source contract is represented by one pure local resolver,
not an authorization service. Missing containers, missing fields, explicit empty
values and invalid types remain distinguishable. An unsupported or ambiguous source
reports that current data cannot establish a unique applicable instruction; any
available original string can then be read/copied only as that explicitly selected
field. Different sources are not merged or selected by length, trim or similarity.
Verification with no independent instruction retains its explicit empty state and
points to the unchanged structured NONE/RUN definition; checks are not synthesized
into a prompt. Source differences alone never change Core eligibility or submission.

The canonical sources are defined by `task.ApplyExecutionInstructions` and
confirmation validation, not by display truthiness:

| Prepared instruction | Canonical selection | Source problems |
| --- | --- | --- |
| Review | variant.reviewInstruction, assigned unchanged to the prepared Review | Missing, wrong type, blank, oversized or NUL-containing text cannot fall back to summary |
| Implementation without executionInstructions | variant.workSpec.codexInstruction | Missing/invalid spec text remains unresolved |
| Implementation with executionInstructions | executionInstructions.implementation must match the materialized workSpec.codexInstruction exactly in prepared content | Different strings are a conflict, not an override hierarchy; identical strings render once with both paths explained |
| Verification RUN | verificationSpec.codexInstruction; when executionInstructions is present, its verification string is required and must match | No checks-to-prompt synthesis or fallback over malformed explicit content |
| Verification NONE | No independent verification instruction; canonical empty/omitted instruction and absent optional verification pointer | A nonempty spec instruction or a present independent verification string conflicts with NONE; retain actual shapes and waiver definition |

`ApplyExecutionInstructions` leaves specs unchanged for a nil instruction object;
otherwise it validates nonblank bounded input and assigns the original bytes.
Confirmation validates the specs first and then requires the materialized specs
to equal the displayed variant. START_APPROVED additionally checks the frozen
candidate; RETRY_IMPLEMENTATION checks materialization from the original Task.
Thus a reader cannot reinterpret different prepared strings as a valid override.
Go pointer decoding can conflate null and missing, while MCP prepare schemas do
not accept those explicit null inputs and normal output omits nil pointers. The
reader preserves such anomalous transport shapes as source uncertainty instead
of silently supplying a fallback. This remains a presentation finding only.

Clipboard writes use the captured original raw string. Success appears only after
writeText resolves and the same content key, open generation, tab, source, copy
identity and connected view still match. Failure exposes a trusted read-only manual
copy area without claiming automatic success or calling a remote tool. Closing and
reopening the same intent invalidates old callbacks. Trusted tab/toolbar/manual-copy
controls are separate from the restricted document; checkedRender still rejects
interactive or executable nodes from proposal and feedback renderers.

Settings opens hidden technical details/diagnostics with copy support: full IDs,
version/content hash, exact submission and child requests, accepted configuration,
capability source/revision, failure code/source/stage, step state/effect, current
Upper/Direct File access facts, resource/component version and script SHA-256.
No raw response, credentials, caller scope or stack dumps are collected. The
additional layout diagnostic reads only local shell dimensions, content width and
padding, first-content inset relative to the shell, and whether the reader is open.
It does not inspect parent DOM, URLs, full CSS, request bodies or credentials.
These computed metrics are observations, not proof of visual parity.

The V4 presentation replaces the earlier CSS and main-card renderers completely:
one stylesheet and one actual confirmation-main script remain. The stable
`#db-confirmation-shell` owns the border, 18px radius and shadow, with a maximum
820px width and 16px surrounding body padding. Its child `#root.dbc-content` owns
26px/28px content padding, reduced to approximately 18px on small screens.
`root.replaceChildren` updates only this inner content; shell and dialogs survive.

The actual DOM uses dbc-header, dbc-intro, dbc-callout, dbc-config, dbc-feedback
and dbc-footer. Branding is 17px, environment text 13px and the main title 21px
(19px on small screens). Local fixed SVGs provide target/shield operation rows,
code configuration headings, document/refresh/play actions and a settings button.
The shield boundary row exists only for actual prepared keyConstraints; missing
short content never gains inferred engineering text. Configuration fields share
an inset, with model/reasoning in two desktop columns and one small-screen column.

The footer groups plan/refresh controls on the left and the original primary action
on the right, wrapping on small screens. Buttons are approximately 38px with 44px
touch targets. All original visibility, disabled conditions, capability checks and
handlers remain authoritative. Host theme variables and system light/dark, focus
and error styles remain local. No fixed outer height or overflow clipping hides
errors or forces the card to fit one screen. Settings now uses a real local gear
SVG button opening the original technical dialog, without a details triangle.

Opening and closing the reader are local presentation changes. They preserve
variant, Project/Repository/branch, model/effort/Fast drafts, pending request/hash,
verified outcome and failures; they do not seed recommendations, allocate a
request or call a business tool. The reader has no approval action; reading or
scrolling the full plan is not a prerequisite. The proposal document contains no refresh, navigation or business controls;
local tabs and instruction controls occupy separate trusted regions. Exact state feedback and outcome facts are separate from
its immutable content.

The dialog has stationary title, tab and instruction-tool rows and a close area,
with one scrollable content body. Its
maximum 720px by 560px geometry is bounded by the intersection of the card's
border box and this iframe's local viewport, with an inner margin. Short geometry
removes redundant subtitle/footer space; the top close stays available. Insufficient
space or missing native showModal support produces an isolated reader diagnostic,
never a long-page fallback. Code and tables can scroll horizontally within the
body. No parent/top DOM, host modal, model message or remote asset is needed.

Both reader and sizer use the stable outer shell as their anchor. The height
notification measures only its normal-flow border-box bottom plus
local document offset and body bottom padding. A card-only ResizeObserver and
requestAnimationFrame coalesce changes, integer heights are deduplicated, and the
original ready handshake gates the existing size-changed notification. Neither
full document/body scrollHeight nor a viewport-based minimum enters that value.
Actual main-card errors/configuration still resize naturally; opening or scrolling
full content cannot add its length to the host card height.

Reader view state uses a bounded cache of at most 16 exact contents keyed by
Session, Project, intent ID, version, contentHash and variant ID. Within each
content, tab, reading/raw mode and exact source identity separate scroll positions.
Only finite existing tabs/modes/sources are retained, with no persistent full-text
copy and no new widget authorization state. Same-content refresh updates state,
read-only configuration and source qualification while retaining unchanged prompt
DOM and position. Different or invalid identity cannot borrow cached instruction
content. The reader and technical dialog are mutually exclusive. Top close, bottom close and
Esc share cleanup; focus returns only to the same-identity current entry, including
after root rerender. Local html/body locks restore exactly their own styles and
scroll position on close, failure and disposal. Dialog-local wheel/touch fallback
respects inner scrolling, zoom, text selection and links; no parent scrolling is
intercepted. Disposal releases local events, observers and pending animation frames.

Reader failures have their own diagnostics and do not set business error/fatal,
release pending or change consumption. A main rendering failure closes the reader
and retains the original verified result and read-only recovery path. UNKNOWN,
expiry, permission and persistent rejection constraints remain visible in both
surfaces. The existing protocol remains the authority for all business state.

One prepared variant uses a static executor label and its exact local variant ID;
it still needs an explicit primary click and current capability. Several prepared
variants stay visible even if only one is available; the Card never switches away
from a user's unavailable choice. File modes hide Codex configuration, while real
locked Execution config remains read-only and Verification inherits it.

V5 resource is `ui://devbridge/confirmation/v5`, component
`PURPOSE_CONFIRMATION_V5`. Tool/resource/result metadata and ui/initialize use the
same identity. The emitted and locally calculated main-script hashes cover the
complete confirmation-main script, including reader and sizing controllers.
Version changes retain exact V1/V2/V3/V4 pending widget correlation and UNKNOWN
protection; component version is not authorization or proof of a host reload.
This confirmation resource requests `prefersBorder=false` while retaining its own
complete shell. The preference does not guarantee that a host removes its outer
tool frame. Existing go:embed delivery and CSP remain unchanged; no additional
stylesheet/script loader, remote icon, font or CDN is introduced.

### Manual appearance and host acceptance

No browser or Card logic was executed for this source change. Syntax checks do
not prove height, layout, focus, scrolling or host support. Keep
CONFIRMATION_PLAN_READER_CONTENT_SEPARATION_HOST_ACCEPTANCE_PENDING,
CONFIRMATION_CARD_VISUAL_PARITY_HOST_ACCEPTANCE_PENDING,
CONFIRMATION_CARD_SHELL_MODAL_HOST_ACCEPTANCE_PENDING,
CONFIRMATION_CARD_INFORMATION_HIERARCHY_HOST_ACCEPTANCE_PENDING and all existing
confirmation/dispatch/file/configuration/successor runtime acceptance labels.

After independently loading the new version, the user may inspect the existing
`confirm_8ea030d758b6a728b2fa07455d976d8c`, version 1, without confirmation or
replacement: compare the loaded card and reference image at the same approximately
800px width, inspecting actual margins, icon elements, column alignment, colors,
configuration inset and buttons. A border, working dialog or syntax check alone
does not establish visual equivalence. Verify its title/config/main action are
immediately locatable,
small screens wrap and full checks remain continuous and complete. Scroll to
both ends of the reader, inspect the reachable close controls and unchanged chat
position, then close with the top/bottom control and Esc on separate openings.
Check that model/effort/Fast selection and same-content reading position remain.
Inspect narrow/short geometry, long code and errors, modal exclusion and the
actual mainScriptSHA256 before attributing behavior to this disk version. Verify
important errors, locked settings and UNKNOWN remain visible and restrictive. Do not click its primary action,
reprepare, mutate records or start the successor merely to inspect the UI.

For the content-separation reader, the user can retain the same original intent:
first open its Implementation instruction, compare Reading and Raw, copy and compare
the exact text, inspect the separate Overview and complete Configuration/checks,
switch tabs and close/reopen to verify local state retention. Inspect source
labels and manual-copy fallback without executing any business action. Long content
must not grow the ChatGPT conversation; toolbar and top close remain reachable.
No reprepare or actual implementation click is part of this read-only acceptance.

## Backend and runtime authority

For a first Coding Agent implementation, backend selection is an explicit part
of the confirmation selection. The client may submit only `configId` and the
Core-issued `configFingerprint`; Core re-reads the scoped inventory and
authoritatively supplies the name and source. Runtime selection (model,
reasoning effort and Fast Mode) is validated separately against the current
allowlist. A selected backend is not a thread or connectivity check.

After approval, the exact backend selection and runtime configuration are
frozen on the resulting Execution. Implementation and Verification read that
Execution snapshot; they do not reselect the first enabled backend or inherit a
mutable Task preference. Options and cards are pure reads and never bind a
backend or create an Execution.

The client backend object and the Core-frozen backend fact are distinct types.
The original submission and its hash retain only `configId` and
`configFingerprint`; the consumed Commit and Execution may additionally retain
the Core-resolved name and provenance source. Same-request replay remains bound
to the exact original payload. A different request that expresses the same
authorized choice compares backend identity rather than display metadata.

WebUI action drafts likewise retain a versioned logical backend identity
(`configId` plus `configFingerprint`), never the `<option>` value used for an
unavailable placeholder. Repeated render/save cycles preserve that pair. An
unversioned historical draft is restored only when a current authoritative
candidate proves the exact pair; otherwise it remains visibly unavailable and
requires an explicit re-selection. No `draft:` prefix or colon-delimited value
is parsed, so a legitimate configuration ID containing those characters keeps
its exact identity.

The local draft distinguishes an explicit empty selection, a known-but-currently
unavailable identity, and an unrecoverable historical identity. The
`backendIdentityUnavailable` marker is evaluated before empty ID/fingerprint
handling and survives every automatic save/render cycle. Only a trusted user
change to an exact current candidate, or the dedicated local clear option,
removes that marker. Clearing remains an invalid required backend selection and
keeps submission disabled; programmatic value changes, options refreshes and
rerenders cannot impersonate either user action.

Continuation Executions currently inherit their predecessor backend because no
general cross-backend Thread compatibility contract exists. REQUEST_CHANGES and
RETRY_IMPLEMENTATION therefore expose that backend as locked, reject a different
identity in the canonical writer, and still allow a new validated model, effort,
Fast Mode and service tier. Verification inherits the complete Execution
snapshot.

The confirmation options and Card may also expose pure-read backend continuity
evidence. Its state (THREADLESS, BOUND_AVAILABLE, BOUND_UNAVAILABLE,
HISTORICAL_UNKNOWN, READ_UNAVAILABLE, or CONFLICT) and reason describe the
conversation binding only; they never grant execution authority. A locked
backend is displayed with its durable identity and is not copied into
Submission.Request. The client submits only configId and configFingerprint;
Core supplies display name and provenance after authoritative validation.

The durable continuity source is written only when the first Thread is
successfully bound. One SQLite writer transaction records the canonical Thread,
initial model, full non-secret frozen backend selection and their exact source
Run/Thread while also attaching that Thread to the same source Run. It reuses the
Run's owner, current-epoch claim, PREPARING-stage, immutable-Thread and timestamp
CAS rules; any local step failure rolls back the entire binding. The external
Codex Thread creation necessarily precedes this transaction and is not claimed to
be atomic with SQLite. A logical threadless conversation, options read, approval,
reservation, resume, or later Turn does not create or replace this binding. An
existing Thread without that evidence remains historical-unknown; today's Task
config or first enabled backend must not fill the gap.

After a successful first binding the Coordinator reads back the exact persisted
source Run and uses that snapshot before changing it to RUNNING. It does not call
the ordinary `Runs.AttachThread` as a second required write on this path. A later
Run that resumes an existing Thread still uses that ordinary association and
cannot rewrite the conversation's initial model/backend source. A failed or
commit-unknown first binding remains an unknown external-start boundary and never
authorizes another Thread creation merely because the local result was not seen.

Fresh Coding admission is checked at three boundaries. The Review or approval
writer compares its frozen backend with the current FeatureArea conversation;
the common Run-claim transaction repeats that comparison for Review,
Implementation and Verification; `BeginRun` and the Coordinator repeat it before
passing the same freshly validated config object to the client factory. Replay
reads a committed receipt before these fresh checks. Thus a candidate prepared
while threadless cannot bypass a Thread established by another candidate, while
same-backend work may choose a different model without rotating the Thread.

## Feature Area conversation model default

A FeatureArea owns the canonical current AgentConversation and its Codex Thread.
Tasks, Reviews, Executions and Runs claim that shared context through the existing
FeatureArea serialization contract; changing models does not reset, rotate,
fork, or replace the conversation.

The conversation's `initialModel` is recorded only after creation of its first
canonical Codex Thread succeeds and the exact owning Run binds that Thread. It is
the requested model from that Run's already-frozen runtime configuration, not a
claim about provider-side effective behavior. The same transaction records the
source Run and Thread. Logical conversation allocation, prepare/options reads,
approval, GPT file review, resume and later Turns do not write this fact. Once
known it is immutable; an old claim, old epoch, rotated conversation, different
Thread, model, or source cannot overwrite it. An existing historical Thread
without reliable creation provenance remains `HISTORICAL_UNKNOWN`.

For a new editable Coding choice the model default priority is: an already
consumed/frozen configuration; the user's current or restored draft; the current
conversation's proven initial model; a prepared recommendation only while the
conversation is threadless; otherwise no default. This rule applies only to the
model. Effort and Fast Mode retain their per-choice recommendation, draft,
capability and freeze rules. A disabled initial model remains visible as the
source but does not lock the FeatureArea: the user may select another currently
valid model.

An explicit different model is frozen on that Review or Execution and is sent on
the next Turn of the same reusable Thread. Verification uses the parent
Execution's frozen runtime. A later new editable Coding choice again defaults to
the conversation initial model; later choices never rewrite it.

Runtime semantic questions now use an immutable current-user decision before any
backend response. Reads, refresh and notifications never answer a question.
See the [Codex request / user decision contract](codex-request-user-decision.md) for exact identity,
response states, canonical submission and isolated validation.

## Repository unlink confirmation

`REPOSITORY_UNLINK` is a Core-authored, purpose-scoped confirmation. Its identity
contains the stable request, exact Project/Repository, registration generation
and Project topology revision. Prepare is read-only and may display authoritative
blockers. Commit repeats every check in the SQLite writer transaction and exposes
no force, outcome or blocker-override field. Consequences state that local files
and managed worktrees remain, history remains readable, future new work is
disabled, and v1 provides neither relink nor cross-Project transfer. See
[the safe unlink closure](safe-repository-unlink-closure.md).


## CODEX_REQUEST 跨 Core 决定恢复

恢复不是新用户确认。原 UserDecision 的文本、选项、command requestId 与 payload hash 不变，原历史 identity 保留；新连接只能在 durable reattachment receipt 与实际同一 pending request replay 均成立后使用该决定。接口 exact identity 增加 appServerInstanceId/appServerInstanceEpoch，与 runtimeEpoch、Run/Thread/Turn/runtimeRequestId 一同校验。没有用户答案不得代答；UNKNOWN 不盲重发，refresh 不 dispatch。详见 [continuity 记录](codex-request-cross-core-continuity.md)。

## Review Decision authority, rejection receipt and the replacement intent

One reducer owns the Review-result resolution Decision. `reviewDecisionSnapshotTx`
reads the Review, its Task, `Task.currentReviewId`, phase and status, the retained
result materialization, the durable Decision row, its resolution receipt, any
successor Review and the repair proof, and derives `decisionStatus`,
`decisionVersion`, `requiredAction`, `availableActions`, `reasonCode` and
`retryDisposition`. The workspace and workflow projections, `TaskView`, the
FeatureArea product projection, prepare preflight, `confirmation_options_get` and
the commit fence all read that same reducer, and the commit writer re-reads and
recomputes it inside its own SQLite transaction. A read may never report
`PENDING` while the writer reports `NOT_PENDING` unless a concurrent version
change explains it. Reads never repair a missing Decision: the only repair runs
inside the commit transaction.

`decisionId` and `decisionVersion` are different facts. The version token is a
canonical hash of the durable Decision and ownership facts; it is stable for
identical state and changes when the status or an owner fact changes. Prepare
freezes it as `review.preparedDecisionVersion`, which is part of the intent's
`contentHash`, and the fence compares it against the token recomputed under the
writer lock. The token is never the Decision identity, the Card generation or a
"non-empty" flag. Terminal refusals keep distinct codes: `DECISION_NOT_PENDING`,
`DECISION_VERSION_CHANGED`, `DECISION_SUPERSEDED` and `DECISION_OWNER_CHANGED`.

Options read the intent's latest durable submission before deciding anything, so
a recorded rejection is visible on every surface at once: `available=false`,
`attemptState=TERMINAL_REJECTED`, `receiptState=REJECTED_NOT_CONSUMED`,
`retryDisposition=REPREPARE_REQUIRED`, `nextAction=PREPARE_NEW_VERSION`,
`reprepareAllowed=true`, and every variant unavailable with
`reasonCode=INTENT_REPREPARE_REQUIRED`. `NOT_ATTEMPTED` and `NOT_OBSERVED` are
only ever reported when no submission exists. The rejected Card shows
`staleIntent=true`, `editableRetry=false` and the copy
本次确认已明确拒绝且未产生业务副作用。需要根据当前 Decision 状态重新准备新的确认。
It offers only `PREPARE_NEW_VERSION`, `ABANDON_TASK`, `OPEN_PROJECT_REPOSITORIES`
and `REFRESH`; it never offers to resubmit, edit-and-resubmit the same intent, or
report `OUTCOME_UNKNOWN`/`READ_ORIGINAL_RECEIPT` for a definite rejection.

`confirmation_reprepare` is the formal `PREPARE_NEW_VERSION` action and the only
replacement constructor: it is an adapter over the existing prepare builder, so no
second intent constructor exists. The client supplies only `aiSessionId`, the
source intent's exact `intentId`, `version` and `contentHash`, a fresh `requestId`
and optional `reviewFeedback`. Core proves the source's recorded
`REJECTED_NOT_CONSUMED`, `consumed=false`, `REPREPARE_REQUIRED` and
`reprepareAllowed=true` receipt, reads the current Decision snapshot, and refreezes
the Decision version token, the Project topology revision, the development context
hash and the current formal Project members. The result is a brand-new `intentId`
at `version: 1` carrying `sourceIntentId` lineage, and the rejected intent becomes
`SUPERSEDED` while its rejection receipt stays readable; nothing about a successor
Review, Run or start authority is created until the replacement itself is
confirmed. `PREPARE_NEW_VERSION` is therefore a product action name and never an
in-place upgrade of the rejected intent to version 2. Replaying the exact
`requestId` returns the same replacement, the same `requestId` with a changed
payload returns `REQUEST_CONFLICT`, and a second replacement attempt on the now
superseded source is refused, so a lost response cannot fork duplicates. When the
Decision is already terminal, reprepare refuses with its typed code and creates no
intent, and the workflow stops offering the re-review action.

`confirmation_commit` is the one public submission entry and is model-discoverable
exactly as the Card calls it; both reach the same Core service and the same closed
DTO, which accepts no outcome, consumed flag, Decision status or version, successor
identity, `retryDisposition`, `reprepareAllowed`, `force`, `assumeNotConsumed` or
`retryAnyway`. Text continuation is routed through that same pair:
`Continue confirmation <intentId> version <version> contentHash <contentHash>` does
an exact `confirmation_get` first, then commits only a `PENDING` intent with no
recorded submission and available options; a `CONSUMED` intent returns its actual
business objects; a recorded `REPREPARE_REQUIRED` rejection returns the typed
rejection and goes to `confirmation_reprepare` without any commit call; an unknown
or unrecorded result is a receipt read, never a resend. A Host transport error is
followed by the same exact read-back, and a typed rejection is never reported as
`recorded=false`, `OUTCOME_UNKNOWN` or `READ_ORIGINAL_RECEIPT`. A surface with no
commit tool reports `CONFIRMATION_COMMIT_TOOL_UNAVAILABLE` and states no outcome.

Outer copy is fixed: the rejection is reported as
本次重新审查确认未被消费。原因：冻结的审查处置状态已发生变化。需要重新准备新的确认。
the prepared replacement as 新的重新审查确认已准备, and only a committed
replacement with durable successor identities as successor Review 已创建并开始准备.
None of them may be described as 当前结果未知 or 无法确认是否已提交.
