> Historical validation record; not a current workflow contract. See the [final v0.1 model](task-execution-v0.1.md).

> Historical implementation evidence. [GPT surface consolidation V1](gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

# TOKEN_ACCOUNTING_EPOCH_AWARE_CLOSURE

Baseline: `dev_20260908_1913` at `0ba5c3a7327eca23a6e90a4345037c68761eb78f`.
Implementation branch: `codex/token-accounting-epoch-aware-closure`.

## Accounting contract

Codex `tokenUsage.total` is a cumulative snapshot within its current accounting
ledger/epoch. Resume restores that ledger when available and does not itself
create an epoch. `last` is an append sample, never a Turn total or an additive
source for Bridge accounting. Repeated cumulative snapshots contribute zero.

Bridge now owns `CodexUsageLedger` and `RunUsageAttribution` in
`internal/codexusage`. The ledger keeps independent raw and logical coordinates,
the accounting epoch, the last accepted Turn, and an unfinished-dispatch marker.
The attribution keeps exact Run/Thread/Turn identity, logical start/end, epoch
start/end, observation/completion evidence, and reliability/reason.

The logical cumulative is a Bridge coordinate over accepted usage evidence. It
is not a reconstructed lifetime total when historical telemetry is missing.
An AVAILABLE Run is the exact difference between its logical end and start.
All six token counters are handled independently; cached input and reasoning
output are not added to total tokens again.

The accounting epoch is only a coordinate for cumulative token evidence. It is
not a runtime-authority epoch and cannot authorize a writer, Run, or claim.
Durable ledger and attribution writes also pass the normal runtime/Run/claim
fence, so a stale runtime cannot mutate accounting merely because its ledger
revision and accounting epoch still match.

## Identity, ordering, and uncertainty

Only the exact `turn/start` response can authorize a candidate to update the
durable ledger. Notifications arriving before that response are reduced in
isolated provisional state; other candidates are discarded. Events for other
Threads, old/retired Turns, and completed Turns cannot update accounting, even
after the bounded retired-Turn cache is evicted. One active Turn per Thread
owns the coordinate at a time.

| Evidence | Accounting action |
| --- | --- |
| Nondecreasing current-Turn cumulative | Add componentwise raw delta to logical cumulative. |
| Equal snapshot / remembered earlier snapshot replay | Add zero. |
| First current-Turn snapshot regresses from a previous Turn | Advance epoch, establish the new raw coordinate, preserve logical monotonicity. Mark the boundary Run UNAVAILABLE because the exact reset position is unknown. |
| Unseen regression within the current Turn | Do not infer an epoch or replace the last accepted raw value. Mark the Run UNAVAILABLE. If no later acceptable end arrives, invalidate the next baseline. |
| Missing baseline / interrupted prior dispatch | First valid sample establishes a raw anchor without charging the unknown prefix. The current Run remains UNAVAILABLE; subsequent Runs can be exact. |
| Missing/null/invalid total, invalid counters, arithmetic overflow | No fabricated zero or negative delta. Mark affected accounting unavailable. |
| No telemetry at completion | UNAVAILABLE; invalidate the baseline so missing spending is not charged to the next Run. |
| Ledger read/write failure or revision conflict | UNAVAILABLE; never overwrite newer evidence or fail/rotate the conversation solely for accounting. |

An epoch regression can involve only some counters. A change in the token mix
must not strand the Thread behind the previous epoch's cache/output counters.
The reset Run is deliberately not claimed as exact. The next Run can be
AVAILABLE from the persisted new coordinate even though its predecessor's
`run_token_usage.status` is UNAVAILABLE. If ordering/end evidence is still
missing, availability waits until a trustworthy anchor has been observed.

No accounting code rotates a canonical Codex Thread or changes Feature Area,
Review, Preparation, Execution, Approval, or Decision state semantics.

## Persistence and migration

Migration 35 adds `codex_usage_ledgers` and `run_usage_attributions`. Existing Run
accounting and workflow history are not rewritten. Production bootstrap supplies
the SQLite store to Codex clients; the Coordinator supplies the exact Run ID as a
local attribution owner, without sending it to the App Server.

Before `turn/start`, Bridge durably marks dispatch uncertain. Once the response
identifies the Turn, accepted observations and attribution are saved atomically
with a ledger revision compare-and-swap. A completed attribution cannot be
rewritten or rebound. A crash before the response, between observations, or before
completion leaves an uncertain marker, so recovery cannot charge an unobserved
previous prefix to a new Run.

Cold resume loads this ledger independently of the previous Run's availability.
Only when no Bridge ledger exists may the existing exact legacy conversation-head
baseline seed it; a missing legacy baseline is not zero. Warm resume preserves
the existing raw/logical pair. Existing `run_token_usage` start/end columns store
logical coordinates for new exact observations, and their original coordinates
for historical records. Logs distinguish new logical snapshots with
`BRIDGE_LOGICAL_CUMULATIVE`; raw coordinates remain in the durable ledger.

## External Execution and presentation

`execution_get.execution.tokenUsage` uses the Run usage envelope:

```json
{"status":"UNAVAILABLE"}
```

```json
{"status":"AVAILABLE","usage":{"inputTokens":0,"cachedInputTokens":0,"cacheWriteInputTokens":0,"outputTokens":0,"reasoningOutputTokens":0,"totalTokens":0}}
```

Only Runs belonging to the requested Execution contribute. All must have valid
AVAILABLE usage to publish an exact total. Pending contributors yield PENDING;
an unavailable contributor dominates and yields UNAVAILABLE. Missing Runs,
legacy/malformed accounting, negative counts, and overflow yield UNAVAILABLE.
Neither unknown nor pending totals include a `usage` payload. Thus a partial sum
cannot impersonate a complete Execution total. The output schema changes with
the projection. Consumers of the former flat token-count object must read
`status` and then `usage`.

Run card metrics render observed zero as `0 tokens`, pending usage as pending,
and unavailable usage as unavailable. Card emission and workflow state remain
unchanged.

The Console/Web operation projection follows the same `status + optional usage`
rule. Its `usage.status` is always `AVAILABLE`, `PENDING`, or `UNAVAILABLE`;
only AVAILABLE has nested counts. Cached-input and reasoning-output remain
optional fields because source zero can mean unknown for those breakdowns.

## Validation

Regression tests cover multiple resets and all counters, mixed-counter reset,
exact zero, contradictory `last` samples, duplicate/stale/foreign/late telemetry,
pre-response notification ordering, warm resume, cold client reload, missing
baseline, interrupted dispatch, invalid/missing totals, persistence failure,
SQLite reopen and atomic rollback, completed attribution immutability, exact Run
delta persistence, External Execution availability/schema, and card metrics.

Commands:

```sh
go test ./...
go test -race ./internal/codex ./internal/codexusage ./internal/storage/sqlite ./internal/execution ./internal/run ./internal/probeexecution
node --test internal/mcpserver/*_test.js
```

Tests use controlled telemetry; no new telemetry research or live Codex spending
is needed to validate the frozen contract.
