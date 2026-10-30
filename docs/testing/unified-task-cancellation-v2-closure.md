> Historical validation record. These commands describe a prior checkpoint, not current package or architecture contracts. See [final v0.1](../architecture/task-execution-v0.1.md).

> Historical implementation evidence. [GPT surface consolidation V1](../architecture/gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

# Unified Task and canonical cancellation V2 closure

Current Task/Execution authority and automatic approved Implementation follow the [final v0.1 model](../architecture/task-execution-v0.1.md).

Acceptance: `UNIFIED_CODEX_TASK_AND_CANONICAL_CANCELLATION_PROTOCOL_CLOSURE_V2_READY`.

## Baseline and Git delivery

Fresh baseline was clean branch `dev_20260908_1913`, HEAD
`bd772e94417b703f010a698870c50e97a26e6506`, source migration 35.
Implementation and validation used isolated branch
`codex/unified-task-cancellation-v2` and worktree
`/private/tmp/devbridge-unified-task-cancellation-v2`.
The delivery commit is the commit containing this report; the final response
records its full SHA. Delivery uses fast-forward into the unchanged development
baseline, followed by removal of this task's branch, worktree, builds and cache.
No push, release, formal port 8081 runtime change or live SQLite migration was
performed. Other existing worktrees and runtime workspaces are outside this task.

## Final contracts

The canonical specification is
[Unified Agent work and cancellation](../architecture/unified-agent-work-and-cancellation.md).

| Task type | Scope | Durable ownership |
| --- | --- | --- |
| INSPECTION | PROJECT | Visible Task envelope → ContextProbe → exact inspection Run; independent read-only Codex discovery; no invented FeatureArea or engineering Host Tool authority |

| Canonical cancellation owner | Accepted target and convergence |
| --- | --- |
| REVIEW | Exact Review/Run; known interrupted terminal or UNKNOWN orphan; same REVIEW Task terminal |
| EXECUTION before Run | PENDING_CONFIRMATION / CONFIRMED owner; authoritative absence of Run and claim; NO_BACKEND; outcome/Task canceled without fake Run, Turn, interrupt or confirmation |
| EXECUTION with Run | Existing exact implementation / verification protocol, including one-shot interrupt, response-loss replay, completion races and atomic finalization |

`operation_cancel`, compatible `execution_cancel` and HTTP cancellation delegate
to the same store, receipt table and runtime supervisor. ACK is not terminal.
Pending TaskPlan decisions are rejected, canceled Tasks cannot CONFIRM/REVISE,
and canceled Executions cannot consume authorization. Existing receipts retain
historical meaning. Approval CANCEL still declines only its requested action.

UNKNOWN_ORPHANED records revoked Bridge authority, isolated workspace/context and
unknown physical backend outcome/quiescence. Original UNKNOWN observations are
unchanged. One transaction terminalizes the owner/Task, expires pending approval
work, quarantines the old workspace, ends/rotates its context, appends an exact
permanent disposition, then releases the exact claim. Permanent Run and claim
fences plus existing epoch/Thread/Turn and Host Tool checks reject stale writes,
recreated claims, late success, new phases and revived Decisions. New work cannot
reuse the quarantined workspace. Claim release means authority ended; it does
not prove backend death. Known terminal still consumes real exact terminal and
quiescence evidence. Close, quarantine, recovery receipts and epoch rotation
never manufacture SafeTerminationEvidence.

Restart consumes committed cancellation intent without a second destructive
signal. Revocation/quarantine/disposition/release are atomic and idempotent;
fault injection verifies rollback at intermediate boundaries. Explicit recovery
of older UNKNOWN operations uses the same orphan seam. Pre-v27 unbound legacy
observations retain their original compatibility receipt and ended-context/epoch
fences; their old NO_ADVANCE field is not rewritten into a new physical proof.

## Schema, history and projections

Legacy Task-less Reviews receive deterministic `work_<review-id>` envelopes and
bind their existing Runs. Review/Run IDs, results, timestamps, branch/revision
provenance and conversation history remain intact. Historical Probes receive
INSPECTION envelopes without fake historical Runs. Backfilled envelopes are
marked LEGACY_BACKFILL/read-only, and repeated migration creates no duplicates.
Older ambiguous Development carriers retain the pre-existing legacy-read-only
compatibility policy and detail access; they do not acquire new authority.

The shared Task.Work projection exposes type, scope, phase, authoritative status,
owner, current/latest Run, Agent, tokens, time, cancellation and backend
disposition. Console/Web add name, model and branch provenance. Text/JSON/GPT MCP /
Local MCP use the same inventory through `task_work_list`; the single main Card
navigates Review and Project Inspection envelopes. Workflow Decisions still
select Development Tasks only. Inspection counts remain canonical in ContextProbe
and are not counted a second time through its Run. Unavailable historical counts
are not fabricated.

## Validation evidence

All commands ran against isolated worktree resources with a temporary Go cache.

| Gate | Result |
| --- | --- |
| `go test ./... -count=1` | PASS, 54 tested packages; opt-in real-backend tests retain their explicit skip gates |
| Full affected race matrix | PASS across storage/sqlite, execution, review, taskpreparation, executionproposal, contextprobe, probeexecution, controlapi, consoleapi, bridgehttp, mcpserver, localmcp, codex, controlplane; controlapi's obsolete Review assertion was fixed and revalidated, followed by final changed-package reruns |
| `go vet ./...` | PASS |
| Production Go / embedded Web build | PASS: devbridge, devbridge-mcp, devbridge-local-mcp; Web assets are embedded by the Core Go build |
| Web/Console and ChatGPT DOM/transport JS | PASS, 105/105 via `node --test internal/bridgehttp/*test.js internal/mcpserver/*test.js` |
| Production ChatGPT UI build | PASS: two self-contained HTML assets and SHA-256 manifest via `node scripts/build-chatgpt-ui.mjs <empty-temp-output>` |
| Desktop no-Turn boundary | PASS: desktop `go test ./internal/codex -count=1`; no changed Task DTO consumer found in Desktop |
| Migration / cold resume / failure injection | PASS in full storage suite, including v35→v36 history, deterministic replay, orphan rollback, immutable boundary and late callback checks |
| Termination boundary regression | PASS: `TestAuditOwnedCloseDoesNotJoinDescendantWriter` proves a descendant may write after direct app-server Close |
| `gofmt -l` and `git diff --check` | PASS, no findings |

The final packaged acceptance runs the three candidate binaries on ephemeral
loopback ports with a disposable DataRoot/repository and a bounded app-server
protocol fixture. It verifies actual GPT/Local proxy calls and Web Task inventory:

| Candidate path | Result |
| --- | --- |
| REVIEW known | Immediately visible REVIEW Task and linked Run → same Task CANCELED, TERMINATED, claim released |
| REVIEW transport loss | Same Task CANCELED, UNKNOWN_ORPHANED, original UNKNOWN / Quiesced=false retained, workspace quarantined and context ended |

`REAL_CODEX_UNKNOWN_NOT_RUN`. No paid real Codex UNKNOWN experiment was required
or claimed. Late callbacks are exercised through the real supervisor/storage
adapter matrix; candidate DB inspection independently verifies disposition,
quarantine, ended context, Task terminal and released ownership.

## Whole-diff review and remaining limits

Physical descendant liveness remains unknown on orphan paths; descendants can
still write inside their old quarantined workspace. This is an explicit accepted
limit, not a physical termination guarantee. Conflicting durable identities fail
closed rather than infer authority. Historical telemetry/provenance remains
unavailable where never recorded. FeatureArea RETIRE stays idle-only: no RETIRING
coordinator, closing UI, FeatureArea-wide automatic cancellation or subsequent
RETIRE orchestration was implemented.
