> Historical validation record; not a current contract. See [final v0.1](../architecture/task-execution-v0.1.md).

> Historical implementation evidence. [GPT surface consolidation V1](../architecture/gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

# MOUNTED_CARD_REVIEW_COMPLETION_GPT_WAKEUP_CLOSURE_READY

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-52746fb0d4db9400)。

Bridge-side implementation and fixture acceptance; **REAL_CHATGPT_WAKEUP_NOT_RUN**.
Real ChatGPT automatic model execution is not proven.

Starting development branch: `dev_20260908_1913`, clean at
`7e17e40e26aea72a155ddcd39a95776b149970ed`. Isolated implementation branch:
`codex/mounted-review-wakeup`. Final commit is the revision containing this
report, recorded explicitly in the completion response. Integrate by fast-forward;
no push or Release. Business schema remains **v41**. No formal port 8081 or live
SQLite access is needed by the implementation or acceptance.

## Protocol and product contract

The Card continues using the self-contained raw MCP Apps JSON-RPC bridge, protocol
`2026-01-26`. There is no installed `@modelcontextprotocol/ext-apps` dependency to
upgrade. API verification used the current official SDK source, especially
`src/spec.types.ts` and `src/app.ts`, on 2026-09-10. The prose specification's
older single-content message illustration differs from the current TypeScript
schema: this implementation uses the SDK's **content array**.

- OpenAI's [UI bridge documentation](https://developers.openai.com/plugins/build/chatgpt-ui)
  recommends standard `ui/*` methods, with optional widget-state persistence.
- The [official SDK types](https://github.com/modelcontextprotocol/ext-apps/blob/main/src/spec.types.ts)
  define `ui/message` as `{role: "user", content: ContentBlock[]}` and
  `ui/update-model-context` with content blocks or structured content.
- The [official App implementation](https://github.com/modelcontextprotocol/ext-apps/blob/main/src/app.ts)
  sends these same methods from `sendMessage` and `updateModelContext`.

The initialize response supplies `hostCapabilities`. The Card requires advertised
text message support for automatic delivery; context text support is detected
separately. Context failure/absence is diagnostic only and does not change Review
success. The message remains self-contained with exact identifiers and digests.
No parent-page DOM, synthetic keyboard, custom host endpoint, sampling, or server
assistant-generation workaround is used.

An accepted SILENT snapshot renders Approval first, then schedules one attempt.
Eligibility requires all of:

- Exact bound AI session, Project, selected FeatureArea and current workflow.
- CURRENT mode, non-legacy, non-stale projection, no admitted Development Task,
  no historical/read-only selection, cancellation, recovery, retiring or retired
  FeatureArea; selected task, if present, must be the exact Review task.
- Current Review ID agrees with the workflow and pending Approval; status is
  SUCCEEDED; both durable result and provenance SHA-256 digests are present.
- REVIEW_APPROVAL_REQUIRED in the projection and authoritative guidance; exact
  Decision ID/subject agrees with the pending REVIEW_APPROVAL and its PENDING
  receipt. Optional projected Review/details identities must agree too.

The active 5-second timer stops at pending Approval. First eligible mount is
also allowed; RUNNING need not have been observed in that widget. Failed,
canceled, infrastructure-failed, unknown, foreign and historical Reviews never
produce an approval-summary notification. Review/Approval UI and its explicit
approval buttons remain available.

After the presentation-only server reservation, `ui/update-model-context` sends
only identity, state and read/summarize/wait instructions. Next, after an additional
complete authoritative SILENT Card refresh,
`ui/message` sends a user-role `[Dev Bridge] Review completed` follow-up with the
same identity. Neither contains the Review result, proposal, repository content,
commands or credentials. The wake invokes no MAIN, DETAILS, Approval, Task,
Preparation or other business mutation.

Both GPT and Local `review_get` descriptions require a fresh exact session/Review
read plus current `workflow_guidance_get` validation. The model must summarize
conclusion, scope, key changes, risks/conflicts/limitations and recommendation,
then stop. If superseded, retired or resolved, it must report current state and
stop. The notification is explicitly **not user approval**. The requested ending
is “如果你确认，我再批准进入实施。” Core still owns every admission and decision.

## Receipt, retry and asynchronous fences

The presentation key includes session, Project, FeatureArea, Review, result digest,
provenance digest, Decision and pending state. It deliberately excludes global
workflow generation/source sequence, so unrelated events do not resend. Review B
and a new immutable result identity have distinct keys.

| State | Meaning / behavior |
| --- | --- |
| WAKEUP_NOT_ATTEMPTED | Eligible for one automatic attempt |
| WAKEUP_IN_FLIGHT | Persisted reservation before any host context/message effect |
| WAKEUP_SENT | Host API accepted; no automatic or button retransmission |
| WAKEUP_FAILED | Explicit error/unsupported/stale/preflight failure; manual retry only |
| WAKEUP_UNKNOWN | Send timeout/dispose; may already be delivered; inspect conversation before explicit retry |

The local receipt lives in `window.openai.widgetState.privateContent.reviewWakeups`,
restored across normal remount/layout/reload. An in-memory in-flight set prevents
concurrent operations in one widget. A new app-only SILENT
`dev_bridge_review_wakeup_claim` tool additionally coordinates **separate Cards**
on the same GPT MCP process, even when a new widget inherits no host state.

This tool first reads the existing authoritative FeatureArea projection under the
bound AI session and validates the exact pending Review/Decision/digests. A mutex
then reserves the full identity once. The first call returns CLAIMED; subsequent
calls return ALREADY_ATTEMPTED, which is deliberately not a delivery acknowledgment.
A duplicate automatic attempt stops with an uncertain outcome/manual action.
Explicit user retry may proceed after either result, but still requires fresh
eligibility. A claim transport failure suppresses send; a lost claim response may
consume the attempt, recoverable only by explicit retry. The reservation never
expires or evicts old keys. At 10,000 entries it fails closed; normal chat remains
available. Its tool metadata is app-only, read-only for business data and SILENT;
it emits no resource. It changes no Review, Decision, Task, event or SQLite row.

The widget saves an in-flight receipt before reserving. A host widget-state write
failure is diagnostic: the server reservation still prevents duplicate automatic
delivery while that MCP process lives. Context/send errors do not cause 5-second
retry loops. Normal reload of an unfinished receipt also does not auto-retry; its
outcome is shown as uncertain. Hosts without text message support show a normal-chat
fallback and the “让 GPT 总结” action rather than claiming success.

No business migration or durable server outbox was added. Normal widget remounts
are covered by host presentation state, and fresh Cards are covered by the shared
MCP process reservation. The process-local reservation resets on MCP restart; a
preserved widget receipt still prevents retransmission. If **both** the server
reservation and host receipt are lost (for example, a server restart followed by
a completely new widget/device), history is unavailable. Multiple independent GPT
MCP processes do not share this in-memory ledger. These are explicit durability
limits, not a claim of cross-device exactly-once. An explicit retry after uncertain
host delivery can also duplicate a message. Durable outbox storage would be needed
to extend those lifetimes, and is intentionally not introduced into schema v41.

Navigation generation and exact eligibility are checked before and after each
await preceding send. The post-context authoritative read re-renders any new
retirement/resolution/superseding Review and suppresses the old message. Failed,
out-of-order or foreign-project readbacks cannot authorize delivery. Foreign-session
notifications cannot rebind an already-bound Card. Late send replies update only
the original receipt, and cannot display success on another selected feature.
Dispose/hidden navigation suppresses unsent work. Once a host message has actually
been submitted it cannot be recalled; the fresh model read and Core's mutation
validation remain the downstream boundary. There is no atomic transaction spanning
Core state and the external host effect.

## Acceptance evidence

| Layer | Fixture host | Real ChatGPT |
| --- | --- | --- |
| A: API request accepted | PASS, including JSON-RPC error and isError paths | NOT RUN |
| B: message inserted/visible | Captured once in simulated host conversation | NOT RUN |
| C: model response actually starts | Not simulated or claimed | NOT RUN |
| D: model reads exact Review and summarizes | Instructions/catalog verified; actual model execution not claimed | NOT RUN |

Read-only browser discovery found only an empty Codex in-app browser and no
connected ChatGPT test conversation. No candidate connector was installed into a
real ChatGPT host, and no formal workflow was altered to create one. Consequently
**REAL_CHATGPT_WAKEUP_NOT_RUN** applies. No observed real-host A/B-only regression
exists in this acceptance. If later testing finds insertion without model execution,
report `CHATGPT_HOST_REVIEW_WAKEUP_LIMITATION`, without looping or declaring the
end-to-end wakeup complete.

The host harness covers RUNNING → poll → Approval → context → fresh read → one
message, eligible first mount, repeated/overlapping reads, preserved-state remount,
new Review/result identity, unrelated generation changes, shared mounted copies,
context failure, protocol rejection, `isError`, timeout, retry success, unsupported
capabilities, persistence failure, dispose before/during send, hidden/visible
lifecycle, navigation during context/read/send, session/project mismatch, retirement,
Approval resolution and supersession. It captures visible messages separately from
request posting and checks that all wake-side tool calls are SILENT reads or the presentation-only reservation.

Production candidate Core/GPT/Local processes run against temporary repositories,
DataRoots and a deterministic Codex fixture on random loopback ports. Actual durable
Review snapshots drive the same mounted UI. Actual GPT MCP claim/replay and exact durable review_get are also
verified before Approval, with zero Development Tasks created. The new candidate test proves one
follow-up bound to the real pending Review/Decision/digests. Existing automatic
Approval-to-Preparation, Review/Preparation polling, cancellation/orphan and token
parity candidate scenarios continue to pass. A fixture message does not prove a
ChatGPT model turn.

## Gates and whole-diff review

- `go test ./... -count=1`: PASS. The first run caught a missing entry for the new
  app-private tool in the canonical interface catalog; documentation was updated
  without weakening discovery assertions, and the complete suite rerun.
- `go vet ./...`: PASS.
- Affected `go test -race ./internal/mcpserver ./internal/localmcp -count=1`: PASS.
- Production `-trimpath` Core, GPT MCP and Local MCP builds: PASS.
- Card/Details/legacy transport, Web/Console and desktop JS: PASS; the three
  optional snapshot tests are skipped in generic JS and explicitly PASS inside
  production candidate acceptance.
- Production self-contained ChatGPT UI build: PASS.
- Packaged fresh tools/list, resources/list and resources/read: PASS. Resource
  content is byte-for-byte the current source Card, including wake methods.
- MAIN lease/alias replay/explicit Retry, DETAILS/SILENT reads and restart binding:
  PASS. Existing 30-second lease and presentationRequestId logic are unchanged.
- Changed Go formatting and `git diff --check`: PASS.

Whole-diff review checked exact eligibility, terminal transitions, remount and
in-flight reservations, explicit retries and uncertain delivery, state persistence
failure, server reservation identity/concurrency/capacity/session isolation, fresh read fences, disposal/late acknowledgments, approval boundaries,
model-visible contract, MAIN emission and cancellation/retirement regressions.
The initial widget-only dedup gap for newly opened Cards was closed with the
process-local presentation reservation. No new P1/P2 finding remains within the exercised Bridge-owned boundary. Host
execution and host storage lifetime limits are explicit above.

**SERVER_SIDE_ASYNC_GPT_WAKEUP_NOT_IMPLEMENTED**: an unmounted Card or closed
ChatGPT page cannot receive this notification. No scheduling, background push,
webhook, or server-created model turn is implemented.

Final housekeeping: fast-forward integration, clean development checkout, and
removal only of this task's worktree, branch, temporary build/cache/test artifacts.
