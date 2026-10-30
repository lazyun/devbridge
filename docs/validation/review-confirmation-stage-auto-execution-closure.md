# Review confirmation stage and auto-execution validation

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-7e28780a062b256b)。

Opening baseline was branch `dev_20260908_1913`, HEAD
`669627138b8d8eb3dd582f1e944504c78b3c3021`, schema v84 and an empty index.
The opening non-ignored byte manifest is
`/private/tmp/review-confirmation-opening.sha256` with SHA-256
`304f3bcf148267c5fe470e510d8c2e5384011dd7cf56fa0242881aced4dec626`.

The change advances the schema to v85 and adds immutable Review result
materialization. Focused fixtures cover a valid result entering confirmation,
an invalid raw result retaining typed failure without a decision, repeated
`FURTHER_REVIEW_REQUIRED` confirmation/re-review, stale Review rejection,
receipt replay and concurrent approval uniqueness. Repository-unlink migration
fixtures explicitly stop at v84 so their historical contract remains isolated.

## Verification

| Command | Result |
| --- | --- |
| `go test ./... -count=1 -timeout=8m` | PASS with a fresh isolated Go cache |
| `go test ./... -count=1 -timeout=8m` in `desktop` | PASS in the default package-parallel matrix |
| `go vet ./...` in root and `desktop` | PASS |
| Focused `TestMultiRepositoryCodingReviewApproveAndStartCreatesAuthorizedExecution` | PASS; one revision, one Execution, one `START_AUTHORIZED`, worker reached `IMPLEMENTING` |
| `node --test --test-reporter=spec internal/bridgehttp/*_test.js` | PASS, 114/114 |
| `node --test --test-reporter=spec internal/mcpserver/*_test.js` | PASS, 150/150 defaults; four established opt-in snapshots skipped |
| `npm test` in `desktop/frontend` | PASS, 52/52 |
| `npm run build` in `desktop/frontend` | PASS |
| `node scripts/build-chatgpt-ui.mjs /private/tmp/devbridge-review-confirmation-card-build` | PASS, two self-contained assets |
| `git diff --check` | PASS |

The first Desktop aggregate observed the known three-second local `--version`
probe timeout. Its exact focused test passed immediately, and a complete default
package-parallel rerun passed without changing code or timeout policy.

Tests used only temporary databases, repositories, caches and loopback
listeners. No live Task, Review, Run, Thread, Turn, DataRoot or model service
was read or modified.
