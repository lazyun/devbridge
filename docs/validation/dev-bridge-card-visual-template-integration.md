> Historical validation record; not a current contract. See [final v0.1](../architecture/task-execution-v0.1.md).

# DEV_BRIDGE_CARD_VISUAL_TEMPLATE_INTEGRATION_READY

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-5e098cfa9bd95a19)。

Starting branch: `dev_20260908_1913`; starting HEAD:
`47c8ce339ba56fec3758a57bf36f395b79717305`. Fresh Git inspection found a clean
checkout. Work was isolated on `codex/card-visual-template`. The final commit is
reported in the delivery response; integration uses fast-forward only, with no
push or release. Schema remains **v41**, verified by querying `schema_migrations`
in a newly initialized temporary Core database. No Go source or migration changed.

## Template integration

Visual reference: user-supplied `dev_bridge_card_visual_template.html`.
The original production Card DOM remains the source of truth. Its complete old
CSS block was replaced by the template's semantic tokens and component rules,
adapted to existing class names. No demo toolbar, demo state machine, mock data,
external stylesheets or CSS framework enters `ui://devbridge/card`.

| Production component | Template mapping |
| --- | --- |
| Card, header, Project/refreshed metadata | Single surface, 20px radius, soft shadow, restrained 16px brand, lightweight bound indicator |
| Branch | Transparent command row, subtle hover, rotated disclosure glyph, subordinate expanded surface |
| Feature/Task | Muted semibold labels, 13.5px entity titles, native lightweight selectors, accessible two-line titles |
| Status | One pill system: active/success/warning/danger/neutral; authoritative labels unchanged |
| Product Progress | Review → Approval → Task → Run; 21px markers, fine connectors, explicit state glyphs |
| Task stages | Preparation → Implementation → Verification; 17px markers, smaller labels, tighter spacing |
| Codex | Lightweight summary, secondary activity text and divider; existing telemetry only |
| Approval | Accent callout, decorative check, primary approval, secondary revise, subtle destructive reject |
| Recovery/warnings | Shared warning surface and soft border; recovery action remains secondary |
| Execution cancel | Existing location and handler, subtle destructive button with danger hover |
| Diagnostics | Native default-collapsed disclosure and subordinate expanded content |

Tokens cover background/surface, secondary/hover/pressed surfaces, borders,
primary/secondary/muted text, accent, success/warning/danger/info, radius, spacing,
shadow, focus and 150ms transitions. Host CSS variables supply available surfaces,
text and status colors; template values are fallbacks. Explicit light/dark theme
attributes and OS dark preference are supported; explicit light wins over OS dark.

Removed old scattered light/dark component colors, rectangular status treatments,
heavy normal execution surface, brightness-filter hover, duplicate task-stage
styles and obsolete eyebrow/inline-actions/screen-reader selectors. Native DOM
connector spans stay present but hidden; CSS draws template connectors between
existing step nodes. There are no `!important` declarations or inline styles.

## Frozen behavior and deliberate differences

An automated comparison against starting HEAD confirms that the embedded script
is identical except for three presentation changes: pill tone selection, the
manual Refresh `aria-busy` indicator, and a stable chevron glyph rotated in CSS.
All action payloads, action handlers, authority/fencing, workflow mapping,
polling, transport, dedup, wakeup eligibility/receipt/context/message, navigation
and binding code are preserved. No section was added or reordered.

The baseline's Agent message disclosure is in the **Web work-details drawer**
(`internal/bridgehttp/ui/product-app.js`), not in the MCP main Card. It remains
unchanged, including drawer-local state, navigation reset, refresh preservation,
waiting priority and independent collapsed activity diagnostics. Its Web tests
were run. Main Card continues to show only its existing Codex summary and status
diagnostics. Likewise, no empty Task stages or Codex block is synthesized for
Review approval: production projection/DOM conditions remain authoritative.

Other visual differences from the demo are intentional adaptations: native
Feature/Task selects and full Branch controls remain; existing copy and metadata
placement remain; callout icon is inline with its heading instead of adding a
new column wrapper. Light muted text is slightly darker (`#72727c`) for small-text
contrast; approval uses the template's stronger accent, and dark primary buttons
use dark foreground for contrast. Long status text can wrap at narrow widths.

## Visual and accessibility acceptance

Evidence: `/private/tmp/card-template-evidence/`. `responsive.json` records **78**
combinations: all 13 requested states × Light/Dark × 320/560/850px. All have
`scrollWidth == clientWidth`; long mixed-language titles stay at two lines.
Screenshots include approval, implementation and recovery at all widths/themes,
plus all requested states at 560px. `approval-light.jpg` and `approval-dark.jpg`
show normal-length titles. Earlier exploratory images are superseded by the
width-suffixed final-build images and final approval pair.

Approval, implementation, recovery and 320px dark layouts were visually inspected.
The two steppers have distinct hierarchy, recovery remains the abnormal-state
focus, approval buttons wrap without overflow, and destructive controls are muted.
Branch Enter/Space toggling, `aria-expanded`, `aria-controls`, retained expansion
through manual Refresh and 320px expanded geometry were checked in the browser.
Existing accessible names, full title/aria-label, and native keyboard selectors
remain covered by DOM tests. Focus-visible includes disclosure summaries; status
continues to use text and glyphs as well as color. Reduced-motion CSS removes
button/chevron transitions and Refresh animation. No animated progress or pulse
was introduced. This is targeted accessibility QA, not a full assistive-technology
audit or automated OS media-emulation test.

## Gates and functional regression

- JS Card DOM/presentation, legacy transport, Details, polling, wakeup and Web
  drawer suites: **260 passed**, three optional candidate tests skipped in the
  generic run and then exercised successfully by candidate acceptance below.
- `go test ./... -count=1`: PASS, including Go presentation, MAIN/SILENT/DETAILS,
  cancellation, approval and recovery tests.
- `go vet ./...`: PASS. Changed Go `gofmt -l`: not applicable (no Go changes).
- Final self-contained ChatGPT UI build: PASS; both Card and Details validated.
- Final Core/GPT/Local binaries and packaged smoke: PASS. Fresh
  `resources/read ui://devbridge/card` is **68,698 bytes**, byte-identical to source,
  SHA-256 `fb7109f6a40aea31f7963dcdabaca73252de4887cde3e1d8d9fc01c28b4815e5`.
- `DEVBRIDGE_WEB_OBSERVABILITY_BIN=... go test ./internal/localmcp -run
  TestWebObservabilityCandidateRuntimeAcceptance -count=1 -v`: PASS in both normal
  and UNKNOWN_ORPHANED scenarios. Actual candidate Core/GPT/Local processes and
  deterministic Codex fixtures validate durable Review completion, mounted Card
  updates, one exact wakeup, polling, automatic first Preparation after Approval,
  token parity, cancellation and recovery. Three optional snapshot JS tests run
  here against actual candidate projections.
- Packaged smoke verifies Project binding/manual refresh inputs, MAIN duplicate
  suppression, explicit Retry, SILENT reads and independent Details rendering.
- `git diff --check`: PASS.

**REAL_CHATGPT_HOST_NOT_RUN**: browser acceptance uses the production self-contained
resource in an isolated fixture host. No real ChatGPT model turn, paid Codex run,
formal runtime restart or production connector deployment is claimed.

Whole-diff review found no new P1/P2 issue within the exercised scope. Source
comparison verifies all frozen logic; source/build/resource hashes match. No
schema, telemetry, lifecycle, action-authority or layout-contract changes remain.
Builds, temporary binaries, servers and this task's worktree/branch are removed
after fast-forward integration; retained screenshots and validation logs are
acceptance evidence rather than build/cache artifacts.
