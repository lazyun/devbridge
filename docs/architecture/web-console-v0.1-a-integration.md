# Web Console v0.1-A parallel integration note

Baseline: `cab6b206c37ab2cc042116f39d5ef999729c3810`

## Conflict audit

The audit was performed before production UI edits with `git worktree list` and
a read-only changed-path comparison of
`codex/m5-1-5-review-product-boundary` against the baseline.

### Safe to edit

- `internal/bridgehttp/ui.go` (unchanged in the parallel worktree)
- `internal/bridgehttp/ui/index.html` (unchanged in the parallel worktree,
  treated as an intentionally small integration seam)
- new `internal/bridgehttp/ui/product-*` assets
- new `internal/bridgehttp/product_console.go`
- new `internal/consoleapi/product.go`
- `internal/topic/service.go` (unchanged in the parallel worktree)
- new tests and this note

### Conflict-sensitive

- `internal/bridgehttp/ui/index.html`: not changed by M5.1.5-C at audit time,
  but it is the shared shell. This branch limits its change to loading the new
  product assets and leaves a compatibility marker for baseline tests.

### Do not touch in parallel

- `internal/bridgehttp/ui/app.js`
- `internal/bridgehttp/console.go`
- `internal/bridgehttp/console_test.go`
- `internal/bridgehttp/server.go`
- `internal/consoleapi/api.go`
- every Review DTO, persistence, prompt, Codex, MCP, execution, and schema path
  changed by M5.1.5-C

`app.js` remains byte-for-byte unchanged and is no longer loaded by the new
shell. No uncommitted parallel work was copied, resolved, or cherry-picked.

## New frontend modules and assets

- `product-i18n.js`: English and Simplified Chinese dictionaries, system
  resolution, English fallback, interpolation, and local preference storage.
- `product-status.js`: the single internal-state to product-presentation map.
- `product-components.js`: lightweight badges, empty states, disclosures,
  copy controls, timestamps, key/value details, Topic cards, and Module cards.
- `product-app.js`: Overview, Projects and Project subviews, Logs, Diagnostics,
  local navigation preferences, data loading, and interaction wiring.
- `product.css`: local responsive product styling with no remote assets.

The legacy monolithic `app.js` does not import these modules and must not be
loaded alongside `product-app.js` because both would own the same document.

## Post-M5.1.5-C integration steps

1. Merge M5.1.5-C first, then this commit, resolving only the shared
   `index.html` shell if the parallel branch began editing it after the audit.
2. Keep `product-i18n.js`, `product-status.js`, `product-components.js`,
   `product-app.js`, and `product.css` as the active shell assets. Do not restore
   the legacy `<script src="app.js">` tag.
3. Move any new Review UI behavior from the parallel `app.js` diff into a new
   product-focused Review module or the Topic detail renderer. Use the landed
   Review DTOs directly; do not reconstruct Reviews from Runs.
4. Add `Review` to the reusable Logs object selector only after the integrated
   log API exposes stable `review_id` filtering.
5. Prefer moving `/ui/api/topics` and `/ui/api/modules` into ordinary `/v1`
   read routes when touching the post-merge router. Preserve their current DTO
   boundaries: no creating AI Session on Topic cards; no Module ID or
   responsibility provenance in the product Module Map.
6. Replace the neutral Topic Reviews/Tasks/Executions note with actual sections
   only when each stable product read projection is present.
7. Rerun the Node frontend tests, focused `bridgehttp` tests, visual bilingual
   probe, and full root regression suite.

No model-facing MCP tool, GPT/Codex prompt, Review persistence, Review identity,
Task prompt, or Task/Execution contract is introduced by this branch.
