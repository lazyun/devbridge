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

## Still not covered

Real models/native artifacts, production host sandboxing, native conversation
resume/successor, interrupted execution recovery, journal UNKNOWN/PENDING,
actual response-loss injection, hostile runtime behavior, and terminal
acceptance/delivery are outside this bounded gate. Ordinary exact replay is not
called response-loss testing. The separate production-binary simulator retains
its own explicit NONE/NOT_RUN waiver boundaries and previous limitations.
