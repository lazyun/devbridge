# DEV_BRIDGE_CARD_ASYNC_ACTION_AUTHORITY_REVALIDATION_CLOSURE

Baseline: actual `dev_20260908_1913` HEAD is
`4ca8a00adf4f8a81095ef83c194baeddbeb101ae`, exactly matching the supplied
historical baseline. Implementation is isolated on
`codex/DEV_BRIDGE_CARD_ASYNC_ACTION_AUTHORITY_REVALIDATION_CLOSURE`.

This is the bounded remaining P2 closure identified by v0.1 Global Final
Acceptance. No new global architecture review was performed.

## Closed invariant

A capability action may submit a mutation after asynchronous preprocessing only
while the complete authority captured by its rendered button remains current:

- AI session;
- selected FeatureArea and selected Task;
- navigation generation;
- accepted workspace object and presentation object;
- presentation FeatureArea/Task identity and capability target FeatureArea;
- the active action token for that exact invocation.

Navigation generation alone is not sufficient. A same-navigation Refresh can
accept a replacement workspace and presentation without changing generation.
That replacement invalidates every action already waiting on the prior
presentation.

## Implementation boundary

The Card reuses its existing render-time authority object and complete authority
predicate. A single action-run predicate additionally requires that the exact
invocation still owns the active action token.

Task Confirm and Revise revalidate this predicate immediately after Decision
hydration and before `ui_task_decide`. Execution Cancel and Recover revalidate it
immediately after SHA-256 request identity derivation and before
`execution_cancel` or `agent_operation_recover`. Direct capability mutations have
no asynchronous preprocessing boundary between their entry validation and tool
submission.

The same predicate fences post-action subject readback and reprojection. Once a
replacement workspace invalidates an invocation, it cannot issue reads on behalf
of the prior presentation either. Normal current-authority mutation, readback,
retry-safe request identity, and reprojection behavior are unchanged.

No Core workflow schema, mutation contract, durable state, backend lifecycle, or
navigation-generation semantics changed.

## Regression coverage

Deterministic Card tests hold each asynchronous preprocessing result, accept a
W2 workspace for the same session, FeatureArea, Task, and generation, then
release the W1 result. They cover:

- Task Confirm after Decision hydration;
- Task Revise after Decision hydration;
- Execution Cancel after stable request digest derivation;
- Recover after stable request digest derivation.

Every case proves that no stale W1 mutation or subject readback is sent and that
the accepted W2 remains rendered and actionable where applicable. Existing
successful Task decision and retry-stable Execution cancellation paths remain
covered.

## Validation

- `go test ./... -count=1`
- `go test -race ./internal/mcpserver ./internal/controlapi -count=1`
- `go vet ./...`
- isolated-cache `go build -trimpath -o /private/tmp/devbridge-card-async-authority-bin/ ./cmd/...`
- `node --test internal/mcpserver/*ui_test.js` (46/46)
- `node --test internal/bridgehttp/*ui_test.js` (49/49)
- `node scripts/build-chatgpt-ui.mjs /private/tmp/devbridge-card-async-authority-ui-build`
- `git diff --check`

All passed. No live product runtime was restarted or replaced, and no Global
Final Acceptance re-review was run.
