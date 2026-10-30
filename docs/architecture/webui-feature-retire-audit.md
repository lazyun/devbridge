# WebUI FeatureArea / Retire initial contract audit

Task: `WEBUI_TASK_OWNED_CONTRACT_AND_IA_CLOSURE`.

This records the read-only Wave 1 findings before implementation. The locations
below describe the initial baseline, not the final UI. The implementation reuses
the existing product/shared projection family and the authoritative retirement
adapter. It does not change retirement Core, prompt transport, review depth, or
production Card recall host behavior.

## Contract matrix

| Initial contract | Classification | Finding and required closure |
| --- | --- | --- |
| `GET /ui/api/featureAreas` | VALID | Existing project-scoped product inventory. Preserve Feature lifecycle and capability truth. |
| `GET /ui/api/project-workspace` | VALID | Existing product projection family. Feature Tasks must be semantic Tasks belonging to the exact Feature. |
| `GET /ui/api/featureAreas/{id}/presentation` | VALID | Uses the same `workflowpresentation.Projection` as other clients. Prefer exact current Task and shared commit facts. |
| `POST /ui/api/featureAreas/{id}/action`, `CREATE_TASK` | VALID | Feature-owned creation remains legitimate when the authoritative capability is available. |
| Feature `COMMIT_FEATURE_AREA` / `RESET_AGENT_CONTEXT` capabilities | VALID | Preserve authoritative capabilities and exact scoped targets. Commit entry belongs in Feature actions. |
| `POST /ui/api/featureAreas/{id}/close` | VALID | Thin project-fenced adapter delegates to `controlapi.RetireFeatureArea`. Keep the existing mutation owner. |
| `can_retire` / `retire_blocked_reason` | VALID | Console product projection reads formal retirement eligibility and shared `RETIRE` capability. Lifecycle must not become a second action authority. |
| Feature row Current Review / review tokens columns | SEMANTICS_CHANGED | Feature rows currently emphasize Review operations. Replace primary workflow emphasis with exact Current Task, commit and dev integration facts. |
| Feature `active_review`, `latest_review`, `reviews` primary sections | WRONG_OWNER | Operations appear as Feature-owned workflow sections. Task detail must own Review and Execution histories. |
| `openReviewDetail(featureAreaID, reviewID)` | WRONG_OWNER | Initial loader searches Feature review inventory. Product Review navigation must originate from the owning Task. |
| Feature `reviewApprovalPanel` and `/review-approval` action path | WRONG_OWNER | Approval is Task progression. Use Task phase plus authoritative Task actions; remove the Feature UI mutation path. |
| `featureAreaRetireControl` permanent row/drawer button | OBSOLETE | Move destructive lifecycle action into a Feature `⋯` menu. Do not occupy Task primary content. |
| Retire `window.confirm()` | OBSOLETE | First click must only open the WebUI confirmation dialog. No request ID or mutation may exist before Confirm. |
| New retirement UUID for every retry | SEMANTICS_CHANGED | Generate identity only on Confirm and retain the same identity for retry of the same project/Feature command. |
| Successful Retire closes Feature drawer | SEMANTICS_CHANGED | Refresh the same Feature and scoped Task projection; retain page context. |

## Initial implementation locations

- `internal/bridgehttp/ui/product-app.js`: `renderTopicRows`,
  `featureAreaRetireControl`, `reviewApprovalPanel`, `resolveReviewApproval`,
  `openTopicDetail`, `closeFeatureArea`.
- `internal/consoleapi/product.go`: `ProductTopicSummary`,
  `ProductReviewSummary`, `ListProductFeatureAreas`,
  `GetProductProjectWorkspace`, `CloseProductFeatureArea`.
- `internal/bridgehttp/product_console.go`: `closeProductFeatureArea`.
- `internal/controlapi/feature_area_lifecycle.go`: `RetireFeatureArea`.
- `internal/consoleapi/feature_area_presentation.go`: shared Feature projection
  and initial Feature-scoped Review approval adapter.

The initial Feature detail renders Review approval and Review history before
its Task list. Its Current Task field displays a technical ID from projection
details. `ProductReviewSummary` initially lacks explicit Task ownership,
ordinal, and update timestamp. The final Task detail contract must expose these
facts through the shared/product read projection rather than through browser
joins or a child-status phase reducer.

The initial retirement handler validates the returned Feature ID and lifecycle,
reloads the workspace, validates the readback, then closes the detail drawer.
The backend adapter checks project and request ID and calls the authoritative
lifecycle service. Its comment mentions capability rereading, but the method
body itself delegates to Core for write validation; no new UI authority should
be inferred from that comment.

## Regression inventory and closure requirements

`internal/bridgehttp/product_ui_test.js` initially asserts `window.confirm`,
permanent retirement buttons, and Feature-owned Review presentation. Those
expectations must be replaced with menu/dialog and Task-owned assertions.
Existing cancel, readback and error cases are useful but their confirmation
harness needs to exercise the new dialog/controller.

`internal/bridgehttp/product_git_commit_test.js` already covers scoped Feature
commit refresh, duplicate-click fencing, related Tasks, and durable retry. Keep
those semantics when moving the entry into the Feature menu.

`TestProductFeatureAreaCloseUsesAuthoritativeLifecycleAndPreservesHistory`
already covers retirement, preserved history, missing request ID, and foreign
project rejection. Keep this HTTP regression.

`TestGetFeatureAreaPresentationMatchesFacadeAndRejectsForeignProject` already
checks shared presentation parity and project fencing.

The new `internal/bridgehttp/product_retire_test.js` exercises the standalone
retirement controller:

1. Open and Cancel generate no command identity and perform no mutation.
2. Repeated Confirm submits once and remains busy through awaited refresh.
3. Transport failure, Cancel and reopen retain the same command identity.
4. Successful mutation followed by failed readback retries only the readback.
5. Late completion cannot close another Feature confirmation; project identity
   is part of the command key.
6. Reopening the same Feature while its request is pending cannot duplicate the
   write.

The integrated native dialog additionally needs Escape, focus containment and
focus return checks, along with 320/560/850/wide viewport and light/dark overflow
checks. The preexisting result modal only handles Escape and cannot be assumed
to satisfy retirement confirmation accessibility.

## Local headless integration verification

The implemented production assets were subsequently exercised using
`internal/bridgehttp/testdata/task_owned_visual_host.mjs` and
`internal/bridgehttp/testdata/task_owned_responsive_check.mjs`. The fixture binds
only to loopback, serves frozen DTOs, and implements only an in-memory retirement
receipt; it never opens a live database or calls a runner. Browser routing rejects
all non-fixture URLs. Headless Chrome uses a new isolated temporary profile.

All eight combinations passed: widths 320, 560, 850, and 1440 in light and dark
themes. Checks include 24 exact Task detail opens, phase/status presentation,
approval and acceptance actions, collapsed histories, layout bounds, visible
compact navigation icons, retirement menu ownership, zero request IDs and writes
before Confirm, Tab/Shift+Tab containment, Escape and focus return, Cancel with
zero writes, one Confirm write, and refresh preserving the Feature detail.

The checks found and verified fixes for Feature-list horizontal overflow,
native-dialog Tab escaping into browser chrome, and clipped compact-navigation
labels without icons. Final screenshots were inspected for the narrow dialog,
dark Task detail, and wide Task list. The local run writes 24 screenshots and
`results.json` under `/private/tmp/webui-task-owned-responsive` by default.

Run with an existing local Playwright installation and browser executable:

```sh
NODE_PATH=<local-node-modules> CHROME_EXECUTABLE=<local-chrome-executable> \
  node internal/bridgehttp/testdata/task_owned_responsive_check.mjs
```

Set `WEBUI_SCREENSHOT_DIR` to select another local screenshot directory. No
dependency download or external network request is part of this check.
