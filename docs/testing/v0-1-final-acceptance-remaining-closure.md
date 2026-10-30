> Historical validation record. Its checkpoint and screenshots are not the current architecture or interface contract; see [final v0.1](../architecture/task-execution-v0.1.md).

> Historical implementation evidence. [GPT surface consolidation V1](../architecture/gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

# V0_1_FINAL_ACCEPTANCE_REMAINING_CLOSURE

Current Task/Execution authority and automatic approved Implementation follow the [final v0.1 model](../architecture/task-execution-v0.1.md).

Result: **V0_1_FINAL_ACCEPTANCE_REMAINING_CLOSURE_READY**.

This is the requested A/B/C implementation closure and bounded whole-diff
inspection. No new Global Final Acceptance Re-review was started.

## Revision and integration evidence

- Starting branch: `dev_20260908_1913`, initially clean.
- Starting HEAD: `0fef927c950271265720b89f4c696d6e45c794e1` (matches the requested baseline).
- Final validated implementation HEAD: `0bea8ef57186fcc661d006fa7ea409ea48091c62`.
- This report is committed afterward; the final development HEAD is reported in
  the delivery response, since a commit cannot include its own hash.

| Scope | Preserved implementation commits |
|---|---|
| A: durable runtime fencing | `e1cb6e5`, `0050910` |
| B: Development Context / FeatureArea Card | `29be22a`, `bdba6db` |
| C: Console usage / canonical docs | `dfde540`, `07f455e`, `6a8db9a` |
| Main-agent shared contracts | `bb38ade` (pre-session show), `22a2eb3` (Console DTO) |
| Integration corrections and evidence | `d48cc9a`, `a059f40`, `1e96f39`, `e16375d`, `0bea8ef` |

All branch commits were integrated with merge commits, retaining their history.

## Runtime fencing closure

`CodexUsageStore.Save` now uses the existing `DB.beginWrite` transaction, which
holds SQLite's writer lock while checking the current durable runtime authority.
The same transaction checks the Run's runtime epoch, live status, exact claim,
canonical Thread, applicable Turn identity, conversation lifetime and absence of
a finalization intent, then performs the existing ledger revision/accounting
epoch CAS and attribution completion CAS. Failed fences and CAS conflicts roll
back both ledger and attribution changes.

Client callback persistence retains its originating runtime/Run context, with a
bounded timeout that preserves the fence. The store does not discard that timeout
or weaken a caller's existing Host Tool fence. New Run-owned Thread initialization
stays in memory until the Coordinator has bound the Thread and started the Run;
the pending marker is persisted before Turn dispatch. This avoids falsely making
all fresh Runs UNAVAILABLE during the pre-binding interval.

ContextProbes have no Run: their writes still use the current durable runtime
authority, and the Runless path rejects Threads already owned by a durable Run.
No second runtime authority, accounting state machine, workflow or finalizer was
introduced. Accounting epochs remain token coordinates, independently of runtime
epochs. The epoch-aware accounting algorithm and canonical Thread identity rules
are unchanged, including reset-boundary UNAVAILABLE and later AVAILABLE recovery.

Coverage includes current writes, stale caller/runtime authority, stale Run after
restart, wrong/empty attribution owner, exact Thread/Turn checks, rejected
mismatched claims, released claims, terminal/finalization closure, competing
revision writers, immutable completed attribution and crash markers. A real
Codex Client with a local fake stdio process and SQLite exercises the Coordinator's
startup sequence and usage/completion arriving before the Turn response, for both
Run and Probe paths. This is not external real-Codex E2E.

## Presentation and token contracts

Previously the SILENT `project_list` could not reach a main Card before
`project_open`, because `development_context_card_show` required a session.
Now show accepts omitted `aiSessionId` and returns an unbound presentation without
opening a Project or inventing a session. The mounted Card reads inventory via
SILENT `project_list`; only a user selection calls authoritative `project_open`,
then loads the bound Development Cycle and branch projection. The bound private
Refresh tool still requires a session.

Current Project, Current Development Branch and Current Revision are separate
facts. Switch target starts at an explicit placeholder. Create branch base has
its own persistent label and selector displaying its exact SHA. Neither selector
defaults to inventory's first branch. Create, select and checkout remain separate
authoritative actions; selected revision never substitutes for unavailable current
revision.

FeatureArea uses a responsive Stepper that mechanically renders authoritative
phase/status facts. COMPLETE, RUNNING/current, PENDING, BLOCKED, FAILED, CANCELED,
UNKNOWN and RECOVERY_REQUIRED are represented; other statuses remain explicit
and never silently become PENDING. Last refreshed uses the successful projection
read time, including unchanged Refresh, action reprojection and remount. Failed,
malformed or stale reads retain the prior time and display a refresh-failed
notice. A failed action reprojection leaves Refresh usable. No domain `updatedAt`
is used as refresh time. The main Card hides View details; the existing Details
backend, read model, resource and tests remain.

Console `OperationUsage` is now `{status, usage?, duration_ms?}`. AVAILABLE has
exact mandatory counts even for zero; PENDING and UNAVAILABLE retain distinct
states and omit counts. Optional cached/reasoning breakdowns remain absent when
unknown. The renderer also suppresses counts supplied with a non-AVAILABLE status.
No partial total is reconstructed. Existing Run and External Execution status/
usage contracts and text/JSON fallback remain covered by regression tests.

Only explicit `*_card_show` emits MAIN. Commands, readback, polling and in-place
Refresh remain SILENT; Details remains separate DETAILS. No durable presentation
state was added.

Canonical interfaces, Development Cycle, production Card, current UX closure,
Console guide and token-accounting docs were reconciled. Foundation documentation
is retained with an explicit historical-phase label. The branch-create signature
uses `baseCommit` and has no `requestId`; creation and selection receipt semantics
remain distinct.

## Validation matrix

| Gate | Result |
|---|---|
| `go test ./... -count=1` | PASS; 54 packages with tests; repeated after final Card correction |
| Complete affected-package `go test -race ... -count=1` | PASS; package list below; MCP repeated after final Card correction |
| `go vet ./...` | PASS |
| `go build -trimpath -o <temporary-output>/ ./cmd/...` | PASS on final implementation |
| `node --test internal/mcpserver/*ui_test.js` | PASS, 79/79 DOM/transport tests |
| `node --test internal/bridgehttp/*ui_test.js` | PASS, 45/45 Web/Console tests |
| `node scripts/build-chatgpt-ui.mjs <temporary-output>` | PASS, three self-contained production assets |
| Fresh GPT/Local catalog and closed schemas | PASS in complete Go suites, including pre-session show schema/purity and fresh production discovery |
| Refresh purity | PASS, presentation/storage/transport and deterministic UI clock coverage |
| Token status parity | PASS, Console JSON/DOM and unchanged External Execution availability/schema tests |
| Runtime stale-writer / concurrent CAS | PASS, SQLite and Client lifecycle tests, including race run |
| `gofmt -l` on changed Go files | PASS, no output |
| Whole-repository `gofmt -l` | Seven pre-existing, unchanged files listed below; no new formatting deviations |
| `git diff --check <starting-HEAD>..HEAD` | PASS |
| Desktop gate | Not expanded; Desktop unchanged |
| External real-Codex E2E / real ChatGPT host | Not run; not claimed |

The complete race command covered `internal/codex`, `internal/codexusage`,
`internal/storage/sqlite`, `internal/run`, `internal/execution`,
`internal/agentconversation` (no test files), `internal/probeexecution`,
`internal/controlplane`, `internal/mcpserver`, `internal/localmcp`,
`internal/controlapi`, `internal/consoleapi`, `internal/bridgehttp` and
`internal/workflowpresentation`.

Whole-repository formatting observations, each verified unchanged from starting
HEAD: `internal/bridgehttp/console.go`, `internal/consoleapi/logs.go`,
`internal/controlplane/dataroot_integration_test.go`, `internal/document/service.go`,
`internal/document/service_test.go`, `internal/projectcontext/bootstrap.go`,
`internal/workflowstate/state.go`. Unrelated formatting was not swept into closure.

## Bounded whole-diff findings

All fourteen requested inspection points were checked. Integration findings were
closed: pre-binding Run fencing, preserving Probe accounting, exact Thread/Turn
ownership, retaining write deadlines, current versus selected revision, unknown
Stepper states, deterministic refresh timestamps, failed-action Refresh recovery,
and the outdated branch-create documentation signature.

Remaining evidence-only gaps are external real-Codex execution and real ChatGPT
host acceptance. Local stdio, DOM, transport and schema tests do not claim those
external observations.

## Git and temporary resources

Development line: `dev_20260908_1913`. All A/B/C and integration commits are
retained. The three task-created worktrees and their merged temporary branches,
task build directories and task caches are removed at delivery. Pre-existing
worktrees are retained. The final development working tree is clean. No push or
Release was performed.
