# Verification report and Task acceptance

The current schema-v130 contract separates native execution from GPT analysis.
A Verification Run `SUCCEEDED` records native completion and independently
captured command/Git evidence. It does not require a formatted Codex final
message, certify engineering correctness, or accept the Task.

## VerificationReport v2

New `NATURAL_V1` Turns omit `outputSchema` and preserve the natural final message.
The execution recorder freezes command evidence and observed check outcomes
before terminal completion. The Task stays `VERIFYING` with
`ANALYZE_RUN_RESULT` until GPT reads `run_analysis_get` and submits the separate
indexed report through `run_analysis_submit`. The source hash binds that exact
Run, frozen VerificationSpec, workset and captured observations. Native text is
never parsed as a report. An invalid GPT report can be corrected without
reexecuting Codex or revising the approved verification instructions.

The explicit GPT report covers every check and acceptance criterion by stable
index, with `PASSED`, `FAILED`, `BLOCKED`, `NOT_RUN` or `UNKNOWN` claims.
Bridge-observed command exit/Git facts and GPT claims remain separate fields;
GPT cannot replace observed evidence. Core stores the validated v2 envelope in
`runs.verification_result_json` without rewriting Run status, original output,
Thread/Turn or native completion time. Receipt and Task phase advancement commit
together. User acceptance remains a separate decision.

Runtime failure, authority loss, unresolved response disposition, scope
violation and verification worktree mutation retain their existing guards.
Missing or invalid *GPT analysis* does not retroactively fail native execution.
Historical dispatched/finished legacy Runs keep their original schema contract;
existing v1/v2 report codecs and legacy-result read semantics remain available.

## Per-Execution acceptance

After the latest Execution is terminal and the Task is nonterminal, with no
active FeatureArea writer, pending cancellation, unresolved runtime authority,
or prior receipt, Core derives
`TASK_ACCEPTANCE:<executionID>`. GPT assesses the structured evidence and the
user selects an option. `decision_resolution_receipts` is the sole durable
receipt mechanism:

- `ACCEPT` requires a persisted structurally available v2 report on the bound
  Verification Run, regardless of that Run's independent transport status, or
  the exact `USER_WAIVED` waiver for VerificationMode `NONE`; it writes the
  receipt/event and changes `Task.status` from `ACTIVE` to `SUCCEEDED`.
  `Task.phase` remains the last durable engineering phase.
  Structural availability includes exact coverage and identity binding to the
  terminal Execution's immutable VerificationSpec; item fact values do not
  constrain the user's choice.
- `REJECT` is the explicit rejection of the Task intent; it writes the receipt
  and changes the Task to existing status `CANCELED`.
- `REQUEST_CHANGES` requires explicit user confirmation of the complete next
  WorkSpec and VerificationSpec. Goal wording, natural-language constraints and
  legal file scope may change in that approved successor; Bridge does not judge
  their semantic relationship to the predecessor. Comments alone do not authorize
  an unspecified successor or expanded access. Exact Task ownership, repository,
  runtime configuration, baseline and technical filesystem authorization remain
  enforced. One transaction writes the
  predecessor receipt, an internal `APPLIED` TaskPlan, a new immutable
  TaskDefinitionRevision, Task pointers, `Task.status = ACTIVE`,
  `Task.phase = IMPLEMENTATION_READY`, and one `CONFIRMED`
  successor Development Execution under the same Task.
- `REQUEST_REVERIFICATION` writes the predecessor receipt and one confirmed
  successor Execution under the same plan and revision, with `Task.status = ACTIVE`
  and `Task.phase = VERIFICATION_READY`. It accepts no new successor spec or
  implementation instruction. Its atomic reservation
  creates only the Verification Run: `run_id == verification_run_id`, with no
  Implementation Run.

For multi-repository reverification, acceptance and delivery follow only exact
immutable `REQUEST_REVERIFICATION` receipts to the successful implementation
under the same Task and revision. Each Run retains its original Execution owner.
Frozen runtime authority is compared with only that receipt-authorized Execution
ID difference normalized in memory; member observations must still prove an
unchanged verification workspace continuous with the original implementation.

New confirmation and approval preserve required `CUSTOM` instructions as explicit
indexed GPT analysis obligations. Codex executes their authored verification;
GPT must assess the actual output/evidence or report BLOCKED, NOT_RUN or UNKNOWN.
These assessments remain `Reported`, with no fabricated `Observed` fact or exit
code. A complete report is not proof that a custom scenario ran or passed.
Explicit completion and integration require every required CUSTOM assessment
and acceptance criterion to be PASSED; recovery resolution still requires its
independent authoritative observations and cannot borrow an analysis claim.
CUSTOM commands and typed command checks retain exact command evidence;
instruction-only Git checks require a supported typed assertion at the repository
root. Missing/wrong-repository command evidence remains insufficient. Requiredness,
authored instructions and historical stored bytes are never rewritten.

Both successor actions preserve the exact frozen v46 next-run runtime selection
(model, reasoning effort and Codex Fast Mode) separately from the immutable
TaskDefinitionRevision. Replay cannot replace that selection with newer defaults.

Historical UNKNOWN is not itself a current blocker. Core shares one durable
unresolved-authority predicate with Git delivery and workflow guidance: only an
exact permanent orphan with revoked authority, ended conversation, quarantined
and detached workspace, and released claim ceases blocking later work. UNKNOWN,
finalization and orphan evidence remain intact, and physical backend death
remains unknown. Unfinished recovery and current writers remain fail-closed.

A recovered terminal failed Execution may retain its Task's in-flight phase.
When guidance exposes runtime disposition, REJECT and REQUEST_CHANGES remain
available; REQUEST_REVERIFICATION additionally requires the exact revision's
successful Implementation lineage. This follow-up does not add ACCEPT to a
phase where current authoritative presentation does not expose it.

Only the latest eligible Execution can be decided. A replay must match the same
decision, option, request ID and, for changes, the frozen successor definition
hash. Older receipts remain audit history but do not block a successor. The
existing unique `(execution_proposal_id, purpose)` Run fence and FeatureArea
claim serialization remain authoritative. Run/Execution terminalization releases
the operation claim before user acceptance; the nonterminal Task retains the
same-FeatureArea lineage until acceptance or an authorized successor. Queue,
reservation validation, and current Execution binding all give an earlier
nonterminal Development Task priority over a later Task in the same FeatureArea.

## Presentation

The shared Task-first projection separately exposes Task overall state, current
Execution outcome, Verification Run transport status, VerificationReport facts,
current Execution acceptance, and historical Execution receipts. Web, Card,
Console, and MCP render those fields mechanically. `decision_respond(kind="TASK_ACCEPTANCE")` is the GPT/Card and Local MCP adapter to the existing
Core Acceptance resolver. It requires `aiSessionId`, `taskId`, `featureAreaId`,
`executionId`, `taskDefinitionRevisionId`, `decisionId`, `option`, and `requestId`.
REQUEST_CHANGES additionally requires complete `nextWorkSpec` and
`nextVerificationSpec`, including their caller-authored `codexInstruction` strings.
Card collects explicit JSON without generating or falling back to old instructions.
Web Task actions and Local `decision_respond` reach that same Core authority.
The disjoint `decision_respond(kind="REVIEW_APPROVAL")` branch remains Review-only;
its REJECT cannot resolve Acceptance. Runtime permission uses
`runtime_request_respond` and retains its own typed identity/retry contract.

The Core checks observed Task/Execution/revision/gate identity before receipt
replay, and current ownership/eligibility before a new mutation. Identical retries
return the same durable receipt and successor; changed payloads conflict. Web
replay reaches Core even after the original action has disappeared. Card hashes
the exact target, option and supplied specs into a stable retry requestId and
refreshes the same Card after the call.

## Deterministic verification tests

Semantic closure tests use an in-process fake Coding Agent runtime, injected
fixed clocks and deterministic application IDs, and a SQLite database created
under the test's temporary directory. Acceptance histories are loaded by Task
ID and assert, independently, Run transport, VerificationReport item facts,
per-Execution outcome/acceptance receipt, and Task overall state. AgentClaim
text is never an expected authority. These tests do not use real Codex turns,
external network services, live data roots, benchmarks, or `real_e2e` paths.

## Verification mutation and historical transport

`VERIFICATION_MUTATED_WORKTREE` is an infrastructure/transport failure:
Verification Run `INFRASTRUCTURE_FAILED`, Report `UNAVAILABLE`.
Verification dispatch uses the read-only runtime authorization ceiling as well
as the post-run snapshot fence. It is neither
an assertion failure nor an Implementation failure nor a rejection of Task
intent. Current Tasks remain nonterminal after Execution completion, and the
FeatureArea operation claim is released. Old terminal Tasks remain `FAILED`
with acceptance `LEGACY_UNRECORDED`; reads do not reopen them or create receipts.
Legacy v1 mutation envelopes are unavailable evidence and retain their original
stored bytes when other Run fields are updated.

The shared Task projection exposes Implementation transport, Verification
transport and failureCode, explicit report availability, current Execution
state/outcome, and current/historical acceptance independently. AgentClaim
cannot supply any of these values. A verification-only successor can request
reverification again by following durable REQUEST_REVERIFICATION receipts to
the successful Implementation of the same immutable definition. It never
owns or creates a synthetic Implementation Run.

## Final read-only verification

Complete source, test, fixture and formatting changes in Development. Record
all tracked and untracked managed file contents, modes, index and HEAD before
final verification. Use offline module resolution, an existing module cache,
and build cache/temp directories outside the repository. Do not run formatters,
generators, fixture/golden update modes or package installation. Compare the
complete snapshot after all checks; any repository mutation invalidates the
verification and requires returning to Development followed by a new full run.

## Explicit successor content and replay versions

New REQUEST_CHANGES successors retain authored goal and constraint bytes. Their
immutable definition records `continuationNormalizationVersion = 2`. Scope paths
retain the existing explicit path-cleaning and duplicate/root shape rules, and
any `..` path component is rejected before cleaning. These protocol rules do not
compare a new approved scope with the predecessor's legal file set.

Legacy definitions without this marker use version 0 only when checking their
existing receipt under the historical normalization/hash contract. Unknown
versions are rejected. A new request never inherits legacy text normalization,
and replay still compares the saved payload and exact approved revision rather
than treating an old approval as authority for changed content.
