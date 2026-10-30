# FeatureArea-first request routing

GPT separates engineering coverage, user authorization and Core execution admission.
A relevant Review provides knowledge, not permission. A user continuation request does
not prove the old writer stopped. An execution blocker does not require another Review.

## Bounded discovery and accurate source reads

Verify the conversation's real Session and bound Project. For an exact Task or Review ID,
validate that object's ownership directly; do not scan unrelated work. Otherwise:

1. `work_list(kind=FEATURE_AREA, limit, offset)` returns Project-authorized areas,
   including retired/decomposed history, in stable ID order. Historical listing grants
   no engineering access. `featureAreaId` is an optional exact assertion.
2. `work_list(kind=TASK, featureAreaId, limit, offset)` lists semantic Tasks, including
   terminal Tasks. Repository and current Review/Execution/definition IDs narrow reads.
3. `work_list(kind=OPERATION, featureAreaId, taskId, limit, offset)` lists persisted
   Review/Execution/Inspection operations. Unknown semantic targets stay explicitly
   unavailable in their original page positions.

Defaults are 25 items, at most 100, with `offset`, `hasMore` and optional `nextOffset`.
Filtering precedes paging and a separate lookahead establishes `hasMore`. A source or
lookahead failure returns an error, never successful absence. Independent reads are not
an atomic snapshot; inventory changes between pages may require rereading. Offset has
an explicit 100000 bound. These are bounded responses; underlying existing stores can
still load Project inventory before paging. `workspace_get` NAVIGATION remains the
compatible view snapshot, not a promise of bounded discovery or relevant selection.
No first, newest, unique or Card-browsed item becomes an implicit command target.

`workspace_get(taskId, include=[TASK])` returns `taskSources` with actual current pointers,
the immutable current definition, payload hash, instruction provenance and its original
Review approval receipt. Optional `definitionRevisionId` requires that exact Task and
TASK include, and reads a historical revision of that same Task. The response retains
current pointers separately, so a historical selection cannot masquerade as current.
Legacy absence is `NOT_RECORDED`; source lookup/identity failures are failed partitions.

`operation_get(target)` exposes a Review's bounded purpose/focus, Repository, registered
baseline, recorded repository provenance, disposition, actual Run status and bounded
result summary. `summaryTruncated` signals shortening; RESULT obtains the full report.
The stored baseline/repository provenance is not asserted to be a current workspace HEAD.
EVIDENCE requests existing exact Run/workspace evidence and repository drift facts.
A completed Review, APPROVAL_READY disposition and a user's APPROVE receipt differ.

Add SOURCE for the actual Review decision receipt and current Task sources, or the exact
Execution's adopted definition. `UNRESOLVED_OR_UNRECORDED` means no durable receipt was
found; it does not claim a pending decision exists. A database/source error remains a
failed partition. Definitions with a recorded Review approval must resolve that exact
Review and same Project/Task/Repository APPROVE receipt; they never borrow another Task's
approval. Original source review, current review, instruction source revision and current
definition are separate fields. Later explicit revisions take precedence over favorable
old conclusions. Instruction-only successors retain their provenance; unchanged technical
continuation retains the same revision. TaskView ExecutionHistory exposes exact
`predecessorExecutionId` and `sourceDecisionId` from persisted receipt subject/result
relations; creation time never defines the chain.

## Routing and continuation

- `CONTINUE_EXISTING_TASK`: same ACTIVE Task, sufficient engineering evidence and clear
  user authorization. Use an eligible initial entry or the exact undispatched candidate.
  Active/UNKNOWN writers remain blocked until the original coordination protocol permits
  proceeding. Where Core offers TASK_ACCEPTANCE/RETRY_IMPLEMENTATION, submit that exact
  decision and original request identity, read the confirmed successor, then invoke
  `execution_start`. A failure alone is not an instruction to create another Review.
- `REVIEW_EXISTING_TASK`: same goal, a concrete missing assumption or incremental scope.
  The formal `task_review_request` consumes the shared REQUEST_TASK_REVIEW exact target,
  purpose and stable request ID. It supersedes eligible old authority and moves the same
  ACTIVE Task to REVIEW_REQUIRED; the existing `task_review_start` then creates the new
  Review. An existing pending Review proposal still uses REVISE. Neither operation
  bypasses a reservation, active writer, unsettled UNKNOWN or file I/O. See
  [the supplemental Review contract](same-task-supplemental-review.md) for the state,
  supersession, reapproval and replay rules. This is optional and never a mandatory
  prerequisite for an eligible technical retry.
- `CREATE_TASK_IN_AREA`: independent goal or new work after a terminal Task. Record source
  Task/Review/version references in the authored problem statement or constraints, retain
  the old Task as history and follow fresh Review/approval. Knowledge is reusable; old
  approval does not authorize the new Task.
- `CREATE_FEATURE_AREA`: no suitable responsibility after sufficient discovery, with a
  clear user goal. Multiple related areas, pagination or failed reads do not establish
  this condition. Select a primary responsible area; split independent deliveries only.
- `NEED_MORE_EVIDENCE`: read a necessary file/source or ask one material question. There
  is no ScopeAssessment entity, semantic matcher or compulsory scope approval.

Unchanged technical continuation reuses the approved revision; explicit instruction-only
changes use complete `executionInstructions` and a new immutable revision. Substantive
changes use the full authorized REQUEST_CHANGES contract. Preserve old failed Execution,
Run, prompt and Review records. Exact replay returns the same successor; changed payload
conflicts. Dispatch remains outside the transaction via the original reservation and
UNKNOWN protocol. Actual model, reasoning effort and Fast Mode follow explicit choice or
the documented inherited configuration, not Review configuration or the first model.
DIRECT_FILE uses its approved file-authority contract, never Coding Agent retry/start.

RETRY_IMPLEMENTATION uses an exact TASK_ACCEPTANCE decision (Task, FeatureArea,
predecessor Execution and revision). It only accepts failed/canceled predecessors whose
Run, runtime authority and finalization satisfy the canonical predicate. A missing legacy
implementationMode is not inferred from an absent field or a prompt: the new retry is
unavailable (`CODING_AGENT_REQUIRED`); historical reads and original receipts remain
intact. A caller-scope mismatch is an explicit unavailable action, not a failed Task page.

The optional complete `executionInstructions` object changes only the two canonical
instruction fields. Omitting it or supplying identical text reuses the revision. V54
adds immutable payload/source metadata linked to the original acceptance receipt; it is
not a second receipt store. The decision transaction creates the receipt, optional
revision and CONFIRMED successor and updates Task pointers together, without touching
old Run/history. Run reservation and dispatch occur later through `execution_start`;
RETRY successors are excluded from the existing automatic approval queue. Exact receipt
replay does not wake that queue or create another Run.

Omitted `nextRunRuntimeConfig` inherits the predecessor's actual model, reasoning effort
and Codex Fast Mode. When that configuration is incomplete or no longer allowed, a safe
retry can be offered with `requiredInputs=["nextRunRuntimeConfig"]`; provide a complete
explicit allowed selection. Core still validates it at submission. WebUI exposes that
input requirement instead of claiming inheritance succeeded; Console and MCP share the
same command. Available means the observed operation is eligible with valid inputs,
not that the user has already authorized it or a runtime has started.

## Read-only code evidence and conversational boundary

Use bounded search to locate necessary files, then `repository_file_read` with the actual
Project/Repository authorization and its documented source/range/hash contract. Reading
uses `repositoryId`, repository-relative `path`, optional exact 40/64-character `commit`,
byte `offset` and `maxBytes` (up to 64 KiB). The full observed file is capped at 8 MiB;
larger/unreadable files fail explicitly. `REGISTERED_WORKTREE` names the registered source
worktree, not a managed Task workspace; `COMMIT` names the exact historical commit.
There is no managed Workspace selector in this interface. Return fields include actual
`headCommit`, full `contentSHA256`, `sizeBytes`, `startLine/endLine`, `nextOffset`,
`truncated`, and UTF8 or BASE64 encoding (including split UTF8 byte pages). All path
components reject symlinks for current reads; historical reads accept only regular Git
blobs. `.git` components are rejected. Git reads disable lazy fetching and replacement
objects, so a missing partial-clone object fails rather than writing the object store.
The existing repository search/Upper grant guard remains in force; bound identity is not
an additional grant. Reading must not create a Review/Run/Inspection, enable Direct File, revive grants or prepare a
workspace. Keep source kind, actual repository/workspace identity and HEAD/commit separate
from the content hash. Use the returned range continuation and expectedContentSHA256
before joining file segments; cross-file reads are not automatically atomic. Actual code
changes inform GPT's engineering judgement, not a Core NEW_REVIEW_REQUIRED decision.

Explain the chosen area/Task, source Review and exact next action briefly. Clear authorized
continuation does not require a mechanical second confirmation. An analysis request does
not start work. Prompts remain standalone implementation/verification instructions that
incorporate resolved user choices without asking the executor to read chat or live SQLite.
The Card remains a read-only view; navigation never changes conversational write targets.

## Static and manual verification boundaries

Source analysis and formatting checks are not executed business, retry, concurrency or
host tests. Manual acceptance must separately cover a failed safe predecessor, unchanged
and revised instruction successors, exact replay and changed-payload conflict, active or
unknown writer refusal, the supported incremental Review transition, a terminal Task's
new follow-up Task, insufficient evidence and DIRECT_FILE separation. No real objects are
created, retried or read from live SQLite during implementation of this change.
