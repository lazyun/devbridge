# Unified Review executors and upper LLM result closure

Canonical contract introduced by migration 48. Task owns Review; Review owns its
SCOPE_REVIEW Run. An upper LLM Review creates neither an implementation Execution nor
an AgentConversation. Both executors use existing FeatureArea claims, Task current
Review rules, cancellation receipts, runtime authority, and approval gates.

## Executors and compatibility

`executorKind` is immutable: `CODING_AGENT` or `UPPER_LLM`. `reviewDepth` independently
remains `FAST` / `DEEP`; it is neither reasoning effort nor a service tier.

Historical Reviews and old starts omitting executorKind retain CODING_AGENT semantics.
New callers explicitly provide the discriminator. Coding starts require a real model
and use the existing model-specific effort resolver and Codex configuration. Upper
starts reject Coding model/effort/configuration and never consult Desktop defaults.
Upper metadata currently has `provenance: UNAVAILABLE`; there is no trusted host model
identity on this transport. A BOUND aiSessionId locates a Bridge Project binding, not proof of a
ChatGPT conversation. No host identity, process, thread, turn, token count, or context
window is fabricated.

Migration 48 adds the discriminator, immutable report/receipt storage, and lifecycle
fields to existing grants/evidence. It preserves historical IDs, owners, configurations
and result text. Scope, instruction, baseline, executor and report cannot be edited in
place. Changed instructions or a different executor require a subsequent Review.

## Public sequence

GPT MCP and Local MCP share the same business tools and HTTP MCP implementation. The
ordinary Console can read the result and perform authorized user decisions; it cannot
create an unattended upper Review or submit a report as an upper LLM.

1. Use `session_initialize(requestId)` to obtain a real unbound aiSessionId, then
   bind the explicitly selected Project with `project_open(aiSessionId,projectId,requestId)`.
   Existing Sessions use `session_initialize(aiSessionId)` readback; every step
   retains that same ID. Binding alone does not admit an upper Review.
2. For an existing FeatureArea, use `task_create` if the semantic Task does not exist.
   Creating a Task does not start a Review. The existing `CREATE_AND_REVIEW` composition
   remains Coding-only; use this two-step path for an upper Review.
3. `task_review_start` explicitly selects UPPER_LLM. Core reserves the existing claim,
   persists the Review/Run/start receipt, prepares the canonical FeatureArea worktree,
   freezes the scoped content snapshot and activates its read grant.
4. `review_access_get` returns the exact target, state, allowed/forbidden paths,
   workspace/worktree IDs, baseline and startSnapshot. Copy the target unchanged into
   `file_list` / `file_read`. Do not build it from guessed IDs.
5. `review_evidence_get` reads up to 512 newest successful content receipts. Directory
   listings are activity, but never source inspection evidence.
6. `review_result_submit` atomically persists the report and closes the Review. No
   separate release call is required. `review_result_get` retrieves either the full
   report or, when requestId is supplied, the original completion receipt.
7. Present the resulting business decision. Completion does not approve implementation
   or accept a Task. No continuation asks GPT to analyze its own submitted report.

The following JSON uses placeholders for values obtained from actual Core responses.
Do not send the placeholder strings to a running Bridge.

```json
{
  "aiSessionId": "<session>",
  "featureAreaId": "<area>",
  "taskId": "<task>",
  "requestId": "review-start-001",
  "executorKind": "UPPER_LLM",
  "reviewDepth": "DEEP",
  "reviewInstruction": "Complete authored engineering instruction and limits.",
  "reviewPlan": {"mode": "FOCUSED", "selectedModules": [{"key": "<module-key>", "role": "PRIMARY"}]},
  "forbiddenPaths": ["tests", "internal/private"]
}
```

`REPOSITORY` is the other reviewPlan mode and has no selectedModules. For Upper,
FOCUSED resolves registered production roots into a hard read boundary. For Coding,
the existing focus/relevance and repository investigation semantics are preserved.
`forbiddenPaths` is an optional Upper-only array of normalized repository-relative
paths (128 entries, 1024 bytes each); it denies each path and all descendants. These
are literal paths, not globs. Existing prohibitions must be represented explicitly;
natural-language instructions are not parsed into access policy.

The target is a closed object with required nonblank IDs and a positive generation:

```json
{
  "aiSessionId": "<session>", "featureAreaId": "<area>", "taskId": "<task>",
  "reviewId": "<review>", "runId": "<run>",
  "runtimeEpoch": "<current-epoch>", "grantGeneration": 1
}
```

For `file_read` / `file_list`, send `{ "reviewTarget": <target>, "path": "src/main.go" }`.
Never mix reviewTarget with a Direct File workflowId or outer session/area scope.
`file_write` and the generic write service reject reviewTarget. Existing generic Direct
File writes retain their own explicit grant and lifecycle. Unscoped repository search is blocked for the creating Session while an Upper Review
on that Repository remains unfinished; use the controlled list/read tools for source
inspection. Search metadata is never substituted for content evidence.

`file_read` returns UTF-8 content, normalized path, byte size, Core-issued evidenceId and
SHA-256 digest. Individual reads retain the existing 256 KiB bound. Optional
`workflowComplete: true` changes access to `AWAITING_REPORT`: the Run and claim remain
active until report submission or cancellation. Grant/status/history reads are pure.

## ReviewReport version 1

Submission is the exact target above, plus requestId and report:

```json
{
  "aiSessionId": "<session>", "featureAreaId": "<area>", "taskId": "<task>",
  "reviewId": "<review>", "runId": "<run>",
  "runtimeEpoch": "<epoch>", "grantGeneration": 1, "requestId": "report-001",
  "report": {
    "schemaVersion": 1,
    "summary": "The inspected entrypoint needs an additional dependency review.",
    "disposition": "FURTHER_REVIEW_REQUIRED",
    "coverage": ["src/main.go"],
    "findings": [],
    "limitations": ["The dependency was outside this Review's authorized roots."],
    "recommendedNextSteps": ["Authorize the missing dependency in the next Review."],
    "evidenceRefs": [{"evidenceId": "<Core evidence>", "path": "src/main.go", "digest": "<64 hex digest>", "startLine": 1, "endLine": 5}]
  }
}
```

A finding has required `id`, `severity`, `title`, `description`, `impact`,
`evidenceRefs`, `suggestedChange`. Severity is CRITICAL/HIGH/MEDIUM/LOW/INFO and IDs
must be unique within the report. Evidence references always contain evidenceId,
path and digest; startLine/endLine are an optional pair with 1-based inclusive bounds.
References must match a successful content read for this exact Review/Run/epoch/grant,
workspace and baseline. Caller-supplied hashes cannot create evidence.

All report arrays are present, including empty findings/next steps/evidence arrays.
Coverage and limitations must be nonempty. Reports are at most 256 KiB serialized;
text entries at most 16 KiB; at most 128 findings or text-list entries, 256 report
references and 64 references per finding. The executable typed schemas live in
`internal/mcpserver/upper_review_tools.go`; domain checks in `internal/review/report.go`
also enforce byte and cross-field constraints.

Disposition rules:

| disposition | Required evidence/specification | Authoritative next step |
|---|---|---|
| APPROVAL_READY | Successful content read references; explicit valid workSpec and RUN verificationSpec | Existing Review Approval decision |
| FURTHER_REVIEW_REQUIRED | References optional; no proposal specs | REVIEW_REQUIRED, DEEP |
| BLOCKED | References optional; no proposal specs | REVIEW_REQUIRED, DEEP |

APPROVAL_READY uses the existing `task.WorkSpec` and `task.VerificationSpec` contract,
including full Codex instructions, mutation scope, checks and acceptance criteria.
Core never generates these from prose. Legacy Coding report text remains readable;
Upper also stores a compatibility ImplementationResult for the existing approval gate.
Core validates structure, provenance and technical evidence identity, not the semantic
correctness of an engineering conclusion.

The completed receipt has exactly these fields:

```json
{
  "requestId": "report-001", "reviewId": "<review>", "runId": "<run>",
  "reportId": "<immutable report>", "payloadSha256": "<64 hex digest>",
  "status": "COMPLETED", "disposition": "FURTHER_REVIEW_REQUIRED",
  "completedAt": "<RFC3339 timestamp>"
}
```

Full report retrieval returns `{report: {reportId, reviewId, runId,
createdByAiSessionId, baseline, startSnapshot, payloadSha256, report, createdAt}}`.
Receipt retrieval returns `{receipt: <receipt above>}`. Both require the creating
Bridge Session. Console reads use existing Project authorization, not a fabricated
upper Session.

## Snapshot, evidence and finalization

`baseline` preserves the admitted development commit. A canonical FeatureArea worktree
can contain accumulated changes; its actual reviewed content is separately identified
by `Run.start_snapshot` and `startSnapshot` in access/report reads. For UPPER_LLM this is
a SHA-256 of sorted, framed authorized paths and content digests, not a Git tree ID.
Internal snapshot checks are not upper source-reading evidence.

Snapshot capture is confined through existing descriptor-relative Root access. It
skips explicit forbidden subtrees and Git administrative paths, rejects symlink
components, and never hashes out-of-scope contents. Bounds are 4096 files, 4096
directories, 32 MiB total content, depth 64 and a bounded entry scan. Exceeding a bound
fails preparation with a visible cause; it does not silently omit files. Use a focused
scope when repository-wide capture exceeds these bounds.

Successful file operations compare the current scoped content with the immutable
start snapshot before returning evidence. APPROVAL_READY rechecks it and every
successful content observation in the current grant generation, including uncited
ones. Replacing the baseline or choosing only a newer digest cannot hide changed
content. Git ref advancement alone does not modify the captured worktree content;
historical results stay readable, while existing approval drift rules apply.
These are bounded observations of a live worktree, not an OS-level immutable copy;
external processes are outside Bridge's file transaction, and no claim of filesystem
snapshot isolation is made.

Reads, lists, submission and cancellation enter the same SQLite writer authority.
The read transaction remains open until I/O, evidence and last activity are recorded.
Thus submit/cancel wait for an admitted read and later reads fail the closed target.
Submission validates the report, enters FINALIZING, records intent/full payload digest,
persists the immutable report, terminalizes the Run, updates the Task/approval,
closes the grant, releases the existing claim, and records COMPLETED in one transaction.
There is no external process to stop in this path. A crash before commit rolls back
the entire transaction; a crash after commit leaves the completed receipt. No durable
half-report is exposed as completion. The grant transition and snapshot bindings are
also guarded by database triggers.

Idempotency hashes the normalized typed submission including its target. Same request
and payload return the original receipt; different payload conflicts; a new request
cannot replace a completed Review result. Receipt replay is checked before freshness,
so the original successful request remains replayable after epoch changes, with creator
authorization. A lost response requires replay/query, not a new report.

## Cancellation, recovery and client presentation

`operation_cancel` uses the existing exact REVIEW target and receipt. Upper's no-backend
branch additionally proves its exact live claim/grant/epoch and absence of thread,
turn and AgentOperation boundary. It then closes access and releases the claim through
the existing no-backend finalizer. Cancel wins → late report rejected. Submit wins →
later cancel returns ALREADY_TERMINAL without changing the immutable report.

On Core restart, nonterminal Upper grants become RECOVERY_REQUIRED; old epochs cannot
read/submit and claims are preserved. Explicit `agent_operation_recover` uses the same
area/Review/Run and requestId, with no agentConversationId for Upper. Its durable
receipt returns REAUTHORIZED / UPPER_REAUTHORIZED / NO_ADVANCE; it does not grant
fresh work. It atomically rebinds the same Run/claim to the new epoch and increments the
grant generation. Replay the original start request to prepare/reactivate the same
workspace after snapshot validation. Old callbacks stay fenced. The Coding recovery
path still requires its actual conversation and does not use this exception.

Preparation failure retains the discoverable Review, Run and claim and records a bounded
preparation blocker. Replay the same start request after fixing the cause, or cancel the
exact Review. A completed/canceled start request is a read of its original admission.

Presentation stages are derived from durable grant/report facts: PREPARING,
PREPARATION_FAILED, AWAITING_FILE_ACCESS, READING, AWAITING_REPORT, FINALIZING,
RECOVERY_REQUIRED, CANCELED, COMPLETED. Last activity is observed file activity, never
an assertion that the model is thinking. Upper token telemetry stays unavailable.
Standalone WebUI explains that upper work must start/continue from a bound LLM;
it does not ask the user to fill a GPT report. Cancellation names the actual owner
and is a secondary action. Report/history/refresh routes remain read-only.

Errors distinguish REVIEW_EXECUTOR_MISMATCH, REVIEW_SCOPE_FORBIDDEN,
REVIEW_REPORT_INVALID, REVIEW_EVIDENCE_MISMATCH, REVIEW_TARGET_STALE,
REVIEW_RESULT_FINALIZED, REVIEW_FINALIZING and REVIEW_PREPARATION_FAILED. Existing CONFLICT covers changed
idempotency payload; existing recovery/workspace errors retain their own codes.

`bridge_diagnostics` keeps the existing build version and adds businessCatalogSHA256
and unifiedReviewSchemaSHA256, derived from executable declarations. A version prefix
is not a complete deployed-HEAD proof. Actual ChatGPT tool refresh, notifications and
host rendering require real-host acceptance; local mocks are not that evidence.
