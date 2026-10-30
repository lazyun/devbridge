> Historical validation record, superseded by the [final v0.1 model](task-execution-v0.1.md). This milestone does not define current production behavior.

> Historical implementation evidence. [GPT surface consolidation V1](gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

# Review Approval to Development Execution authority closure v2

Status: `REVIEW_APPROVAL_TO_DEVELOPMENT_EXECUTION_AUTHORITY_CLOSURE_V2_READY`.

The implemented contract is [Review Approval workflow authority](review-approval-workflow.md).
This report records the implementation, validation, and limits of the closure.

## Baseline and delivery boundary

- Starting development branch: `dev_20260908_1913`.
- Starting HEAD: `cb30f3c3b64bd678054b5d14923d5372ee3b4778`, initially clean.
- Starting schema: v37; resulting migration: **v38**.
- Implementation branch: `codex/review-approval-v2`, in an isolated worktree.
- The final source revision is the commit containing this report; its exact SHA
  and the development branch's final HEAD are recorded in the acceptance response
  and `/private/tmp/devbridge-review-approval-v2-evidence/git-final.txt`.
- Formal runtime: existing PID 83720, `127.0.0.1:8081`, baseline build revision
  `cb30f3c3b64bd678054b5d14923d5372ee3b4778`. It was not replaced or restarted.
- All migrations, mutations and process acceptance used isolated test databases
  and DataRoots. The live SQLite was not opened or migrated. No push or Release.

## Final workflow and authority

```text
Requirement → FeatureArea → visible REVIEW Task → Review → Execution → Codex Run
→ durable successful Review result → REVIEW_APPROVAL Decision
→ APPROVE → DEVELOPMENT Task + immutable initial TaskDefinitionRevision
→ automatic first Preparation → internal TaskPlan + approved Execution
→ Implementation → Verification → terminal result
```

Configured GPT/upper LLMs are trusted user-intent agents. GPT, Local MCP, Web and
Card can submit APPROVE, REJECT or REVISE. Core validates exact current workflow
state; there is no new login, OAuth, click challenge or human-identity proof.

The visible REVIEW Task remains separate from DEVELOPMENT: Review does not run
Preparation or an Execution. It owns its exact result/evidence and terminal
completion. Production Task creation endpoints reject direct development creation;
the Review Approval handoff is the new materialization boundary.

Review completion and startup recovery idempotently ensure one pending Decision
on the write side. Approval freezes a versioned structured implementation result,
including the agent-authored work and verification specifications. Empty,
malformed, failed, canceled or superseded Reviews cannot authorize development.

The immutable snapshot binds Project, FeatureArea, REVIEW Task, Review, Run,
accepted start request/payload hash, scope, engineering instruction/prompt hash,
repository revision, development branch/cycle, exact result text and SHA-256.
The existing Decision receipt infrastructure stores option, request, subject,
Project and resulting Task/Review identity. `review_approval_receipt_context`
records the resolving AI session for all three options. APPROVE also binds the
Task and initial revision through `review_approval_materializations`.

Receipt, resolving-session context, lifecycle activation, one DEVELOPMENT Task,
initial revision and materialization mapping commit atomically. An injected
failure rolls the entire transaction back. Duplicate requests return the same
receipt/Task; response-loss retry returns fresh guidance. Opposite options,
superseding Reviews and closed FeatureAreas cannot reuse old authority.

Preparation remains 1:N, with exact approved revision, source Review, visible
Run, claim and runtime epoch. It performs internal planning after approval.
Material scope conflict, path traversal, absolute paths or modifications beyond
approved PATHS require a new Review/Approval/Task. Conflict cannot be bypassed by
re-preparing the old Task. A failed Preparation on a still-ready Task may be
explicitly retried. Canonical cancellation takes precedence.

READY Preparation atomically materializes an internally accepted plan and one
already CONFIRMED Execution. It creates no TaskPlan or Execution user receipt and
does not call the old confirmation handoff. Execution startup and subsequent
Run reservations recheck the approved source, revision and lifecycle.

## Shared guidance and adapter behavior

Core supplies `status`, `message`, `currentState`, `nextAction`, relevant IDs,
`sourceSequence` and opaque `generation`. The snapshot is client-neutral; next
steps are not classified separately by adapters. Scoped success and rejection
responses expose the guidance envelope; existing entity status fields retain
their original meaning. Complete Core snapshots are preserved intact, preventing
a newer phase from being combined with older IDs during concurrent handoff.

| Current authoritative fact | Success/read nextAction | Wrong advancement |
| --- | --- | --- |
| No FeatureArea | SELECT_FEATURE_AREA | FEATURE_AREA_REQUIRED |
| FeatureArea without Review | START_FEATURE_AREA_REVIEW | REVIEW_REQUIRED |
| Review running | WAIT_FOR_REVIEW_COMPLETION | REVIEW_IN_PROGRESS |
| Unusable Review result | REVIEW_AGAIN | REVIEW_NOT_USABLE / approval prerequisite rejection |
| Review complete, Approval pending | WAIT_FOR_USER_APPROVAL | REVIEW_APPROVAL_REQUIRED |
| REJECT | WORKFLOW_REJECTED | no Task; opposite option rejected |
| REVISE / stale source / material scope conflict | REVIEW_AGAIN | old Task/Approval cannot advance |
| APPROVE, Task ready | WAIT_FOR_PREPARATION_START | direct Task creation cannot duplicate it |
| Preparation running | WAIT_FOR_PREPARATION_COMPLETION | no Execution startup |
| READY Preparation / Execution ready | START_EXECUTION | exact Execution required |
| Implementation running | WAIT_FOR_IMPLEMENTATION_COMPLETION | no second start |
| Verification running | WAIT_FOR_VERIFICATION_COMPLETION | no second start |
| Successful terminal | WORKFLOW_COMPLETE | terminal work cannot be revived |
| Canceled / failed Task or Execution | WORKFLOW_CLOSED | no new Run |
| RETIRING | WAIT_FOR_RETIREMENT | FEATURE_AREA_RETIRING |
| RETIRED / DECOMPOSED | WORKFLOW_CLOSED | no late approval/materialization |
| Runtime permission / unsafe context | RESOLVE_RUNTIME_APPROVAL / RECOVER_AGENT_OPERATION | original ownership and recovery gates remain |

WAIT is a real state: guidance itself does not start a Review, approve a Decision,
create a Task or run an Execution. Every actual mutation revalidates state.

- GPT and Local expose `review_approval_respond`, `workflow_guidance_get` and
  `execution_start`; generic `decision_respond` supports Review Approval.
- HTTP exposes `/v1/review-approvals/resolve`, `/v1/workflow-guidance` and
  `/v1/executions/{id}/start-approved`.
- Web's FeatureArea detail uses the same receipt and displays the exact proposal,
  approval actions and Core state. Navigation version checks fence stale responses.
- Console Task and Preparation rows no longer show a second confirmation for new
  approved work. Card uses Review → Approval → Task → Run; Preparation, Implementation and
  Verification appear as Run substatus. Canonical runtime permission/cancellation
  controls remain available.
- New guidance and Card refresh are pure reads. Existing legacy `CurrentWorkflow`
  Preparation observation is retained only for historical workflows.

## Validation and dogfood

Evidence is retained at `/private/tmp/devbridge-review-approval-v2-evidence/`.
The deterministic backend is explicitly **REAL_CODEX_NOT_RUN**.

| Gate | Evidence |
| --- | --- |
| Full `go test ./... -count=1` | `full-final.log` |
| `go vet ./...` | `vet-final.log` |
| Affected race suite: storage, controlapi/controlplane, console, HTTP, GPT/Local, Preparation, execution/proposal | `race.log` |
| Final changed-path race regressions | `race-final.log`, `race-last-delta.log` |
| Web JS and ChatGPT DOM suites, 115 passing tests | `js-final.log` |
| Production Core, GPT MCP and Local MCP binaries | real candidate acceptance below |
| Production ChatGPT UI build, two self-contained assets | `ui-build-final.log` |
| v37→v38, duplicate migration, legacy preservation and FK checks | full storage suite |
| Restart, completion ensure, concurrent duplicate approval and injected transaction failure | full storage suite |
| Changed Go formatting and whitespace | final `gofmt -l`, `git diff --check` |

The candidate acceptance starts three actual binaries with a private DataRoot and
OS-assigned loopback ports. It reproduces the "Hot-reload Codex runtime model
configuration" requirement through the native app-server transport:

1. FeatureArea creation returns REVIEW_REQUIRED; direct Task creation rejects.
2. Review runs under one visible REVIEW Task, then produces a durable result and
   pending Approval, with zero DEVELOPMENT Tasks.
3. Web APPROVE commits one receipt and Task; GPT retry reads the same identities.
4. Local starts Preparation; Console shows EXECUTION_READY and READY Preparation,
   with no second confirmation.
5. HTTP starts the approved Execution; GPT start replay is harmless; verification
   reaches COMPLETE and Local approval replay returns fresh terminal guidance.
6. GPT REJECT and REVISE create no Task. RETIRE before approval denies late APPROVE.
   GPT APPROVE followed by Local RETIRE converges without task duplication/revival.

The in-process cross-adapter acceptance also holds individual phases to verify
running guidance and exact immutable revision references. Existing cancellation,
UNKNOWN_ORPHANED, recovery, response-loss, runtime approval and RETIRE regression
suites remain enabled. Historical flows are seeded only in explicit test fixtures;
no production adapter exposes a legacy-create switch.

## Whole-diff review and limitations

Reviewed the new write paths, migration invariants, ownership checks, source
snapshots, replay, late callbacks, immutable definitions, internal execution
handoff, error envelopes, schemas, client projections and historical compatibility.
Found and fixed scope-conflict re-preparation, mixed-snapshot response IDs,
Web drawer response races and residual second-confirmation labels/advice.
No unresolved P0/P1/P2 finding remains in the inspected change.

The configured engineering agent must report semantic changes in prose (goals,
architecture, security/runtime boundaries) as CONFLICT. Core enforces explicit
verdicts, immutable structured definitions, path bounds and provenance; it does
not infer arbitrary semantic equivalence from natural language. Actual paid
Codex behavior was not exercised. The production runtime remains on the baseline
build; applying v38 there requires a separate deployment.

Canonical documentation was updated in README, interface reference, user guide,
ownership/design documents and the new workflow authority document. Historical
TaskPlan/Execution confirmation remains readable, finishable, cancelable and
recoverable; migration does not invent or rewrite old authorization receipts.
