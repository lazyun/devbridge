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
./scripts/test-simulated-continuation.sh --response-loss
./scripts/test-simulated-continuation.sh --cross-session
./scripts/test-simulated-continuation.sh --journal-unknown
./scripts/test-simulated-continuation.sh --journal-recovery
./scripts/test-simulated-continuation.sh --continue-unknown
./scripts/test-simulated-continuation.sh --submission-withdraw
./scripts/test-simulated-continuation.sh --withdraw-arbitration
./scripts/test-simulated-continuation.sh --cancel-late-completion
./scripts/test-simulated-continuation.sh --process-crash
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
`--response-loss`, `--cross-session`, `--journal-unknown`,
`--journal-recovery`, `--continue-unknown`, `--submission-withdraw` and
`--withdraw-arbitration`, `--cancel-late-completion` and `--process-crash` select further
independent lifecycles. All scenario selectors are mutually exclusive and each
combines with `--race`. All eleven scenarios retain the same request, pipeline,
context and process budgets.

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
  observations and supervisor joins support independent oracles. No business row
  is seeded or manually rewritten, and no result or approval is fabricated. Only
  `--response-loss` substitutes a client response-body reader after the real
  handler completes. Only `--journal-unknown`, `--journal-recovery`,
  `--continue-unknown` and `--submission-withdraw` install
  the precisely scoped, test-only SQL abort triggers described below in their
  disposable databases; they also use read-only SQL snapshots to distinguish
  persisted journal state from public readback. `--withdraw-arbitration` instead
  installs a non-failing exact writer barrier plus an observation-only service
  wrapper that forwards unchanged to the same real Facade. It also uses read-only
  SQL snapshots. `--cancel-late-completion` uses a similar non-failing barrier
  at the final journal write after the business commit, an observation-only
  service wrapper, and actual public caller cancellation. `--process-crash` uses
  a precisely scoped final-journal barrier in a separate Core-hosting process,
  actual parent-issued SIGKILL, and read-only post-death SQL observations before
  opening another Core. Other scenarios install no storage fault or writer barrier.

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

## Response-loss readback scenario

The original and sequential scenarios already cover exact replay, stale cards,
request conflicts and consumed discard. This separate scenario adds the missing
**post-handler / pre-client response-body loss**, not a second replay-only test:

- After real implementation, verification and indexed analysis, an explicit
  fixture-user `DEFER_TASK` commit traverses the production HTTP handler exactly
  once. A dedicated test client drains/closes the real bounded JSON response and
  validates the matching JSON-RPC ID and exact consumed original request. Only
  then does its substitute response reader deliver a distinctive transport
  error, with **zero response-body bytes** to the SDK. Genuine headers remain.
- The client must receive that exact error and no usable result. Synchronized
  forwarding counters require exactly one matching business POST, even if an SDK
  retry were attempted. Unexpected or repeated business calls fail closed before
  forwarding; protocol initialization/cleanup pass unchanged. `MaxRetries=-1`
  is retained but is not used as proof of no resend.
- A deliberately never-submitted request is probed by pure read only. Its
  `UNKNOWN / recorded=false` result grants neither non-consumption evidence nor
  retry authority, even though the same intent contains a different consumed
  request. Exact `confirmation_get` using the **pre-dispatch** original identity
  and request discovers the actual DEFER receipt and hold, without commit replay.
- A cold Core reopen preserves that exact receipt. Then explicit fixture-user
  `CONTINUE_TASK` loses its response in the same way; Core is cold-reopened before
  its first readback. Only the original receipt establishes consumption and the
  cleared hold. Re-reading old DEFER history cannot recreate the hold.

Both losses retain `lost-response-<requestId>.json` with the genuine intercepted
request/response and counters as **oracle-only evidence**, never as recovery
input. The ordinary `mcp.jsonl` client record contains only the injected error and
no result. Recovery uses the same real business Session/intent/request, not a new
Session. The short-lived faulted MCP transport is closed; reads use the ordinary
GPT transport (reconnected to the same Core data after cold reopen).

The same exact-three-Run, frozen definition/evidence, owned-file/HEAD and
unaccepted/uncommitted oracles run throughout. No Core outcome is fabricated, no
business command is retried, and no unknown durable journal record is inserted.
This establishes recovery from this precise response-stream loss point only,
not arbitrary crash timing, journal `UNKNOWN` reconciliation, cross-business-
Session authorization, native conversation resume, or production latency.

On 2026-10-31, the ordinary response-loss script **passed 211 checks in 19.52
seconds**, and its separate composed `--race` invocation **passed 210 checks in
307.86 seconds** (both exit 0), plus all 13 PREPARING-receipt oracle cases. All
six recorded source hashes were unchanged. Each dropped response had exactly
one forwarded commit, one failed body read and a closed body; no usable commit
result reached the SDK. Root `go vet -p 2 ./...` also passed. These are scoped
response-loss results, not a new full-root-suite or production-latency claim; the
aggregate limitations recorded below remain in force.

## Cross-business-Session identity scenario

`--cross-session` creates a distinct real Session B via public `session_initialize`,
then uses `project_setup_prepare` and explicit fixture-user confirmation to bind
B to the existing fixture Project. Both A and B have the same established
`data-instance-local` caller scope. A successful B `workspace_get` proves it can
read the same Project/Task/Execution; foreign-intent refusal is not merely an
invalid/unbound-Session or wrong-Project rejection.

The scenario then checks the Session component of exact confirmation identity:

- B cannot read options/receipt, discard, or commit A's pending DEFER intent.
  Each probe must return the exact typed `CONFIRMATION_FACTS_UNAVAILABLE` failure
  with no intent, options, display or submission. A retains its original pending
  content, action ownership/generation and unchanged verification checkpoint.
- B's legitimate project-binding commit deliberately uses the same request-ID
  string as A's later legitimate DEFER. Both independently succeed and retain
  distinct payload digests/receipts in their own Sessions. Foreign probes use
  both the copied original request ID and a fresh B request ID; refusal must not
  depend only on a request-key collision.
- After A consumes DEFER, the same foreign probes cannot borrow its historical
  receipt or undo the hold. A also cannot read B's project-binding intent. Each
  Session can still read its own original receipt with unchanged contents.
- A cold Core reopen preserves the two exact persisted Session IDs and Project
  bindings. B again reads the shared Task but still cannot operate A's DEFER.
  A's explicit CONTINUE then clears the hold; foreign probes cannot change that
  receipt, Task action generation, files, or the exact three-Run inventory.

There is no Session-ID swap in shared fixture state, implicit Session creation,
raw SQL write, storage fault or response loss. Beyond the shared genuine
execution/verification pipeline, these probes add no engineering acceptance or
Run. Session B is created and bound through genuine public business handlers; the
frozen initially-unbound project confirmation is distinct from its successful
`BIND_PROJECT` effect and current Session binding. `cross-sessions.json` records
the exact test identities and the same-caller claim boundary.

This is **Session-scoped intent identity isolation**, not a claim that a known
Session ID authenticates a different principal. Cross-principal/token-boundary
security, alternate adapters, journal UNKNOWN reconciliation and arbitrary
crashes remain outside this scenario.

On 2026-10-31, the ordinary cross-Session script **passed 320 checks in 16.31
seconds**, and the separate composed `--race` invocation **passed 319 checks in
299.28 seconds** (both exit 0), plus all 13 PREPARING-receipt cases. Both runs
recorded all 21 foreign-identity refusals as failure-only envelopes, and all
seven source hashes remained unchanged. Root `go vet -p 2 ./...` passed.

Two earlier authoring attempts remain failed evidence: a fixture DTO field did
not compile, and a subsequent run incorrectly expected the immutable prepared
ProjectID to change after binding. The corrected oracle preserves that frozen
identity and checks the real `BIND_PROJECT` step plus public current-Session
readback. These were test-only corrections, not Core changes or retrospective
passes. The full-root-suite limitations below are not cleared by this scenario.

## Durable journal UNKNOWN after an injected rollback

`--journal-unknown` uses the same real admitted implementation/verification Runs
and indexed analysis, then explicitly confirms one exact DEFER choice. The
controlled fault is **inside the real business transaction**, not a fabricated
outcome or lost HTTP response:

- A regular SQLite `BEFORE INSERT` trigger on `task_hold_receipts` applies only to
  the exact actor, Task, Execution, revision and DEFER action. Its subrequest ID
  must match the original confirmation's **already durable** UNKNOWN step while
  that confirmation is CONFIRMING with the original outer request ID. It cannot
  apply before Core journals its own admitted command.
- The trigger calls a test-only non-deterministic scalar that records those six
  primitive identities in a synchronized in-memory witness and returns a unique
  injected error. The callback never touches the DB, writes business records or
  calls test assertions. This proves the exact fault point despite the public
  adapter's sanitized error. A TEMP trigger would not reliably reach a pooled
  writer and is deliberately not used.
- Core's normal transaction had attempted the hold before inserting its receipt;
  the injected error causes the normal rollback. Core itself then records the
  confirmation and step as `UNKNOWN`. Read-only SQL independently requires zero
  holds/choice receipts and exactly one consumed original confirmation request.
  The fixture never inserts an UNKNOWN row, edits authority, or disables a fence.
- The trigger is removed and its absence checked before recovery observations.
  Exact original receipt reads, options, consumed discard and a refused competing
  prepare must not invent a successful effect, new intent or retry authority.
  Cold Core reopen preserves the raw journal/request bytes, original Task action
  owner/generation, zero canonical choice receipts and `canResume=false`.
  No `confirmation_commit` is retried with either the original or a new request.

The two outcome layers are intentionally distinct: submission
`outcome=CONSUMED` / `businessOutcome=COMMITTED` freezes the original request;
**intent/step UNKNOWN does not mean the DEFER succeeded**. Lack of a hold is not
non-consumption evidence and cannot authorize another business command. The
original real Runs, verification evidence, file bytes/HEADs and unaccepted Task
remain unchanged. The test ends with the unresolved confirmation still fenced.

`journal-fault.json` retains the exact trigger SQL/error marker;
`journal-fault-witness.json` must contain exactly one matching invocation.
`journal-unknown-before.json` and `journal-unknown-after.json` contain independent
read-only persisted snapshots and must be identical across the observations and
cold reopen. The public calls remain in `mcp.jsonl`.

The scalar is registered once, before the isolated scenario opens fixture DB
connections, and only when this opt-in test runs. Per-test witness tokens are
cleaned up. Run this scenario through the dedicated script, not as part of an
arbitrary concurrent opt-in test selection: modernc function registration is
process-global and affects future connections. No production fault switch or
runtime behavior is added.

This establishes **conservative durable UNKNOWN after a known injected rollback**.
It does not simulate an uncertain transaction commit, process crash, disk loss,
or positive recovery from a surviving canonical choice receipt. Those remain
separate cases, not inferred successes.

On 2026-10-31, the ordinary journal-UNKNOWN script **passed 175 checks in
12.54 seconds**, and its separate composed `--race` invocation **passed 174
checks in 244.34 seconds** (both exit 0), plus all 13 PREPARING-receipt cases.
An independent isolated rerun passed 175 checks in 12.44 seconds. All eight
source hashes were unchanged, each run observed exactly one matching fault
witness, and the raw before/after snapshots were identical. Root `go vet -p 2
./...` passed. The intentional UNKNOWN is still unresolved at test end: these
passes qualify conservative fencing, not successful DEFER completion or general
recovery. The aggregate-suite limitations below remain in force.

## Canonical choice recovery after a final journal-save failure

`--journal-recovery` distinguishes **a committed business choice with a failed
final confirmation-journal save** from the previous scenario's rolled-back
business transaction. It uses the same genuine admitted Runs and verification
checkpoint, with no fabricated authority or result rows:

- An ordinary `BEFORE UPDATE OF intent_json` trigger matches only the exact
  Session/intent/outer commit request/action, the durable `CONFIRMING` intent
  with its `UNKNOWN` business step, and the attempted `COMPLETED` replacement
  with the same exact `RECORD_TASK_CHOICE` subrequest. It also requires the
  matching canonical `task_hold_receipts` row to exist already.
- A test-only scalar records the exact identities and old/attempted journal
  documents in a synchronized memory witness, then returns a unique error.
  It performs no database writes, re-entry or assertions. Each original DEFER
  and CONTINUE commit must reach this exact final-save fault once, returning
  `CONFIRMATION_FACTS_UNAVAILABLE` at `RECEIPT_READBACK`, not observed completion.
- Before any recovery-capable public read, independent read-only SQL captures
  the raw `CONFIRMING / UNKNOWN` journal, original consumed request, real choice
  receipt, hold, event history and action owner/generation. The receipt's actor,
  subrequest, Task/Execution/revision/action and SHA-256 of the frozen business
  payload are verified independently. DEFER really holds; CONTINUE really clears.
- After the trigger is removed and its absence checked, original-identity
  `confirmation_get` and options reconstruct `COMPLETED / COMMAND_COMPLETED`
  solely from that canonical receipt. The consumed submission stays frozen, with
  no reprepare, redispatch or resume authority. Completed options retain the
  existing fresh-prepare hint (`reprepareAllowed=true`), but original variants
  remain unavailable with `INTENT_ALREADY_CONSUMED`, retry `NONE` and routing
  to the original receipt. That hint is not authority to retry a consumed request.
  DEFER is read warm and again
  after cold reopen; CONTINUE is cold-reopened **before its first recovery read**.
- Raw snapshots before and after each pure-read window must be byte-identical:
  projected completion is **not** a claim that readback repaired the journal.
  The public Task action node may project its next generation while raw owner
  and generation remain unchanged. A separately labeled normal new CONTINUE
  prepare, only after DEFER is proven, may persist prior reconciliation and
  advance ownership. It is not a retry of the unresolved command.
- Old DEFER history after CONTINUE cannot recreate the hold. There is only one
  commit call per choice, no commit retry under any request ID, exactly two
  canonical choice receipts/events, and still only the original three Runs.
  Frozen evidence, file bytes/HEADs and unaccepted/uncommitted state are checked.

`journal-recovery-fault-<ACTION>.json` records the exact fault and scope;
`journal-recovery-witness.json` contains the two actual final-save attempts.
`journal-recovery-before-<ACTION>.json` and `journal-recovery-after-<ACTION>.json`
retain the independent raw snapshots. The public calls remain in `mcp.jsonl`.
The scalar has the same isolated-process registration limitation as the
journal-UNKNOWN scenario: use the dedicated script, not arbitrary concurrent
opt-in selections. No production fault switch or behavior is added.

This establishes read-only reconciliation from genuine canonical choice
receipts at this exact final-save abort. It is not an uncertain business commit,
process crash, disk failure, transport response loss or native runtime recovery.

On 2026-10-31, the ordinary journal-recovery script **passed 237 checks in
14.60 seconds**, and its separate composed `--race` invocation **passed 236
checks in 304.78 seconds** (both exit 0), plus all 13 PREPARING-receipt cases.
All nine recorded source hashes remained unchanged. Both runs observed exactly
two final-save witnesses, three distinct Core epochs and unchanged raw snapshot
pairs. An independent isolated rerun passed 237 checks in 16.69 seconds;
independent review approved the exact fault and raw/public state distinction.
Root `go vet -p 2 ./...` passed. These are scoped recovery passes, not a
full-root-suite or latency claim.

Two earlier authoring attempts remain failures: a duplicate test-helper name
prevented compilation, and an overly strict options oracle rejected the existing
completed-intent fresh-prepare hint. The corrected oracle distinguishes that
hint from the consumed request's immutable no-retry receipt; no Core behavior
was changed, and neither prior process is relabeled as a pass.

## CONTINUE rollback preserves the genuine existing hold

`--continue-unknown` starts with an actual successful fixture-user DEFER through
the public MCP handler. Its hold, canonical receipt and choice event are real
Core outputs, not seeded preconditions. Before preparing CONTINUE, a read-only
snapshot freezes the exact hold (including note and original `heldAt`), every
canonical receipt/event field, and the completed DEFER journal/request.

The subsequent explicit CONTINUE commit uses the same precisely scoped
canonical-receipt abort seam as `--journal-unknown`, now bound to the original
CONTINUE action and subrequest. An additional trigger predicate requires the
hold to be **absent inside that business transaction** before the witness fires.
Together with the existing baseline this proves the real hold deletion occurred;
the normal transaction rollback, not a replacement hold insert, must restore it.
The witness records exactly one original CONTINUE fault. The earlier successful
DEFER never enters the installed fault.

Core conservatively records the consumed CONTINUE intent and step as `UNKNOWN`.
The trigger is dropped before recovery observations. Warm/cold original receipt
reads, options, consumed discard and a refused fresh DEFER must preserve:

- exactly the original hold, DEFER canonical receipt/event and completed DEFER
  request, byte-for-byte, with **no CONTINUE canonical receipt**;
- the original consumed CONTINUE request, raw UNKNOWN journal and blocking Task
  action owner/generation, with no resume, redispatch or reprepare authority;
- held Task state, `continuationConfirmed=false`, no completion authority, the
  same three real Runs, unchanged evidence/files/HEADs and unaccepted/uncommitted
  engineering state.

Reading successful DEFER history cannot settle the failed CONTINUE. No DEFER or
CONTINUE commit is replayed, and no new choice commit follows UNKNOWN.
`continue-unknown-business-before.json` and `-after.json` contain the original
business snapshots; `continue-unknown-journal-before.json` and `-after.json`
separately cover the post-fault UNKNOWN journal and action ownership. Both pairs
must match. `journal-fault.json` and `journal-fault-witness.json` retain the exact
scoped fault and its single invocation, alongside the public `mcp.jsonl`.

This is a known injected CONTINUE rollback with a surviving genuine hold, not an
uncertain commit, crash or successfully recovered CONTINUE. The same opt-in,
isolated-script scalar-registration restriction applies. Production code and
all timing budgets remain unchanged. The original DEFER-rollback scenario keeps
its separate zero-hold/zero-receipt assertions after the narrow helper extraction.

On 2026-10-31, the ordinary CONTINUE-UNKNOWN script **passed 238 checks in
14.46 seconds**, and the separate composed `--race` invocation **passed 237
checks in 288.05 seconds** (both exit 0), plus all 13 PREPARING-receipt cases.
All ten source hashes remained unchanged. Each run preserved both exact raw
snapshot pairs and witnessed one original CONTINUE fault. The refactored original
DEFER-UNKNOWN scenario separately passed 175 checks in 12.47 seconds, retaining
its zero-hold/zero-canonical-receipt result. Independent isolated reruns passed
238 checks in 13.93 seconds (CONTINUE) and 175 in 13.25 seconds (DEFER), with
unchanged hashes and exact snapshot pairs. Independent review and root
`go vet -p 2 ./...` passed. These are bounded safety passes; CONTINUE remains
unresolved and the earlier aggregate-suite limits still apply.

## Explicit submission withdrawal: unobserved versus consumed

`--submission-withdraw` exercises the production public
`confirmation_submission_withdraw` entry, not a locally inferred cancellation:

1. Prepare a real DEFER choice after the genuine verification checkpoint. Read
   one deliberately **never-submitted** request identity. It must remain
   `UNKNOWN / recorded=false`, without non-consumption proof or retry authority;
   independent SQL snapshots require that this read wrote no ledger or effect.
   This is a controlled missing-request probe, **not transport response loss**.
2. An explicit fixture-user withdrawal supplies the complete original payload.
   Core records `REJECTED_NOT_CONSUMED / CONFIRMATION_SUBMISSION_WITHDRAWN` for
   that exact request/hash while the intent remains pending and effect-free.
   Its durable failure stage is `SUBMISSION_WITHDRAWAL`; the existing closed
   public vocabulary projects `RECEIPT_READBACK` without rewriting that row.
   Repeated withdrawal is idempotent. After cold reopen, an intentional late
   original commit is refused by that already-proven durable fence. A
   schema-valid changed payload under the same request conflicts. Raw intent,
   every Session request row, hold/receipt/event set and owner remain unchanged.
3. Only after that durable rejection, a separate explicit fixture-user
   confirmation with a **new request ID** consumes the original pending DEFER.
   Withdrawal of this genuinely consumed request must preserve its completed
   receipt and actual hold. Withdrawal of the older rejected request returns
   its own immutable non-consumption facts, not the newer consumed receipt;
   its current retry projection closes because the intent is now consumed.
4. Prepare CONTINUE and inject the already-qualified exact receipt-insert abort,
   after its transaction deletes the genuine hold. The rollback preserves the
   original hold, and Core records the consumed CONTINUE as UNKNOWN. With the
   trigger removed, warm and cold withdrawals of that exact consumed request
   must only read back UNKNOWN: no new rejection, retry permission, owner release,
   continuation confirmation or hold deletion. No consumed/UNKNOWN commit is
   ever replayed.

`submission-withdraw-sealed-before.json` and `-after.json` prove the durable
withdrawal fence survives the late-copy/conflict probes. The `-consumed.json`
snapshot records both distinct DEFER request receipts. The
`submission-withdraw-unknown-before.json` and `-after.json` pair must remain
identical across warm/cold withdrawal; it includes every request in the fixture
Session so older receipt history cannot silently disappear. Exact original
DEFER business rows are additionally checked with the full existing hold/
canonical-receipt/event oracle. The fault SQL and single original CONTINUE
witness use the same `journal-fault.json` / `journal-fault-witness.json` artifacts.

The same three real Runs, frozen evidence, file bytes/HEADs and unaccepted/
uncommitted state remain intact. No business authority or conflict metadata is
seeded. This is **sequential withdrawal/fencing**, not a competing withdrawal-
versus-commit race, real response loss or legacy-conflict migration qualification.
The `--race` option instruments this same serial scenario. Its shared test scalar
retains the isolated-script registration restriction; production behavior and
all timing budgets are unchanged.

On 2026-10-31, the ordinary withdrawal script **passed 225 checks in 14.89
seconds**, and the separate composed `--race` invocation **passed 224 checks in
267.17 seconds** (both exit 0), plus all 13 PREPARING-receipt cases. All eleven
source hashes were unchanged, both raw snapshot pairs matched, and one original
CONTINUE fault was witnessed across three real Core epochs. Root
`go vet -p 2 ./...` passed. An independent isolated rerun passed 225 checks in
15.04 seconds; independent review verified the wire order, immutable receipts
and stage distinction. These results do not clear the aggregate limits below.

An earlier authoring run remains failed: its oracle incorrectly expected the
internal `SUBMISSION_WITHDRAWAL` stage on the public wire. The corrected test
asserts the existing normalized public stage **and** the untouched durable
internal stage. This is a test-only correction, not a Core fix or a relabeled
pass for that earlier process.

## Overlapping withdrawal/commit arbitration with known first writers

`--withdraw-arbitration` adds actual overlapping public calls; this property is
established by recorded barriers and invocation intervals, **not inferred from
running the race detector**. Both participants use separately initialized MCP
clients and the same production HTTP adapter/real Core:

- A thin test-only wrapper observes the exact `CommitConfirmation` and
  `WithdrawConfirmationSubmission` service entries, then synchronously forwards
  each input unchanged to the production service and same Facade. It supplies no
  result or authority. Counters require exactly one entry per operation.
- An ordinary SQLite `BEFORE INSERT` trigger matches the exact original
  Session/intent/request, submitted payload digest, DEFER action, expected ledger
  outcome and still-pending intent. Its scalar records the actual proposed ledger
  fields and pauses the **first real writer inside its transaction**. It performs
  no DB re-entry or business-row writes and returns success only after release.
- With that writer paused, the second public request is launched. Only its exact
  service entry releases the rendezvous. The synchronized trace must show
  `first entry < writer paused < competing entry < release < writer return`;
  actual client-call intervals must overlap. This does **not** prove the second
  transaction reached its writer lock before release.
- Withdrawal-first records one genuine `REJECTED_NOT_CONSUMED` receipt. The
  overlapping original commit is refused, and there are zero holds, canonical
  choice receipts or events. Both callers and subsequent cold readback retain
  that exact winner; no callback manufactures a rejection.
- Only after this proven fence, a **new explicitly confirmed request ID** on the
  still-pending intent exercises commit-first. Core consumes it once and records
  one real DEFER hold/receipt/event. The competing withdrawal must read the same
  consumed request, possibly while its business step is still CONFIRMING; it is
  not required to predict immediate final completion. After both calls join,
  exact reads establish COMPLETED and preserve it cold. The prior rejected
  request still returns its own immutable history, not the newer consumption.

No consumed/UNKNOWN commit is retried. Both known first-writer orders are covered
in one bounded lifecycle, with original three-Run inventory, evidence/files/HEADs
and unaccepted/uncommitted engineering state unchanged. This is serialization at
this exact request-admission seam, not all possible schedules or second-lock
arrival proof.

`arbitration-plan-<ORDER>.json` records the exact trigger, inputs and scope;
`arbitration-trace-<ORDER>.json` records sequence, counters, actual ledger witness
and timeout status. `arbitration-joins-<ORDER>.json` requires two started/two
joined calls. Results are appended once per call to `mcp.jsonl` by the main test
thread, retaining original start/finish times and phase labels; trace and any
joined responses are also retained on failure. The `arbitration-withdrawal-*`
and `arbitration-consumed-*` before/after snapshot pairs must remain identical
across their cold-read windows.

Each rendezvous is bounded at two seconds, the scalar at three seconds, each
pair's caller context at ten seconds, and cleanup join at twelve seconds. A
barrier/rendezvous timeout fails the scenario and is **never** counted as a legal
winner or silently retried. Cleanup releases before cancellation/join, then drops
the trigger and closes clients/server before Core reopen. Existing shared test
and production deadlines are not increased. The same isolated-script global
scalar-registration restriction applies; no production hook is added.

On 2026-10-31, the ordinary arbitration script **passed 197 checks in 15.98
seconds**, and its composed `--race` invocation **passed 196 checks in 283.16
seconds** (both exit 0), plus all 13 PREPARING-receipt cases. Twelve source hashes
were unchanged. Each phase recorded exactly five ordered barrier events, one
entry per operation, one matching ledger witness and two joined calls; both cold
snapshot pairs matched across three real Core epochs. No barrier timeout or
race-detector warning occurred. An independent isolated rerun passed 197 checks
in 16.69 seconds, and independent review verified both order traces and exact
winning receipts. Root `go vet -p 2 ./...` passed. An earlier authoring command
failed to compile because the SDK Connect options argument was
missing; it remains failed evidence, not a passed gate. These scoped results do
not clear the full-root-suite limits below.

## Client cancellation with late final journal persistence

`--cancel-late-completion` tests a distinct boundary from response-body loss:
the real public caller cancels **before the real service returns**, while Core
is already persisting its final confirmation journal after the genuine DEFER
business transaction committed. This is one DEFER, not a new CONTINUE test,
early cancellation, transaction rollback, process crash or execution recovery.
The test does not infer non-consumption from client cancellation.

A dedicated production MCP HTTP adapter uses a thin observation-only
`StaticService` wrapper. It forwards the exact input and actual request context
to the same real Facade and supplies no business result. One pre-initialized SDK
client calls `confirmation_commit` once with the original frozen identity.
A precisely scoped ordinary `BEFORE UPDATE OF intent_json` trigger pauses only
that original Session/intent/request at the transition from `CONFIRMING` with an
`UNKNOWN` step to `COMPLETED`/`COMMAND_COMPLETED`. Its predicate requires the real
canonical choice receipt for the original business subrequest and exact targets.
The callback records actual old/new journal JSON; it never accesses the DB,
creates authority, invokes test assertions or performs another business command.

While the writer is paused, independent read-only WAL transactions capture the
actual `CONFIRMING`/`UNKNOWN` journal and genuine committed hold, canonical receipt
and event. The main test then cancels its client context. It requires a nil client
result with `errors.Is(err, context.Canceled)` **and** observation that the real
server request context was canceled, before releasing the writer. Client-side
cancellation alone is not server-cancellation evidence. The retained SDK client
error also reports `sending "notifications/cancelled": Bad Request`; this is not
a claim that the separate cancellation notification was delivered successfully.
The production adapter enables `PropagateRequestCancellation: true` on its
stateless JSON HTTP transport, and the SDK propagates HTTP carrier cancellation
into the handler context. The test relies on the
actually observed service-context cancellation, not on that notification. The recorded causal order
is service entry → writer paused → client cancel issued → observed server
cancellation and client return → writer release → writer return → service return.
The two cancellation observations need not be ordered relative to each other.

Core's existing `RecordStep(context.WithoutCancel(ctx), ...)` must persist the
actual attempted final `COMPLETED` journal despite the canceled request. This
also legitimately clears the original action owner and increments generation
once via `task_action_confirmation_settled`; the test requires that exact
transition rather than freezing a consumed action owner. Every prior submission
row, the genuine hold (including `HeldAt` and note), canonical receipt and choice
event must remain identical. The separately captured, oracle-only service return
contains `COMPLETED` plus the real `CONFIRMATION_REQUEST_INTERRUPTED` /
`RECEIPT_READBACK` failure: its receipt-readback context was canceled. That is
**not** a successfully delivered client response or a non-consumption receipt.

After both caller and service have joined, the dedicated endpoint closes and the
trigger is removed. A separate unaffected public client uses only the original
identity frozen before dispatch, never the captured server result, to read the
consumed completed receipt. Warm and cold reads must preserve exact raw completed
journal and immutable business bytes, without repair, resend or new request.
The existing checkpoint oracle preserves all three original Runs, worksets,
analysis, file bytes/source HEADs and still-unaccepted/UNCOMMITTED Task state.

`cancel-late-plan.json` records the exact trigger and limits;
`cancel-late-trace.json` records events, counters, old/new journal witness and
oracle-only service output; `cancel-late-joins.json` records both joins. The
`cancel-late-before.json`, `-canonical.json`, `-after.json` and `-cold.json`
artifacts distinguish pre-release raw journal from its actual later persistence.
Exactly one canceled public commit is retained in `mcp.jsonl`, with nil result and
its real error. No `confirmation_commit` is retried.

The scalar has a strict **5-second** timeout, rendezvous waits **2 seconds**, and
the client/service phase **10 seconds**, unchanged under `--race`. Cleanup releases
before cancellation/draining, bounds joins at **12 seconds**, joins the context
observer, and retains partial trace/results on failure. Any barrier timeout,
missing propagation, wrong identity, extra forwarded call or missing join fails;
no timeout is accepted as late success. Driver scalar registration remains
process-global: only the isolated dedicated script selection is supported, not
arbitrary parallel opt-in tests. Shared request/pipeline/context/process budgets
are unchanged; this is not production-latency qualification.

On 2026-10-31, the frozen ordinary scenario **passed 151 checks in 12.72
seconds** (`/tmp/devbridge-test-only-continuation.9UPSOM`), and composed `--race`
**passed 151 checks in 247.48 seconds**
(`/tmp/devbridge-test-only-continuation.w71U5w`), both exit 0 plus all thirteen
pure receipt-oracle cases. An independent reviewer run also **passed 151 checks
in 12.84 seconds**, exit 0 (`/tmp/devbridge-test-only-continuation.ktr27Z`). All
three had one exact forwarded commit, one final-write
witness, one service return and joined context observer, no timeout, and matching
causal order/raw snapshots. No race warning was emitted. All thirteen tracked
source hashes matched the final source. Root `go vet -p 2 ./...`, opt-out skip
before registration, receipt cases, script syntax, conflicting/duplicate flags
(exit 2), and `git diff --check` passed. These are scoped results, not a whole-root
suite pass or production-default timeout qualification.

During the prior-scenario regression batch, the 4.9 GiB `/tmp` tmpfs filled
with retained generated executables. Five scripts exited 1 **at linking, before
any test ran**, with `mapping output file failed: no space left on device`:
`CvRYsG` (journal UNKNOWN), `7daEmV` (journal recovery), `Snkkh4` (CONTINUE
UNKNOWN), `T5tvEu` (withdrawal), and `4XZptE` (arbitration), all under the usual
`/tmp/devbridge-test-only-continuation.` prefix. Their original logs and failed
exit sidecars remain; no missing report is called a failed or passed test.
Nine finished generated binaries were copied to workspace-backed storage,
SHA-256 verified, and their original paths retained as hash-valid symlinks,
freeing 487,332,612 bytes without removing evidence. The relocation manifest is
`/workspace/scratch/e4799e9b8e60/devbridge-evidence-binaries/relocation-manifest.json`.
The blocked checks were rerun unchanged with workspace-backed `TMPDIR` and
`DEVBRIDGE_SIM_EVIDENCE_DIR`; no code or timeout change was made.

All nine prior ordinary selectors then had a successful final-source run
(exit 0, plus thirteen receipt-oracle cases each):

| Selector | Checks | Duration | Evidence suffix |
| --- | ---: | ---: | --- |
| `default` | 435 | 20.57s | `y4tsg0` |
| `--sequential` | 307 | 17.53s | `gKi3du` |
| `--response-loss` | 210 | 17.70s | `LCzMRb` |
| `--cross-session` | 320 | 15.98s | `CiAzDB` |
| `--journal-unknown` | 175 | 12.59s | `d6UnJS` |
| `--journal-recovery` | 237 | 16.96s | `OkAjLu` |
| `--continue-unknown` | 238 | 13.08s | `U6Tcrr` |
| `--submission-withdraw` | 225 | 16.58s | `ScxiUh` |
| `--withdraw-arbitration` | 197 | 17.30s | `pHhqtl` |

The first four suffixes are under `/tmp/devbridge-test-only-continuation.`;
the last five are under
`/workspace/scratch/e4799e9b8e60/devbridge-evidence/devbridge-test-only-continuation.`.
All thirteen source hashes matched across these nine directories and the three
new-scenario qualifications. The response-loss count differs from its preceding
211-check run only by one `workspace_get succeeds` polling observation; no
behavioral assertion was removed. The five original linker failures are still
failures, not retroactively relabeled passes. Prior root aggregate/socket and
production-latency limitations below remain unchanged.

The first authoring run (`/tmp/devbridge-test-only-continuation.moFmhr`) **failed
in 9.59 seconds** because the draft oracle incorrectly required action ownership
and generation to stay frozen after successful final persistence. Actual causal
cancellation, late `COMPLETED`, exact owner release/generation increment and the
interrupted receipt-readback failure were captured. The test expectations were
corrected against the production settlement trigger and failure mapping; no Core
behavior was changed. This failed run remains separate from final qualification.

## Actual Core process-crash scenario

`--process-crash` fills a different boundary from both the final-save SQL abort
and canceled caller scenarios: **the actual process owning Core and its SQLite
writer is killed with SIGKILL**. It covers precisely the interval after the real
canonical choice transaction commits, while the final confirmation journal
update is paused. It does not qualify arbitrary crash points, an uncertain
business transaction commit, machine/power loss, or interrupted Agent recovery.

The parent first runs the same real implementation, verification and indexed
analysis, joins both Agent supervisor sessions, and freezes the original pending
`DEFER_TASK` identity/request. It gracefully closes its own MCP connections and
Core, then launches only the exact current Go test executable's named helper.
That graceful **handoff is not the crash under test**. The helper opens the same
parent-owned disposable DataRoot in a new real Core epoch and exposes the
production GPT MCP handler. Its environment is explicitly empty except fixture
paths/switches, telemetry/Git isolation and minimal PATH; a nonexistent explicit
Codex path denies native execution even if a runtime were accidentally requested.
Persisted executable overrides must be absent. It creates no new Session or
business authority and never seeds or repairs an Execution, choice or receipt.

The parent sends the exact frozen fixture-user commit once. Both the transport
and unchanged-input service wrapper refuse unexpected or repeated commits; the
transport does not substitute response bytes. A test-only SQL trigger is scoped
to that intent/Session/action/request and the original task, Execution and
revision. Its predicate requires the real canonical `task_hold_receipts` row,
the durable `CONFIRMING / UNKNOWN` predecessor and attempted `COMPLETED` journal.
Its scalar callback copies only the old/new journal and identity, flushes a
separate event file, notifies the parent through private FD3, then blocks. It
performs no database re-entry or authority writes and cannot succeed on timeout.

Only after this exact barrier witness does the parent kill its own child. The
kernel wait status must prove `Signaled / SIGKILL`; a nonzero exit, client EOF or
cancellation alone cannot pass. The event trace must be exactly `ready`,
`service_entry`, `writer_paused`, with no callback timeout, service return or
graceful helper cleanup. The still-live caller must receive an actual transport
error and no usable result, with no caller cancellation or second business POST.
The child process, caller and pipe reader are joined before fixture cleanup.

After death, **before opening another Core**, the parent opens a `mode=ro` /
`query_only` SQLite handle and records the surviving raw WAL facts: the original
consumed submission, unfinished journal, genuine choice receipt, hold/event,
action owner and generation. The trigger must still exist, proving no graceful
cleanup removed it. Another Core opens the same DataRoot with a new epoch and
its raw choice snapshot must be unchanged. Only the exact test trigger is then
removed. Public receipt/options/workspace reads use the parent's **pre-dispatch
original identity**, never an identity chosen from oracle-only witness output.
They project completion from the surviving canonical receipt without changing
the raw journal, redispatching the choice or granting retry authority.

After the recovered DEFER is proven, a new explicit fixture-user `CONTINUE_TASK`
is prepared at the current generation and undergoes a second real Core process
crash at the same precise boundary. Its own canonical receipt and cleared hold
survive; reading the historical DEFER cannot recreate that hold. Across both
crashes the full checkpoint oracle requires the same three Runs, original
frozen definition, analysis and verification contract, all owned worktree bytes
and HEADs, and still-unaccepted/uncommitted state. Agent request/response logs
must remain byte-identical throughout each handoff/crash/reopen/read sequence.

Retained artifacts use `process-crash-<ordinal>-<action>` prefixes: `-plan.json`,
`-child.log`, `-core.jsonl`, `-child-events.jsonl`, `-trace.json`, `-death.json`,
`-before-open.json`, `-after-read.json` and `-epochs.json`. The plan records the
exact trigger and original request; death records distinguish the graceful
handoff, signaled process death and forwarding counters. Raw snapshots are
independent oracles, not recovery inputs. `mcp.jsonl` retains each failed client
call with nil result and the real transport error. The database and repository
fixtures remain temporary; this is not a production-data crash experiment.

Existing handler/pipeline/context/script budgets remain unchanged. Added owned
process bounds are 45 seconds for startup, 10 seconds for the writer rendezvous,
20 seconds for a callback timeout that **fails**, 10 seconds each for process and
client joins, and 2 seconds for event drain/reader joins. The helper has a
90-second context and two-minute process timeout; these do not extend the parent
budgets. Failure cleanup kills and joins before canceling/draining, with no
unbounded event-channel range. The script verifies that the exact selected test
exists in the built executable, so a build-tag-excluded platform cannot pass by
running only `PreparingReceipt`. This SIGKILL scenario is not built on Windows.

On 2026-10-31, the frozen ordinary process-crash scenario **passed 249 checks
in 22.60 seconds**, and its separate composed `--race` invocation **passed 248
checks in 304.77 seconds**, both exit 0 plus all thirteen PREPARING-receipt
oracle cases. Their evidence suffixes are `1oX476` and `1T2S37`, respectively,
under `/workspace/scratch/e4799e9b8e60/devbridge-evidence/devbridge-test-only-continuation.`.
The single check-count difference is bounded workspace-read polling, not a
removed invariant. All fourteen source hashes matched between both runs and
the final source; no race warning was emitted. Each run proved two actual
SIGKILL deaths, one forwarded original commit per death, nil client result with
real HTTP EOF, three distinct epochs per crash, joined processes/callers/readers,
exact raw snapshot equality, and no additional Agent protocol activity.

Independent review approved the final timing and cleanup oracles and precise
coverage limits. A separate reviewer lifecycle passed 247 checks in 21.69
seconds (`CFipZI`, exit 0) on the immediately preceding source, before the two
explicit pending-before-kill assertions were added; that is supporting review
evidence, **not** a final-source race or timing-hardening qualification. Earlier
authoring ordinary runs also passed; no failed crash run was relabeled success.
Final-source root `go vet -p 2 ./...`, opt-out tests, all receipt cases, shell
syntax, duplicate/conflicting selectors (exit 2), and `git diff --check` passed.
The two directly affected prior scenarios also passed on the same fourteen
source hashes: `--journal-recovery` **237 checks / 15.03 seconds** (`C4o0nU`)
and `--cancel-late-completion` **151 checks / 15.32 seconds** (`6OmQQh`), each
exit 0 plus the thirteen receipt cases. These remain scoped functional/race
results, not a fresh whole-root-suite pass, production-default timeout
qualification or real-model/native acceptance.

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
and dependency hashes. Check counts can vary with bounded workspace-read
polling; the business lifecycle and required invariants are unchanged. Start
admission remains limited to success or the explicitly qualified `PREPARING`
reservation, not a generic allowance for failed or unknown outcomes.

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
resume/successor, interrupted execution recovery, journal recovery beyond the
labeled rollback, final-save abort, post-business-commit cancellation and exact
post-business-commit process-crash cases, unmodeled response-loss/crash points, cross-principal
authentication and other cross-Session workflows, hostile runtime behavior,
and terminal acceptance/delivery are outside this bounded gate. Only the explicit `--response-loss` scenario injects the documented
post-handler response-stream failure; ordinary exact replay is not called
response-loss testing. The separate production-binary simulator retains
its own explicit NONE/NOT_RUN waiver boundaries and previous limitations.
