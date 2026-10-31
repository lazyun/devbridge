# UPPER → DIRECT_FILE consumer composition

This gate composes `internal/upperworker` and `internal/fileworker` over the
**production GPT adapter's HTTP handler** and one genuine isolated Core, Session
and Task. Earlier consumer gates exercised each client separately through Local
MCP; a separate catalog check proved shared schemas. This gate tests the actual
cross-client workflow and GPT middleware, not another crash point.

## Authority remains separate

1. A schema-only database fixture enters production project/session,
   confirmation, coordinator and workspace services to admit an UPPER Review.
   No claim, grant, report or completion row is seeded.
2. The UPPER consumer reads both exact members through Core. Its deterministic
   test callback authors a v2 APPROVAL_READY report with real read evidence,
   a DIRECT_FILE work specification and a RUN verification proposal.
3. The report alone creates no file grant, mutation, result or acceptance.
   A separately simulated user uses the real typed confirmation service to
   APPROVE with an explicit USER_WAIVED selection. This deliberately changes the
   approved verification definition to NONE; it must not rewrite the original
   report's RUN proposal, authored work, read evidence or immutable receipt.
4. APPROVE is still NO_START. A second, independent START_DIRECT_FILE
   confirmation is required before a usable file grant exists.
5. The file consumer receives only the resulting exact grant, its own stable
   worker/request identities and the actual approved NONE mode. It reads source
   digests, changes only the approved member files and submits its actual formal
   completion. It neither selects a verification backend nor accepts the Task.
6. The UPPER consumer observes its original receipt after the file workflow
   closes. Historical UPPER readback must not borrow the subsequent file
   workflow's identity, executable input or result.

The callback implementation is explicitly deterministic test code, **not a live
dot/model callback**. Confirmation calls represent separate simulated user
choices through Core services; the test does not qualify a ChatGPT button click,
host confirmation UI, connected tunnel or deployed connector. No model is called.
The independent file-consumer RUN fixture already covers real Core verification
handoff using a test-only protocol subprocess; this composition gate does not
repeat it or silently waive production verification.

## Non-transferable claims

A test-only negative probe on the genuine enrolled file workflow uses the actual
UPPER claim identity with an otherwise exact file mutation target. It must be
refused without file or receipt changes before legitimate DIRECT_FILE writes.
This is a cross-channel authority-boundary check, not an invented target or a
behavior of either client. Identical worker labels alone never grant authority.

## Evidence and stopping point

The gate checks report/approval/frozen-definition conservation; no automatic
advance at report or APPROVE; exact grant lineage; distinct claims; actual
member changes; unchanged source repositories, unrelated repository and HEADs;
formal result and grant closure; historical readback; and no acceptance,
implementation/verification Run or Git delivery under the explicit fixture waiver.

```sh
go test ./internal/controlapi -run '^TestWorkerClientsComposeThroughProductionGPTHTTP$' -count=1 -v
go test -race ./internal/controlapi -run '^TestWorkerClientsComposeThroughProductionGPTHTTP$' -count=1 -v
```

Passing this gate means the two consumers compose through local production GPT
transport and real Core boundaries. It does not restore an unavailable OpenAI
tunnel, refresh an imported tool catalog, change credentials/host permissions,
qualify production native execution, clear unrelated full-root test limitations,
or authorize deployment, acceptance or merging.

## Qualification of this increment

- The exact composition test passed ordinarily in **6.043s** and with the race
  detector in **164.929s**, both exit 0. This is one composed workflow with a
  scoped negative probe, not a count of additional process-crash scenarios.
- Both consumer packages' unit regressions passed (`upperworker` **5.057s**,
  `fileworker` **19.352s**); root `go build -p 2 ./...` and `go vet -p 2 ./...`
  passed. These focused runs are not a claim that the full-root test suite passes.
- The composition test and both consumers' Go source hashes were unchanged across
  the frozen ordinary/race invocations; no race-detector warning was emitted.
  Relative documentation links, exact documented test selectors and whitespace
  checks passed. No production source was modified in this increment.
- Independent review required an exact structured Core worker-identity refusal
  and unchanged raw durable rows, rather than accepting any transport/schema
  error. Those assertions were corrected and approved before the frozen runs;
  the review also corrected the documented test selector and observation scope.

The subsequent [assistant-authored local MCP acceptance](assistant-authored-local-mcp.md)
uses actual assistant-authored review and implementation requests instead of these
deterministic callbacks. It also records the discovered file-only acceptance
projection defect and its narrowly scoped regression.
