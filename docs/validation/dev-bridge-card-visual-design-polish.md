> Historical validation record. Describes the implementation before the final v0.1 model; it is not a current architecture or interface contract. See [current model](../architecture/task-execution-v0.1.md).

# Dev Bridge Card visual design polish

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-bb829de15b41c680)。

Starting HEAD: `da0b6a330e954dbee2d8c43287f7d19a5eca17b7` on `dev_20260908_1913`.
Scope: production ChatGPT Card HTML/CSS, presentation tests and documentation.
Database migrations remain v38 (confirmed in the isolated fresh Core's
`schema_migrations`). No Go product logic, resource metadata or tool catalog changed.

## Result and layout

The bound header places Dev Bridge, session binding and Refresh on one row;
Project is readonly inline metadata beside the last successful read time.
Feature and Task names are primary text with one status/type pill each. Native
selectors retain exact IDs, an explicit Feature overview option and historical
read-only navigation. Long names wrap and retain full title text.

Branch defaults to a single disclosure row with short revision. Checkout mismatch
and moved-ref warnings appear inside the expanded section. Switch remains separate;
Create uses a full-width base select followed by name and Create on one row.
Disclosure state is local, survives Refresh/navigation, and resets on session change.
It never selects, creates or checks out a branch by itself.

The main stepper displays Review → Approval → Task → Run. It groups existing Core
`progress` phase facts; PREPARATION / IMPLEMENTATION / VERIFICATION share Run,
with Preparing / Implementing / Verifying from `currentPhase`. Failed, canceled,
unknown and blocked progress retain their existing markers. No new workflow
classifier, nextAction inference or Task/Run-history inference was introduced.

Only actionable decisions or recovery create a decision surface. Review approval
has one primary Approve action, secondary Revise and subtle Reject. Runtime
permission choices, recovery, start and canonical cancellation controls retain
existing capability/target validation. No empty Decision panel or main JSON/detail
history remains. MAIN/SILENT/DETAILS routing is unchanged.

## Visual acceptance

Real production HTML was served unmodified through an isolated iframe fixture
host using the existing postMessage handshake and read-only fixture replies.
The host rejects mutation calls and connects to no Core, database or agent.
Run it with `node internal/mcpserver/testdata/card_visual_host.mjs`.

At 560px, the same approval fixture's reported iframe height fell from 1245.70px
to 488.69px (about 61%); both measurements use the same host sizing convention.
The before Card expanded branch diagnostics and six vertically stacked progress
steps; the after Card uses disclosure, four horizontal steps and one approval
surface. This is fixture evidence, not a ChatGPT host measurement.

Accepted snapshots: REVIEW_REQUIRED (no Task), REVIEW_RUNNING,
REVIEW_APPROVAL_REQUIRED, DEVELOPMENT_TASK_READY, PREPARATION_RUNNING,
IMPLEMENTATION_RUNNING, VERIFICATION_RUNNING, COMPLETE, RETIRING, RETIRED,
RUNTIME_APPROVAL_REQUIRED and RECOVERY_REQUIRED. Checked expanded branch controls
and checkout mismatch/moved-ref warnings. Long names were checked at 320, 560 and
850px; no visible clipping of names, overlapping buttons or horizontal card
scrollbars. The 320px Create controls preserve base above name/Create.

Keyboard Enter and Space toggle Branch, retain focus, and expose aria-expanded /
aria-controls; Refresh preserves expansion. Native selects and buttons remain
keyboard operable, names have full text/title, and status uses text/markers as well
as color. This is targeted accessibility verification, not a full assistive
technology audit. Light rendering was visually inspected; dark theme uses host
variables with dark fallbacks and was not separately host-tested.

Evidence retained outside the repository:
`/private/tmp/devbridge-card-visual-polish-evidence/`.
Key files: `before-approval.png`, `after-approval.png`, `narrow-expanded.png`,
`narrow-collapsed.png`, `long-560.png`, `long-850.png`, state-named PNGs,
`browser-states.json`, `authority-review.json`, `fresh-resource.json`, `schema.json`.

**REAL_CHATGPT_HOST_NOT_RUN**. No real Codex, Desktop, full retirement runtime
acceptance, or live 8081 restart was performed.

## Gates and whole-diff review

- 31 Card DOM tests; 120 combined Card/Details/Decision and Web JS tests passed.
- Go MCP server, workflowpresentation and local MCP suites passed, including
  transport, binding, stale decision, review approval and cancellation regressions.
- `go vet ./...` passed. Changed Go test file is gofmt-clean. `git diff --check` passed.
- Production self-contained UI build and Core/GPT MCP builds passed.
- Final binaries started against a temporary data root and random loopback ports;
  MCP `resources/read` of `ui://devbridge/card` matched the final source byte-for-byte:
  43512 bytes, SHA-256 `d0634d890e6f77be1577fd540c995254d50d653418f6488a11a2769a066230b9`.
- Race gate is not applicable: no Go product logic changed.

An existing discovery test expected “preserves authored instruction bytes” in
frozen server instructions, which actually say “passes those instructions
verbatim”. Only that test expectation was repaired; server instructions and the
separate documentation expectation were preserved. Initial sandbox-bound tests
could not bind loopback sockets; the final authorized loopback runs passed.

Whole-diff review found no new P1/P2 issue. Header density, readonly Project,
collapsed warnings, two-row Create, single pills, four-step mapping, action
hierarchy, empty-state removal, responsive behavior and keyboard disclosure were
checked. Request transport, refresh, actionAuthority/currentActionAuthority,
validActionTarget, invokeAction, readActionSubject, receive, dispose and exact
workspace/navigation matching were compared unchanged against the starting HEAD.
The setIdentity change only clears local disclosure state on session changes.
Branch mutation payloads, exact base/ref use, stale closures, no auto-selection,
manual history focus, session binding and Last refreshed semantics remain covered.
No v38 migration, receipt, Core admission, Preparation/Execution, retirement,
UNKNOWN_ORPHANED, binding or visibility behavior was changed.
