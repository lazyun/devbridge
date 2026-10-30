> Historical validation record; not a current workflow contract. See the [final v0.1 model](task-execution-v0.1.md).

> Historical implementation evidence. [GPT surface consolidation V1](gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

> Current live-read and MAIN lease contract: [Review observability and Card live update](review-observability-and-card-live-update.md).

# DEV_BRIDGE_UNIFIED_WORKSPACE_CARD_NAVIGATION_CLOSURE

Baseline: actual `dev_20260908_1913` HEAD was
`75868398b374ca6fd12a89afbf0f6a805f24b054`, matching historical reference
`7586839`. Implementation is isolated on
`codex/dev-bridge-unified-workspace-card-navigation-closure`.

## Product surface

ChatGPT has one long-lived main resource, `ui://devbridge/card`, titled
**Dev Bridge Card**:

```text
Project
  -> Development Branch
  -> FeatureArea
  -> Task
  -> Progress + Decision
```

The existing `development_context_card_show` and `feature_area_card_show` entry
tools remain compatible, but both mount this same resource. No command or
auxiliary read mounts another main Card. Pre-session Project selection and the
Project-bound workspace therefore continue in one Card instance.

Development Branch controls retain the existing durable select/create commands.
FeatureArea and Task selectors are Card-local navigation: changing them writes no
Bridge state and grants no workflow authority. The selected IDs are persisted only
in host widget state so remount can repeat an exact pure read.

## Pure workspace projection

The app-only `dev_bridge_card_get` read composes:

- the authoritative Development Context;
- Project-owned FeatureArea inventory;
- Project-owned Tasks filtered by the exact selected FeatureArea;
- either the exact FeatureArea presentation or the exact selected Task's current
  operation graph.

No FeatureArea is inferred when none was selected. A Task ID is accepted only
with a FeatureArea ID and only when the Task is owned by that FeatureArea. Task
navigation uses `CurrentWorkflowForTaskForSession`, so a newer Task does not
replace an explicitly selected older Task. The snapshot wrapper also preserves
the established pure-read guarantee: Refresh cannot materialize terminal Task
Preparation or claim a continuation.

The projection reuses the existing mechanical Progress, pending Decision,
Cancellation, Recovery, blocker, and exact capability model. Decision clicks
retain the existing sequence: mutation, exact authoritative subject readback,
then a fresh workspace projection. Response loss never becomes local success.

Navigation action authority is further closed by
[DEV_BRIDGE_CARD_NAVIGATION_ACTION_STALENESS_CLOSURE](dev-bridge-card-navigation-action-staleness-closure.md):
changing FeatureArea or Task synchronously removes the prior actionable
projection, and every rendered action is bound to its exact accepted navigation
and workspace projection.

## Semantics intentionally unchanged

Project binding, Development Branch durability, FeatureArea lifecycle, Task
selection by authoritative lineage, operation claims, Decision receipts,
Execution authorization/cancellation, Recovery, and runtime behavior are not
changed. No external GPT workflow schema is modified. The new navigation read is
app-only and presentation-only.

The independent FeatureArea Details resource remains available for explicit
history inspection; it is not used to infer Card selection or workflow state.

## Validation

- `go test ./...`
- `go test -race ./internal/mcpserver ./internal/controlapi`
- `go vet ./...`
- `go build -o /private/tmp/devbridge-unified-card-bin/ ./cmd/...`
- `node --test internal/mcpserver/*ui_test.js` (40/40)
- `node scripts/build-chatgpt-ui.mjs <temporary-output>` (two self-contained assets)
- `git diff --check`

All passed. No live product runtime was restarted or replaced.
