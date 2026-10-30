# DEV_BRIDGE_CARD_NAVIGATION_ACTION_STALENESS_CLOSURE

Baseline: actual `dev_20260908_1913` HEAD is
`bac66885e08dcfe38d95949d5aa9b2022b09b29c`, exactly matching the supplied
historical baseline. Implementation is isolated on
`codex/dev-bridge-card-navigation-action-staleness-closure`.

## Closed invariant

An action rendered by `ui://devbridge/card` is usable only while all of the
following remain the exact accepted authority that rendered it:

- AI session;
- selected FeatureArea;
- selected Task, including an intentionally empty Task selection;
- navigation generation;
- accepted workspace object and its presentation object;
- presentation FeatureArea/Task identity and capability target FeatureArea.

The Card captures that authority when it renders each decision, approval,
cancellation, or recovery button. It validates the whole authority before any
action read or mutation is sent. Generation is retained for in-flight fencing,
but is no longer the proof that a button belongs to the current navigation.

The remaining asynchronous preprocessing boundary is closed by
[DEV_BRIDGE_CARD_ASYNC_ACTION_AUTHORITY_REVALIDATION_CLOSURE](dev-bridge-card-async-action-authority-revalidation-closure.md):
Task Decision hydration and cancellation/recovery request digest derivation now
revalidate the same complete render authority before submitting their mutation.

## Navigation transition

A FeatureArea or Task selection change synchronously re-renders the Card before
starting its pure projection read. The prior Development Context remains useful
as a non-authoritative shell, but its Progress, Decision, cancellation, recovery,
and action controls are not rendered. Tasks from a differently selected
FeatureArea are also hidden.

The next workspace is accepted only when its selected IDs and embedded
presentation identities match the current navigation exactly. A failed or
identity-mismatched read leaves the new navigation selected and actionless with
an explicit retry state; it never falls back to the prior projection. A normal
Refresh failure without a navigation change may continue to show the last
successful projection for that same exact selection.

Detached DOM handlers are independently fenced. Even if a host invokes a stale
button reference after navigation or after a same-navigation workspace
replacement, the handler cannot issue a tool call.

## Regression coverage

The unified Card tests exercise:

- Task A cancellation followed by Task B navigation while the B read is pending
  and after it fails;
- FeatureArea navigation against stale Confirm, Authorize, runtime Cancel,
  Execution Cancel, and Recover handlers;
- rejection of a workspace whose selected Task and embedded presentation Task
  disagree;
- invalidation of a detached action after same-navigation workspace replacement,
  where navigation generation alone does not change;
- the existing successful action/readback/reprojection paths.

No Core workflow schema, mutation contract, or backend lifecycle behavior is
changed.

## Validation

- `go test ./...`
- `go test -race ./internal/mcpserver ./internal/controlapi`
- `go vet ./...`
- isolated-cache `go build -o /private/tmp/devbridge-card-navigation-action-staleness-bin/ ./cmd/...`
- `node --test internal/mcpserver/*ui_test.js` (44/44)
- `node scripts/build-chatgpt-ui.mjs /private/tmp/devbridge-card-navigation-action-staleness-ui-build`
- `git diff --check`

All passed. No live product runtime was restarted or replaced.
