> Historical validation record. Describes the implementation before the final v0.1 model; it is not a current architecture or interface contract. See [current model](../architecture/task-execution-v0.1.md).

> Historical implementation evidence. [GPT surface consolidation V1](../architecture/gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

# REVIEW_OBSERVABILITY_AND_CARD_LIVE_UPDATE_CLOSURE_READY

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-c42ce75f1ce8e74e)。

Starting branch: `dev_20260908_1913`.
Starting HEAD: `b5d46cb15d97a7ec9faf40f4d446f62eff0ba72c` (clean).
Implementation branch: `codex/review-observability-card-live-update`, isolated
worktree under `/private/tmp`. Final commit/HEAD is the revision containing this
report and is recorded explicitly in the completion response. Integration is a
fast-forward of the starting development line; no push or Release.

Schema: **v41**, unchanged migration source. Formal port 8081 and live SQLite
were not used or modified. Candidate processes used random loopback ports,
temporary DataRoots/repositories and a deterministic official App Server fixture.

## Accepted contracts

- Activity: default collapsed; native Enter/Space/click, ARIA linkage; ordinary
  refresh preserves drawer-local state; FeatureArea/Review/Task navigation and
  close reset it. Terminal activity says 最后活动/已结束. Waiting summary precedes
  bounded content. Diagnostics stay collapsed, without duplicate message text.
- Token: exact Review → linked Run → existing token accounting. Review detail,
  REVIEW Task inventory and Console latest Run agree. Shared Task.Work reads
  the same exact Run. AVAILABLE displays provided counts, optional context;
  PENDING/UNAVAILABLE stay distinct, with no invented zero or derived counts.
- Card: one 5-second active-work timeout. Review, Preparation, Implementation,
  Verification, cancellation/finalization and retirement follow fresh complete
  projections. A selected terminal REVIEW Task still follows FeatureArea
  RETIRING → RETIRED. User-wait/terminal/no-selection/dispose stop polling.
- Lifecycle: hidden pauses, visible reads immediately, manual refresh and
  navigation cancel/rebind, no overlapping poll even if only one transport leg
  fails, failures preserve the last snapshot, older reads are fenced. New
  snapshots invalidate old action authority.
- Transport: widget → `dev_bridge_card_get` + `development_branch_list`, always
  SILENT. No show calls, resource emissions or assistant-message injection.
- MAIN: canonical `development_context_card_show` and compatibility
  `feature_area_card_show` share one server-local, mutex-protected 30-second
  lease per project-bound AI session (unbound entry is connection-scoped).
  Duplicate calls return ALREADY_SHOWN/SILENT and empty template metadata.
  Closed MCP object-root schemas advertise both possible result shapes.
- Retry/recall: later recall works after TTL; a new explicit
  `presentationRequestId` allows immediate recall/Retry, and the same successful
  identity is suppressed for two minutes across aliases. Failed tool-result
  production does not establish a lease or consume a retry identity. Mounted
  Retry itself uses SILENT read, with no new Card.

| Request | Final emission |
| --- | --- |
| Explicit first show / allowed recall | MAIN |
| Duplicate show | SILENT / ALREADY_SHOWN |
| Mounted active poll / manual Refresh / navigation | SILENT |
| Workflow guidance / readback / presentation reads | SILENT |
| `feature_area_details_show` | DETAILS |
| `feature_area_details_get` | SILENT |

`SHOW_CARD` is not a workflow next action. **ASYNC_GPT_WAKEUP_NOT_IMPLEMENTED**:
Card completion updates in place; no unsolicited GPT response is delivered.

## Candidate acceptance

`TestWebObservabilityCandidateRuntimeAcceptance` launches real candidate Core,
GPT MCP and Local MCP. It passes for known cancellation and orphan outcomes.
Actual active/terminal Review and Preparation projections feed the mounted
Card DOM harness, yielding `REVIEW_AND_PREPARATION_CARD_LIVE_UPDATE_PASS`:
same widget instance, next phase/Approval visible, no manual Refresh and only
SILENT reads. Deterministic timeout advancement avoids a five-second test sleep;
production cadence remains 5 seconds.

Official `thread/tokenUsage/updated` fixture observations yield
`REVIEW_TOKEN_EXACT_RUN_PARITY_PASS`: input 584,383; cached input 474,624;
output 3,111; reasoning output 1,166; total 587,494; context window 258,400.
The Review and exact REVIEW Task latest Run have equal serialized usage and
matching AVAILABLE Task.Work availability.

`card-details-polish-smoke.py` passes against packaged production candidates:
fresh tools/list, resources/list and resources/read; one MAIN followed by two
suppressed alias shows; explicit Retry MAIN followed by suppressed replay;
independent DETAILS and SILENT reads; durable Project binding across adapters
and restart. Lease unit tests additionally cover failed first result, retry
failure/success, explicit later recall, TTL expiry and concurrent calls.

The acceptance uses a fixture host transport, not a live ChatGPT conversation.
It does not claim to have observed a ChatGPT mount acknowledgment.

## Gates

- `go test ./... -count=1`: PASS, including Review/detail/projection, token,
  shared Task.Work, workflowpresentation, controlapi, bridgehttp, localmcp,
  agentruntime v40/v41 and cancellation/retirement regressions.
- `go test -race`: PASS for mcpserver, consoleapi, storage/sqlite, controlapi,
  bridgehttp, workflowpresentation and agentruntime; Local MCP recovery,
  cancellation and retirement subset also PASS.
- `go vet ./...`: PASS.
- Production Core, GPT MCP and Local MCP builds: PASS, temporary output only.
- Web/Console JavaScript syntax and JS suites: PASS. Card/Details DOM, polling
  and transport suites: PASS. Optional candidate-snapshot test is skipped in
  the generic JS run and explicitly passes in candidate acceptance.
- Production self-contained ChatGPT Card/Details asset build: PASS.
- Fresh resource and tool-catalog smoke: PASS.
- Changed Go `gofmt -l`: empty; `git diff --check`: PASS.

One initial full-gate failure was reproduced at the untouched starting HEAD:
`TestExplicitRecoveryReprojectsFailedExecutionAsReady` expected an empty
Details.RunID after recovery. Current Task-stage projection intentionally keeps
that terminal historical Run. The assertion now checks the preserved exact Run
and separately forbids live CurrentOperation/cancel authority. No recovery or
cancellation implementation was changed to silence the test.

## Whole-diff review and limitations

All 21 requested review points were checked: default disclosures, exact Run
binding/availability, Task parity, active polling/SILENT replacement, automatic
terminal transition, wait-state stop, lifecycle/fencing, alias dedup, failure
retry and recall, separated workflow guidance, emission matrix, absence of
synthetic wake-up, and unchanged workflow/cancellation/token authority. No new
P1/P2 was found within the Bridge-owned implementation and exercised transport.

The remaining host boundary is explicit: MCP supplies neither trusted user-turn
identity nor mount/error acknowledgment. The lease records successful Bridge
result production, not proof of a rendered host Card or delivery after a socket
failure. TTL and explicit request identity make unobserved failures retryable.
A host ignoring result SILENT/empty template metadata is outside this verified
contract; live ChatGPT mounting behavior is not proven by the fixture. Lease
state is process-local and resets on restart; deliberate new request IDs can
bypass the heuristic. No permanent one-Card lock or business lease was added.

Final housekeeping: fast-forward integration, clean development checkout,
remove task branch/worktree and task-owned build/cache directories; no push,
Release, formal runtime replacement or live database modification.
