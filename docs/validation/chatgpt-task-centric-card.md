# CHATGPT_TASK_CENTRIC_CARD_UI_CLOSURE

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-c6efb751c65b1b50)。

Status: **CHATGPT_TASK_CENTRIC_CARD_UI_IMPLEMENTED**
Host acceptance: **MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING / NOT_RUN**

## Workspace and scope

- Root: `/Users/phoenix/Documents/devbridge`
- Branch: `dev_20260908_1913`
- Starting and final HEAD: `4ed8e3e7aaa584f4f7f46190b6273e2a068d754f`
- Starting tree was dirty. Existing wakeup, host diagnostics, pending scope, current Decision authority, minimal approval, and selector changes were preserved.
- This task changes `internal/mcpserver/dev_bridge_ui.html`, `dev_bridge_ui_test.js`, and `dev_bridge_pending_scope_test.js`; adds `dev_bridge_task_centric_test.js`, this report, and `chatgpt-task-centric-card/` evidence. Existing Go and interface-document diffs belong to earlier work, not this task.
- No commit, merge, push, deploy, service restart, live SQLite writes, or real Task / Review / Decision operations. No browser, Desktop, or simulator was started.

Reference: user-supplied `/Users/phoenix/Downloads/dev-bridge-card-source.zip`. Inspected its desktop/mobile screenshots and display-layer CSS/JS/types. Adapted presentation to existing production HTML and embedded-resource delivery; did not replace the production file with the reference package. No demo data, runtime defaults, framework, CDN, or tool was imported.

## Rendering changes

Before: `render` placed a separate project Pending Decisions section above Task content; `renderProgress` subtracted aggregate targets; Task output appended Review facts/history/outcomes and diagnostics.

After: `render` builds WorkspaceHeader → WorkNavigation → TaskSummary → `renderCurrentInteractions` → DetailsEntry. Header settings contain Host capabilities, wakeup/claim and refresh diagnostics. Feature and Task use native selects. Task summary has the accurate title, status, four metadata fields, and the Chinese stage rail. Empty Task selection renders overview without a fabricated Task.

`currentInteractionEntries` combines presentation descriptors from `currentPendingInteractions()` and projection capabilities/actions, retaining each source's existing scope and authorization checks. Actions deduplicate by complete action and target; omitted redundant Task/FeatureArea fields on business Decision targets normalize to the exact current scope. Pending and projection options for one Decision share its typed panel. Every legitimate current runtime request remains, including separate request IDs with identical capability targets. The source partitions remain independent; this is not an atomic business snapshot. There is no `aggregateTargets` subtraction or separate top Pending Decisions panel.

Review Approval shows “Review 已完成”, “请根据 GPT 的分析决定是否继续。”, expanded Model/Reasoning/Fast controls, and 确认 / 拒绝. APPROVE appears first regardless of capability inventory order. REVISE remains in business capabilities but is omitted from this minimal surface. Runtime permissions, user questions, recovery and other typed actions retain their required content.

Explicit details remain inside the same card. They read precise Review/Execution targets through existing `operation_get`, validate returned kind/ID/owner/run, and fence late results by scope and sequence. Object groups are read-only. Returning restores the same Task, configuration drafts and navigation; details do not create mutation authority. Default Task content no longer appends history or long Review bodies.

Current activity requires consistent running facts. Terminal Run or conflicting activity is presented as neutral last activity without altering Core Task phase. Existing polling, refresh modes, menu dismissal, draft capture/restore, host display mode and size notification remain in use.

## Authority and runtime preservation

The existing pending Decision and projection dispatchers still submit exact targets with request-ID idempotency, asynchronous authority revalidation, stale/detached callback fencing and authoritative readback. Local details add a read-only guard. Confirm submits the selected authoritative model, supported reasoning effort and Fast choice through `nextRunRuntimeConfig`. Reject works independently of invalid runtime controls and omits that configuration. Confirm does not invoke `execution_start`.

[Preserved-contract hashes](archive/chatgpt-task-centric-card.md#file-f4adefb288cddf32) compare the current-task starting HTML with the final HTML. These six sections are byte-for-byte unchanged: wakeup transport/claim lifecycle; pending scope predicate; option-owner predicate; `nextRunRuntimeFields`; `invokeProjectDecision`; `invokeAction`. This comparison is against the already-dirty starting file, preserving earlier fixes.

Parallel read-only preparation covered rendering/helper tracing and regression design. The shared HTML/CSS was edited by one owner. Independent functional and static responsive reviews completed; their findings on action order and shared runtime targets were addressed and covered by regression tests.

## Automated validation

All checks use the real production HTML, existing dispatcher and projection fixtures. Existing UI regression tests were retained; layout assertions and explicit-details setup were updated for the new surface.

| Check | Result | Evidence |
| --- | --- | --- |
| Targeted UI, pending scope and Task-centric Node tests | 346 passed, 0 failed | [log](archive/chatgpt-task-centric-card.md#file-4a4e96d4d617757d) |
| `node --test internal/mcpserver/*test.js` | 1000 tests: 996 passed, 4 existing optional fixture skips, 0 failed | [log](archive/chatgpt-task-centric-card.md#file-4f8bc66ce645e430) |
| `go test ./internal/mcpserver` | PASS | [log](archive/chatgpt-task-centric-card.md#file-7a48d73d527710e3) |
| `git diff --check` | PASS | Final working-tree check |

Go tests ran with `DEVBRIDGE_REAL_CODEX` and `DEVBRIDGE_REAL_REVIEW_SMOKE` empty. Sandbox escalation allowed Go cache and test-local loopback access only. No projector package was changed in this task.

New regression coverage includes first-show snapshot/refresh/collapse parity; exact approval runtime payload and independent rejection; multiple runtime requests and foreign-scope exclusion; precise read-only details and draft return; late/invalid detail responses; background focus preservation; terminal activity; overview/native selection; static responsive/theme contracts; partial source capability deduplication; and terminal Run facts with an ongoing Task phase. Existing tests cover permissions, user input, recovery, acceptance, strict Session, stale callbacks, foreground loading and readback failures.

## Reference differences and manual acceptance

The production card adopts the reference's white surface, pale blue border, rounded sections, green cube brand/confirmation and pale red rejection. Desktop runtime controls use two columns; narrow layouts use one, keeping actions side by side. Host dark-theme variables and reduced-motion support remain. Responsive CSS and DOM contracts were reviewed for desktop and 320 / 390 / 680px, including title wrapping, min-width and native keyboard controls.

Actual pixel rendering, horizontal overflow, native-select popup behavior and keyboard focus in ChatGPT are **NOT_RUN**. Reference screenshots were inspected; no new production screenshot was generated. Residual differences include Host-controlled typography/native controls, real authoritative model choices and metadata, dynamic numbers of runtime requests, and the existing labeled collapse control. Pixel parity is not claimed.

Manual Host acceptance, using authorized test fixtures/Tasks:

1. Open a workspace with a current Review Approval and an unrelated-area pending Decision. Verify one current approval panel, no foreign action, no long Review body, and no displayed REVISE action.
2. Check Model/Reasoning/Fast defaults against authoritative configuration. In an authorized test Decision, confirm the chosen payload; separately verify rejection with incomplete controls and no automatic execution start.
3. Check first show, manual refresh and collapsed → expanded. At desktop and 320 / 390 / 680px, inspect long titles, native selectors, keyboard focus, visible actions and horizontal overflow.
4. While editing a runtime draft or opening a menu, allow a background refresh. Verify retained focus/draft/menu; verify foreground operations block duplicates and release loading on failure.
5. Check multiple legitimate runtime requests, permission warnings and user questions. Check running Review/implementation/verification, acceptance, completed/failed/recovery and no-Task states.
6. Enter details, inspect precise Review/Execution/Run groups, and return to the same Task with drafts intact. Historical/read-only content must not expose mutation authority. Switch scope while a read is pending and verify the late response cannot replace current content.
7. Verify terminal last activity is neutral and Host/wakeup/claim/refresh diagnostics are confined to settings. Check menu Escape/outside dismissal inside the card; cross-iframe click interception is not claimed.

Final working tree intentionally remains dirty with preserved earlier work plus this implementation and validation artifacts. HEAD and branch are unchanged. No GLOBAL_READY or deployment readiness claim is made.
