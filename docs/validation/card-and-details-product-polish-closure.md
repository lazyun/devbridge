# Card and Details product polish closure

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-fe52f9c7f56992f4)。

Baseline: `dev_20260908_1913`, `5ea3a92fca40ff3f6b717aa71436e15b20cbd7ff`, clean.
Implementation uses an isolated worktree. Schema remains **v37**, with no migration.

## Contracts

- Before: app-only `feature_area_details_get` owned the Details template and
  DETAILS emission. After: it retains its input/output and authoritative read
  semantics but is SILENT and resource-free. New model/app-visible
  `feature_area_details_show` exclusively owns the Details template and DETAILS
  emission. Both use the same reader; the widget loads the complete paginated
  result through hidden get. Minimal show results and late identity responses
  have transport regression coverage. Main show tools remain MAIN. No domain or
  hosting configuration was added and no View details action was added to MAIN.
- `project_open(projectId)` preserves explicit first-selection/new-session
  behavior. `project_open(aiSessionId, projectId)` reopens the same durable
  session for the same Project; a different Project fails with
  `AI_SESSION_PROJECT_ALREADY_BOUND` and
  `当前会话已绑定其他项目，请新建会话后切换项目。`.
  GPT and Local MCP use the shared Core implementation. Existing Console Project
  browsing remains independent. Existing session storage supplies restart
  durability; no second binding model exists.
- Project and Development Branch now occupy separate full-width rows. Bound
  Project is read-only. Branch and short SHA share a line, warnings remain local,
  and Create Branch base/name/action stay together on desktop. Narrow screens
  wrap without horizontal overflow. Branch create/select/checkout stay separate.
- The Card locks its first authoritative Project identity. Navigation and
  replacement projections revoke stale action authority. Detached pre-binding
  selectors, concurrent selectors, stale branch handlers, async digest/hydration,
  and same-navigation replacements have regression coverage.

## Validation

- `go test ./...`.
- `go test -race ./internal/aicontext ./internal/controlplane ./internal/controlapi ./internal/storage/sqlite ./internal/mcpserver ./internal/localmcp`.
- `go vet ./...` and production builds of Core/GPT MCP/Local MCP.
- `node --test internal/mcpserver/*ui_test.js internal/bridgehttp/*ui_test.js`:
  112 passing tests, including Card and Details DOM/transport and Console JS.
- `node scripts/build-chatgpt-ui.mjs <temporary-output>` validates both production
  self-contained UI resources.
- Changed Go files: `gofmt -l` clean. `git diff --check` clean.
- `scripts/card-details-polish-smoke.py` exercises fresh packaged tools/list,
  resources/list/read, pre-session show, explicit Project A selection, same-ID
  replay, Project B rejection, Refresh, cross-adapter rejection, Details read/show
  and unchanged history, Feature navigation, and cold restart/replay. Each run
  uses random loopback ports, temporary repositories, and a temporary DataRoot.
- Browser DOM/screenshot acceptance uses the actual Card HTML with an isolated
  JSON-RPC fixture. Desktop and narrow viewport checks cover full-width sections,
  Create controls, checkout mismatch, ref moved, Feature/Task navigation, and
  RETIRING/RETIRED display. This fixture is distinct from packaged-service smoke.
- `REAL_CHATGPT_HOST_NOT_RUN`: no real ChatGPT conversation was changed.

Whole-diff review found no P1/P2 defects. RETIRE orchestration, canonical
cancellation, UNKNOWN_ORPHANED, claims and Decision semantics are untouched;
their package regression tests pass. The formal 8081 runtime and live SQLite
were not changed. No push or Release is part of this closure.
