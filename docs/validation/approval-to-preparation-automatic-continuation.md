> Historical validation record. Describes the implementation before the final v0.1 model; it is not a current architecture or interface contract. See [current model](../architecture/task-execution-v0.1.md).

# Approval → automatic first Preparation closure

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-9b630cbc4d59a05a)。

Baseline: `dev_20260908_1913` at
`d2874e7703b4f67a8ff5ccb6c9809aee0b997da8`, clean; schema v41.
Implementation uses an isolated `codex/approval-preparation-continuation` branch.
Schema remains v41; no migration or continuation table was added.

## Resulting contract

The former gap was an explicit `START_PREPARATION` instruction after Approval,
combined with no async GPT wake-up after a Card action. APPROVE now durably
materializes the receipt, resolving session, Development Task and immutable
initial revision, then signals Bridge's write-side loop. The first Preparation
starts without another GPT or user command. Preparation-ready still follows the
existing approved Execution handoff; this closure adds no later-stage scheduler.

The shared retirement/reconciliation loop handles an immediate postcommit wake,
startup, terminal wakes and a one-second fallback. Each sweep uses a rotating
keyset cursor and at most 16 candidates. Atomic admission includes Preparation
ordinal 1, its linked owned Run and the ordinary FeatureArea claim. Reservation
failure leaves no partial attempt. Existing attempt history prevents automatic
retries, including after launch failure, response loss and restart.

The current immutable approval/revision/session/source and lifecycle/cancellation
fences are checked again in the writer transaction. RETIRE/cancel committed
first prevents admission. Admission committed first remains subject to canonical
owner cancellation. Existing runtime epoch, UNKNOWN_ORPHANED, launch protocol,
telemetry and finalization behavior are unchanged.

New approved Tasks return `WAIT_FOR_PREPARATION_START` (`required=false`), then
`WAIT_FOR_PREPARATION_COMPLETION`. Explicit `task_prepare` returns
`PREPARATION_AUTO_START_PENDING` or `PREPARATION_ALREADY_STARTED` for initial or
existing attempts. Failed approved attempts use `RETRY_PREPARATION`; historical
legacy guidance and explicit retries retain their earlier behavior.

Card silently polls the pending automatic-start state every five seconds and
shows “正在启动 Preparation…”. Approval from a selected Review follows the exact
materialized Task. The Card is not re-emitted through MAIN. Web and adapters
consume the same authoritative guidance. Reads never trigger admission.

## Validation evidence

- Store tests cover concurrent ensure/manual start/Approval replay, exactly one
  Task and first ordinal, exact source/revision/session/Run/claim, reservation
  rollback, explicit failed retry, read purity, durable restart gaps, stale
  identities, canceled Task and RETIRING admission fences, busy claim release,
  and canonical cancellation admission after the retirement race.
- Retirement race was repeated 20 times, with six competing admissions per run.
- Real Core bootstrap tests close after Approval before its wake, reopen the
  temporary DataRoot, observe first admission and a deliberately unavailable
  fixture Codex failure, then verify no second attempt on fallback or restart.
- A pre-reservation runtime-unavailable test verifies no durable failed attempt,
  followed by autonomous bounded retry. It also preserves a long Chinese Goal
  while bounding only its display title.
- Cross-adapter tests observe automatic Preparation and reject manual duplicates,
  while retaining exact receipts and the existing no-second-confirmation gate.
- Production candidate Core, GPT MCP and Local MCP binaries use temporary
  DataRoots and a deterministic Codex App Server fixture. After Web APPROVE the
  client sends no GPT/Local/Web calls until a read-only test observer sees the
  exact first Preparation Run in RUNNING. Both known-cancellation and
  UNKNOWN_ORPHANED scenarios emit `NO_GPT_APPROVAL_TO_PREPARATION_PASS`.
- Candidate authoritative snapshots drive an actual mounted Card DOM action and
  SILENT refresh sequence through Approval → Preparation → ready, retaining the
  same Card instance. No `task_prepare`, MAIN Card emission or synthetic event
  is used. The real ChatGPT host and a paid model are not part of this fixture.
- Gates: complete `go test ./... -count=1`; affected race suites (SQLite,
  Preparation, controlapi, controlplane, GPT/Local MCP); `go vet ./...`; Core/GPT
  MCP/Local MCP builds; Web/Console and Card DOM/transport JavaScript tests;
  production ChatGPT UI build; changed Go formatting and `git diff --check`.

## Whole-diff review

No remaining blocking finding. Review checked the approval/admission/launch
transaction boundaries, logical first-attempt uniqueness, startup and replay,
manual compatibility, failure retry limits, lifecycle/claim fences, read purity,
Card navigation/polling and legacy behavior. The review also closed a legal-long-
Goal admission dead end and retained legacy `START_PREPARATION` failure guidance.

`ASYNC_GPT_WAKEUP_NOT_IMPLEMENTED`. Deterministic Approval → first Preparation
continuation does not require it. Permanent pre-admission infrastructure failures
remain pending for bounded retry; failures after durable admission remain an
explicit recovery/retry concern. This work does not add automatic Implementation
or Verification startup, and does not change the formal 8081 runtime, live
SQLite, release or remote Git state.
