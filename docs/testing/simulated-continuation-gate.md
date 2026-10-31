# Test-only simulated continuation gate

This gate is separate from [the production-binary Core simulator](core-simulation.md).
It addresses a boundary that the model-free DIRECT_FILE route cannot cross:
`DEFER_TASK` / `CONTINUE_TASK` require a real current Execution at a terminal
verification checkpoint. It never inserts an Execution, Run, decision or hold
row to manufacture that checkpoint.

## Run

From the repository root, with the repository's Go toolchain and Git installed:

```sh
./scripts/test-simulated-continuation.sh
./scripts/test-simulated-continuation.sh --race
./scripts/test-simulated-continuation.sh --sequential
```

Set `DEVBRIDGE_SIM_EVIDENCE_DIR` to an optional evidence parent directory. Each
invocation creates a new child, builds its own test executable, records source
revision/status/hashes and binary hash, and retains `test.log`, `test.exit`,
`report.json`, complete `mcp.jsonl`, the subprocess request log
`agent-wire.jsonl`, outgoing protocol writes in `agent-responses.jsonl`, local
Core observations in `core.jsonl`, and independently observed fixture-file/HEAD
snapshots. Response records are written after the unchanged stdout write and
include its byte count/error; they do not by themselves prove Core received it.
If the pipeline observation budget expires, before normal cleanup the gate
also captures a goroutine dump and bounded read-only Run/runtime/event/session
diagnostics. A live session is never canceled or recovered by this diagnostic.
There is no separate raw child-stderr artifact.
Fixture repositories and Core's data directory are temporary and are deleted by
Go test cleanup; the retained report is not a retained database. Do not point
this gate at an existing Core or repositories: there is no such option.

The test is opt-in and skipped by ordinary `go test`. The `--race` variant
instruments this composed Core, MCP adapters and the subprocess test executable;
it is not the driver-only race mode of the separate production-binary simulator.
`--sequential` selects a separate anomaly scenario, with a fresh Core, real
verification checkpoint and its own evidence directory. It does not append work
to the original continuation gate or extend its time budgets. It may be combined
with `--race`; that combination is a distinct check, not implied by either
ordinary sequential or original composed-race results. `scenario.txt` and the
report's `scenario` field identify which lifecycle was exercised.

## Exact trust boundary

- **Real:** Core SQLite transactions and migrations, typed business producers,
  frozen task definitions and worksets, runtime selection issuance/binding,
  coordinator/supervisor, actual Git worktrees, actual file writes and shell
  checks, Codex protocol ingestion, production GPT and Local MCP HTTP adapters,
  confirmation and run-analysis handlers, task holds and durable replay.
- **Explicitly trusted simulation:** the existing `_test.go` platform dependency
  (`TEST_ONLY_PROTOCOL_FIXTURE_V1`, collector
  `test-only-substitute-not-native-collector`) and deterministic protocol Agent.
  Its inventory is registered with typed store APIs. This deliberately replaces
  artifact measurement/qualification at dependency composition, **not** through
  a production request or hidden CLI flag. It is not evidence that a script
  satisfies native qualification. Production binaries are unchanged.
- **Simulated upstream:** deterministic engineering review and GPT analysis;
  confirmation calls are separately labeled `fixture_user`. These are explicit
  fixture decisions, not actual user acceptance of project work.
- **Bootstrap:** existing real typed project/session/confirmation/coordinator
  services admit the initial UPPER Review. The shared database template contains
  schema only. Bootstrap is not claimed to have crossed HTTP. Afterwards all
  business writes use the real MCP HTTP adapters; a few read-only Core API
  observations and supervisor joins support independent oracles. No raw SQL
  mutation, result injection, fake approval row or fault trigger is used.

The Agent launcher can only exec the exact current Go test executable and named
helper. It uses `env -i`, a private HOME/CODEX_HOME and fixed fixture switches;
there is no real executable/model fallback. Ambient real-fixture switches and
credentials and Git environment redirection/config/template inputs are cleared
before bootstrap; telemetry is disabled. Dynamic tool requests are denied.
The subprocess is **trusted test code running with host process permissions**;
this is not a hostile-agent sandbox test or proof that OS isolation is enforced.

## Bounded lifecycle

1. Actual UPPER reads ground the two-member Coding proposal. Explicit fixture
   user approval freezes `CODING_AGENT` + `RUN`, without waiver or implicit start.
2. Local MCP receives the exact public `executionStartTarget` and explicit
   inventory-backed runtime/backend selection. Real Core creates the Execution
   and implementation Run. The deterministic Agent actually writes each owned
   member's `same.txt`; source branches and unrelated backend stay unchanged.
3. Core starts a real verification Run. The Agent actually executes
   `test -f same.txt` once per exact member and emits protocol command evidence.
   The deliberately narrow criterion is only that both files exist; this is not
   a claim of substantive model correctness. Independent file reads additionally
   check the expected `coded app` / `coded sdk` bytes and unchanged Git HEADs.
4. Terminal Run success alone leaves analysis required. Simulated GPT reads the
   exact immutable observations and source hash, checks member provenance and
   actual exit codes, and explicitly submits indexed analysis via MCP. The
   Task reaches `ACTIVE / ACCEPTANCE_PENDING`, not accepted or committed.
5. Stale task-action generation and blank defer note are rejected without
   changing the checkpoint. Preparing a valid choice has no hold effect. An
   unavailable variant produces an unconsumed rejection; corrected selection
   uses a **new** request ID, not a changed payload under the rejected ID.
6. Explicit `DEFER_TASK` records a hold bound to the same Task, Execution and
   revision. Exact replay and cold Core reopen preserve it. Completion while
   held is refused. Explicit `CONTINUE_TASK` clears the hold and records the
   continuation checkpoint; it does not dispatch another Run or successor.
7. Old DEFER replay after CONTINUE cannot recreate the hold. Changed consumed
   input conflicts. Warm/cold exact receipts and original analysis remain
   immutable. Full owned worktree file/HEAD snapshots, exact three-Run inventory,
   original verification specification and unaccepted/uncommitted state remain
   unchanged throughout the choices.

During asynchronous execution, workspace partitions are explicitly non-atomic;
the bounded wait may observe an unavailable/stale Task view and rereads it.
This is read polling only. Transport errors are failures. The one explicit non-success wire branch is a
committed start reservation with exact original Task/Execution/Run/revision/workset
identity and current-epoch durable `HANDOFF_IN_PROGRESS / PREPARING`. This means
admission only: the gate waits for that same Run, never retries start. Unknown,
foreign, missing-record and mismatched receipts fail closed (also unit-tested).
Business writes are never silently retried with a new request ID.

## Sequential anomaly scenario

After the same real implementation/verification and explicit indexed analysis,
the separate scenario checks the following through the public MCP wire:

- Exact pending prepare replay returns the original unconsumed choice. A changed
  prepare payload conflicts, and a competing choice is refused with
  `TASK_ACTION_PENDING`.
- Wrong frozen hash, version or purpose cannot read, discard or consume the
  original intent; exact readback remains pending without a submission.
- Explicit fixture-user discard makes that pending intent `SUPERSEDED`, with no
  hold or continuation effect. Its old card and old task-action generation are
  refused; a fresh current-generation prepare creates a different intent.
- Runtime selection on a choice-only operation is rejected with a durable
  `REJECTED_NOT_CONSUMED` receipt. Exact replay preserves that refusal; changing
  the selection under the rejected request ID conflicts. Only the documented
  edit-selection path with a new request ID consumes the original pending choice.
- Discard after consumed `DEFER_TASK` or `CONTINUE_TASK` cannot undo either choice.
  Cold reopen preserves their exact receipts and the prior rejected request's
  immutable identity and non-consumption result. Its *current retry projection*
  correctly closes after consumption; it is not immutable retry authority.

The exact three-Run inventory, frozen definition, original verification evidence,
all owned worktree bytes/HEADs and unaccepted/uncommitted state are checked
throughout. These are serial commands and exact replay, not injected response
loss, native runtime qualification, production-data testing or real-user consent.

## Instrumentation budget and retained failures

This composed **functional** gate explicitly configures a bounded 60-second MCP
request deadline; production adapter defaults remain 15 seconds. The pipeline
observation wait is bounded at 90 seconds, post-bootstrap test context at five
minutes, and overall script test process at eight minutes. These budgets are
recorded in the report/source. Call transcripts
include start/end timestamps and duration. A prior exact-source composed race run
failed with HTTP 503 on the first workspace read about 15.3 seconds after start
admission, with no race-detector warning. The handler timeout is the supported
inference from source and timing; the SDK did not retain the response body, so it
is not a proven root-cause trace. That default-deadline failure remains evidence,
not a successful run or a fixed Core performance defect. Increasing this test
composition budget does not establish production latency/SLA or change any
business authority; transport errors still fail and no write retry is added.

The longer request budget did **not** make the composed race gate green. A
subsequent run exhausted the unchanged 90-second pipeline wait: every HTTP read
succeeded, but the original implementation Run remained `RUNNING / UNKNOWN`
with recovery required and no verification Run. Its helper request log recorded
one turn; outgoing notifications and live supervisor failure details were not
captured in that older run. The cause remains unresolved, not a proven Core
defect and not permission to invent a terminal result or issue recovery.

One bounded diagnostic race run with the added logging did complete both Agent
turns, indexed analysis, DEFER/CONTINUE and two cold reopens. It then exhausted
the existing five-minute post-bootstrap context on a final workspace read
(393 successful checks, overall **FAILED**). Both outgoing `turn/completed`
writes were recorded without write errors. This is a non-reproduction of the
earlier pipeline stall, **not** a fix: logging can alter scheduling. No race
detector warning appeared in these runs, but that does not turn failed test
processes into passes. Ordinary functional validation is reported separately;
composed-race completion and default-deadline performance remain unqualified.
The budgets were not increased again and no transport or business-write retry
was added to conceal these failures.

### 2026-10-31 reproduction and anomaly coverage

On a fresh Linux workspace using Go 1.26.10, the exact `eb173e0` functional
baseline passed 435 checks in 28.22 seconds. Its composed-race binary instead
failed after 255.04 seconds (exit 1, 30 checks), at the unchanged pipeline
observation deadline. The retained evidence now distinguishes a completed Agent
Turn from failed Core persistence:

1. The helper recorded its outgoing `turn/completed`, and Core independently
   logged `codex.turn.completed` for that same implementation Turn.
2. Process-exit telemetry, Agent finalization and the recovery-marker write then
   reported `database is locked (517)` (`SQLITE_BUSY_SNAPSHOT`).
3. The captured supervisor join reported an infrastructure failure at **Agent
   finalization intent**. Its session had exited, but the durable implementation
   Run remained `RUNNING / UNKNOWN`; no verification Run had been created.

This is a concrete post-completion storage failure in the new reproduction,
not a missing outgoing Agent notification, a live Agent wait, or proof that all
older failures had the same cause. No race-detector warning appeared. Read-only
diagnostics did not retry, cancel, recover or manufacture a result. The race
process and its report remain **FAILED**. Brief concurrent functional work
overlapped schema bootstrap only, before the pipeline budget; these timings are
not a production performance comparison. Root-cause repair remains separate
from adding anomaly coverage.

The new ordinary sequential-anomaly scenario passed 307 checks in 16.56 seconds,
plus all 13 exact PREPARING-receipt rejection cases, through the documented
script with unchanged recorded source hashes. Two earlier anomaly iterations
failed honestly and were corrected in the fixture: an internal runtime field
was not part of the public wire schema, and a historical rejection's current
retry projection was incorrectly assumed to be immutable after consumption.
Neither correction changes Core authority or converts an earlier failed process
into a pass. Sequential race coverage and production-default-deadline latency
are not established by this ordinary pass.

### Canceled SQLite reader regression

The storage investigation reduced the failure signature to a model-free driver
test: cancel a wide `SELECT`, return its connection to `database/sql`, commit a
write on a sibling connection, then begin a fresh transaction and execute its
first `UPDATE ... RETURNING`. With the old `modernc.org/sqlite` v1.39.1, this
failed with error 517 even though the new transaction had performed no preceding
application read. The retained regression test is
`TestSQLiteCanceledReadReleasesPooledSnapshot`; it varies cancellation timing,
so it is a bounded stress test rather than a deterministic scheduling claim.

The driver could drop a returned reader during cancellation without closing it,
retaining a stale WAL snapshot. The [upstream cancellation fix](https://gitlab.com/cznic/sqlite/-/merge_requests/81)
closes that reader before returning the context error. Version v1.40.1 contains
the fix and is now pinned; no busy-timeout increase, business-command retry,
fencing relaxation or invented completion is part of the repair.

The exact repository regression failed on an explicit v1.39.1 modfile at
iteration 2 with error 517 and successful rollback. On v1.40.1 it passed ten
times (1,000 fresh writer acquisitions). A separate minimal stress comparison
also completed 3,000 pinned-connection and 500 pooled-connection iterations on
v1.40.1 without subsequent writer errors. This establishes the driver defect and
its repair independently; composed workflow and aggregate suite results must
still be reported separately, rather than inferred from the narrow regression.

With v1.40.1, the original composed-race script subsequently **passed** all 434
checks in 405.82 seconds (exit 0), including both real terminal Runs, indexed
analysis, DEFER/CONTINUE and both cold reopens. The receipt oracle's 13 cases
also passed; source and dependency hashes remained unchanged. This qualifies
that bounded run, not the former failed runs, all possible concurrency schedules,
or production-default handler latency. It supports the storage repair while the
exact canceled SQL statement in the earlier composed failure remains untraced.

The separate sequential composed-race script then **passed** 306 checks in
335.92 seconds (exit 0), plus all 13 receipt-oracle cases, with unchanged source
and dependency hashes. Check counts can differ by one when start admission
returns the explicitly qualified `PREPARING` reservation instead of synchronous
success; the business lifecycle and assertions are unchanged.

Additional validation passed: the focused storage fencing/atomic finalization/
restart suite, repeated canceled-reader regression under single- and multi-P
invocations (including `-race`), root `go vet ./...`, Desktop `go test ./...` and
`go vet ./...` after a real frontend build, and JavaScript tests (621 passed,
four skipped). The regression explicitly provisions and restores at least two
scheduler Ps so that a single-P caller still exercises concurrent cancellation;
it does not skip the coverage assertion. Independent final review approved the
revised test and minimal dependency change.

The root `go test ./...` attempt is **not green**: this execution environment
refuses local Unix-socket creation, so the existing App Server socket fixtures
fail with `socket: operation not permitted`. No test was skipped or sandbox
restriction bypassed to conceal that limit. Those aggregate failures remain
separate from the passed model-free continuation gates.

That aggregate invocation ultimately exited 1. The same socket restriction also
prevented recovery protocol helpers from starting (exit 72), and the control API
package exhausted its default ten-minute total test budget. In addition, the
aggregate build began before the regression's final `runtime` import edit and
later reported a missing runtime import configuration; it is not a frozen-source
aggregate pass. A fresh, separate full SQLite-package run on the final frozen
commit compiled successfully and **passed all 548 top-level tests in 373.354
seconds** (exit 0). It resolves the storage-package verification gap without
relabeling the earlier aggregate failure or its uncompleted control API tests.

## Still not covered

Real models/native artifacts, production host sandboxing, native conversation
resume/successor, interrupted execution recovery, journal UNKNOWN/PENDING,
actual response-loss injection, hostile runtime behavior, and terminal
acceptance/delivery are outside this bounded gate. Ordinary exact replay is not
called response-loss testing. The separate production-binary simulator retains
its own explicit NONE/NOT_RUN waiver boundaries and previous limitations.
