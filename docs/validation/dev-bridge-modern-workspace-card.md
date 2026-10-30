# Modern Workspace Card presentation

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-07514a00c430e930)。

Implemented the supplied HTML/CSS visual reference in the production
`internal/mcpserver/dev_bridge_ui.html`, using the existing shared projection.
The header, branch row, Feature/Task tiles, separate status cells, current
activity and refresh error surface use local CSS and inline SVG. Native selects
retain the existing navigation handlers and accessible names. Branch disclosure
retains its controls and keyboard behavior; a separate copy button copies the
shown branch and short revision and reports unavailable clipboard access.

The unbound Project view has a separate header, introductory paragraph and
labelled native picker. It emits no workspace, activity or status placeholders.
Removed the older CSS rule that hid the selected Task title. Existing workflow,
Task, Result, approval and recovery sections remain available.

No projection schema, workflow reducer, action payload, authority validation,
presentation lease, Card identity, transport or polling logic changed. Existing
function changes are limited to renderProjects, selector, renderBranch and
render; new helpers produce static SVG markup. No external assets or libraries
were introduced.

Validation:

- Node Card, wakeup and candidate suites: 165 passed, 3 optional candidate
  snapshot tests skipped because no candidate runtime was supplied. This count
  includes the shared Card harness tests imported by the other suites.
- `go test ./internal/mcpserver -count=1`: passed with dependency downloads off.
  Local loopback access was needed for the package's test servers.
- Browser fixture: initial, Review running, implementation running, recovery and
  complete states; long titles; light/dark themes; 320/560/850px iframe widths.
  No horizontal overflow in the measured states; entity titles remain at most
  two lines. Initial Project text and label have separate, non-overlapping flow.
- Refresh failure retains the successful projection and shows an accessible
  alert; Retry clears the alert after successful reads. Narrow dark error state
  and keyboard Branch expansion were checked. Polling remains unchanged and
  can clear a transient failure automatically, so the fixture can fail all
  projection read paths while the failure checkbox is checked.
- `git diff --check`: passed.

All browser checks used the local fixture host. No external network, real Codex
execution or real ChatGPT host was used.
