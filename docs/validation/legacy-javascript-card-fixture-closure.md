# Legacy JavaScript and Card fixture closure

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-9bbe4a29b247b192)。

Date: 2026-09-18

## Baseline

- Branch: `dev_20260908_1913`
- HEAD: `9f606647e4bb5bcbff54ab568b123d142042a064`
- Schema: v84
- Index: empty
- Opening non-ignored byte manifest: `/private/tmp/legacy-js-card-baseline.sha256`
- Opening manifest SHA-256: `33a858871a34dc7ad1492bf2f43b3b667f708e01d6303a61f9b403c1996a378a`
- Card assets: `dev_bridge_ui.html` `f663b5402810831f29d47407ad0e529dacb2822f767ecbc7c4ef1ef41cf01eb3`; `feature_area_details_ui.html` `54fc6d315a8624496c2f4f8ca56c4a09d63a88c640616b9e5ba82ede0bd87a4f`

## Failure ledger

| Surface | Primary category | Evidence | Disposition |
| --- | --- | --- | --- |
| `product_app_bootstrap_test.js` pagination | `HARNESS_DEFECT` | Node strict-assert formatting recursively traversed enumerable DOM/document/template back-references and exhausted the heap. | DOM-double graph internals are non-enumerable, compact inspection is bounded, and hierarchy cycles are rejected. The real multi-page path remains enabled. |
| `product_app_bootstrap_test.js` Task tabs | `FIXTURE_DRIFT` | The stale assertion required `.task-tabs` to disappear after a paginated Run refresh, while the current Task view keeps its navigation available. | Assertion now verifies that Task navigation remains present after page 2 refresh. |
| `dev_bridge_ui_test.js` | `ASSERTION_SUPERSEDED` | Legacy cases expected Card-side Session/Project creation, branch and workflow mutations, runtime approvals, cancellation/recovery, or writable decision controls. | Fixtures now carry authoritative entry identity. Retired cases verify the mounted `read-only-v1` mutation fence, exact reads, and exact Task navigation. No skip was added. |
| `dev_bridge_collapsible_test.js` | `ASSERTION_SUPERSEDED` | Expected removed collapse wake/message and writable action behavior. | Replaced by collapsed and expanded passive-read assertions with no mutation controls. |
| `dev_bridge_host_capability_test.js` | `ASSERTION_SUPERSEDED` | Expected removed `messageText`, `toolsCall`, and wake diagnostic APIs to grant behavior. | Host declarations are verified unable to grant Card mutation authority; diagnostics remain bounded and RPC-free. |
| `dev_bridge_pending_scope_test.js` | `ASSERTION_SUPERSEDED` | Expected removed pending aggregation/project-decision helpers and Card-side decision submission. | Foreign pending items are isolated and canonical pending projections remain read-only. |
| `dev_bridge_refresh_modes_test.js` | `FIXTURE_DRIFT` | Retry/loading assertions targeted the old write/readback UI. | Manual refresh is verified as a bounded read and late results are fenced after identity replacement. |
| `dev_bridge_review_wakeup_test.js` | `ASSERTION_SUPERSEDED` | Expected automatic claim/message wake-up after Review approval. | Durable Review status is presented as chat guidance and refresh cannot produce a side effect. |
| `dev_bridge_task_centric_test.js` | `ASSERTION_SUPERSEDED` | Expected runtime chooser, `decision_respond`, and operation mutation controls. | Compact canonical Task projection and exact-target failure behavior are verified read-only. |
| Candidate mounted-host snapshots | `OPT_IN_NOT_RUN` | Four tests require `DEVBRIDGE_CARD_SNAPSHOTS` and an explicit candidate-host run. They are not part of the default offline suite. | Retained as explicit opt-in; default run reports four skips with their names and no failures. |

Every failure in this scope has a final primary category and disposition.

## Verification

| Command | Result |
| --- | --- |
| `node --test --test-reporter=spec internal/bridgehttp/*_test.js` | PASS, 114/114, 0 skipped |
| `node --test --test-reporter=spec internal/mcpserver/*_test.js` | PASS, 150/150 default tests; 4 explicit opt-in snapshots skipped |
| `npm test` in `desktop/frontend` | PASS, 52/52 |
| `npm run build` in `desktop/frontend` | PASS |
| `node scripts/build-chatgpt-ui.mjs <temporary-directory>` | PASS, two self-contained assets; hashes unchanged from baseline |
| `git diff --check` | PASS |

The JavaScript/Card changes relative to the opening byte snapshot are confined to the two Product DOM files, the base Card harness, the six focused Card suites, and this ledger. No production Card, Core, SQLite, runtime, MCP, or Desktop source was changed in this closure.
