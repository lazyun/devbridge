> Historical validation record. These commands describe a prior checkpoint, not current package or architecture contracts. See [final v0.1](../architecture/task-execution-v0.1.md).

# FEATURE_AREA_RETIRE_ORCHESTRATION_CLOSURE_V2

Current Task/Execution authority and automatic approved Implementation follow the [final v0.1 model](../architecture/task-execution-v0.1.md).

## Baseline and isolation

Fresh source baseline: `dev_20260908_1913`,
`00ebc718f634de26de6e861566bc52d6c64c58e0`, clean worktree, schema 36.
Implementation uses isolated `codex/feature-area-retire-v2` at
`/private/tmp/devbridge-retire-v2`. The final source commit is the commit containing
this report, fast-forwarded into the development branch; its full SHA is reported
in the completion response to avoid a self-referential commit identifier.

The existing Core `/readyz` returned ready. A read-only `bridge_diagnostics` call
reported existing runtime `0.1.0-dev+gbd772e9`, ready, streamable HTTP. This older
runtime was not replaced, restarted or otherwise administered. No live SQLite
was edited. Candidate builds, fixtures, repositories, DataRoots and ports were
isolated. No push, release or deployment is included.

## Implemented authority

The background coordinator starts at runtime boot, wakes after accepted intent
and owned terminal completion, and internally reconciles every 100 ms. Each
child acceptance is followed by a final completion check. The timer also covers
claim-release, orphan/recovery and restart commits outside an owned runtime.
Reads and Refresh never drive convergence.

| Work | Convergence validated |
| --- | --- |
| DISCOVERING / ACTIVE, empty | Single request → durable RETIRING → automatic RETIRED |
| Development awaiting Task confirmation | Canonical TASK cancellation; Task canceled, plan rejected; no confirmation receipt |
| Pending / confirmed pre-Run Execution | NO_BACKEND; no fake Run, Thread, Turn or interrupt |
| Implementation | Canonical exact EXECUTION cancellation, known and UNKNOWN_ORPHANED |
| Verification | Exact Verification Run; successful Implementation history unchanged; both dispositions |
| Review | Visible REVIEW Task / exact Review Run; both dispositions; completion race preserves genuine completed history |
| Multiple Tasks / completed history | Same-area current Tasks converge; terminal history remains |
| PROJECT INSPECTION / another FeatureArea | Not selected or canceled; independent advancement/finalization continues |

Pending Task/Execution/Approval Decisions cannot advance after RETIRING. Owner
cancellation, pending plan rejection and approval expiry remove actionability
without inventing Decision results. Approval CANCEL retains its original meaning.
Existing user cancellation is reused, including reason/session/request identity;
retirement does not establish a second child cancellation protocol.

Known completion uses exact terminal/finalization facts. UNKNOWN_ORPHANED retains
original UNKNOWN and false/unknown quiescence, permanently revokes authority,
quarantines workspace/context, terminalizes owner/Task and releases the exact
claim atomically. RETIRED does not mean every historical backend is physically
dead. Late epoch/Thread/Turn/Run success, claim recreation, Decision and Host Tool
advancement remain fenced. Historical conversations, Threads, provenance and
all work records remain readable.

Crash tests close/reopen SQLite after intent, cancellation intent, dispatch and
last Task terminal. A newly started coordinator reaches RETIRED with only pure
store observations, without a second RETIRE or redispatch. Intent and final-state
fault injection prove rollback. The unchanged v36 orphan fault tests cover
terminal/context/disposition/claim failure points and permanent fencing. Replay
returns the same intent's current RETIRING/RETIRED state.

## Product and client behavior

Web enables close for DISCOVERING/ACTIVE regardless of current work/Decision/
recovery, confirms automatic stop plus history retention, and acknowledges
RETIRING before final completion. Closing/closed states and authoritative progress
appear in the list and drawer. Closed/All preserves history. Task rows show real
cancellation and `Agent结果未知 · 已隔离` for orphan disposition.

## Deterministic and regression gates

| Gate | Result |
| --- | --- |
| Retirement lifecycle, migration, owner matrix, Decision, Inspection, ordering, restart/fault | PASS |
| Full `go test ./... -count=1` | PASS on final source |
| sqlite, execution, controlapi, consoleapi, bridgehttp, mcpserver, localmcp, controlplane, featurearea, retirement, review, taskpreparation, executionproposal, workflowpresentation and cancellation regression | PASS in full suite |
| Affected packages `go test -race … -count=1` | PASS on final source (15 packages; 13 with tests, 2 without test files) |
| `go vet ./...` | PASS |
| Production Go / embedded Web | PASS: devbridge, devbridge-mcp, devbridge-local-mcp |
| Web/Console and ChatGPT DOM/transport | PASS: 106/106, `node --test internal/bridgehttp/*test.js internal/mcpserver/*test.js` |
| Production ChatGPT UI | PASS: `node scripts/build-chatgpt-ui.mjs <empty-temp-output>`, two self-contained assets and manifest |
| `gofmt -l` | All 46 changed/new Go files clean; whole-repository audit found 7 unchanged baseline files, each byte-identical to starting HEAD |
| `git diff --check` | PASS |
| v36 termination-boundary regression | PASS, including `TestAuditOwnedCloseDoesNotJoinDescendantWriter`; Close remains distinct from physical quiescence |

The unchanged baseline formatting findings are `internal/bridgehttp/console.go`,
`internal/consoleapi/logs.go`, `internal/controlplane/dataroot_integration_test.go`,
`internal/document/service.go`, `internal/document/service_test.go`,
`internal/projectcontext/bootstrap.go`, and `internal/workflowstate/state.go`.

## Actual candidate runtime acceptance

`REAL_CODEX_UNKNOWN_NOT_RUN`. No result is presented as a real Codex physical
termination proof. The fixture demonstrates protocol/orphan semantics only.

## Whole-diff review

Residual boundaries: orphaned historical descendants may continue writing their
quarantined old workspace; they cannot regain Bridge authority. Inconsistent
identity remains fail-closed and RETIRING retries instead of inventing completion.
There is no physical-death guarantee and no live runtime upgrade in this task.
