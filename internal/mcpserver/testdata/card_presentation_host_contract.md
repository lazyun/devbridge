# Dev Bridge presentation host contract

This optional extension is a presentation protocol, not an MCP workflow tool.
Existing hosts without it keep automatic recall unavailable (the preference can
still be saved). The repository contains an implementation in the isolated
`card_visual_host.mjs`; a production ChatGPT host must implement the same contract.
No production host integration is claimed by these fixture tests.

Initialization advertises:

```json
{"hostCapabilities":{"devBridgePresentation":{"visibility":true,"recall":true}}}
```

After accepting a workspace identity, the Card sends a JSON-RPC notification
`ui/notifications/dev-bridge-presentation-subscribe` with `{presentationId}`.
The host must report the initial state and subsequent transitions through
`ui/notifications/dev-bridge-presentation-visibility`, with
`{presentationId, visibility: "VISIBLE" | "OFFSCREEN" | "HIDDEN"}`. Messages must
come from the iframe's parent and match its current identity. The host measures
the iframe in the outer page viewport; any intersection is VISIBLE. Hidden
pages/removed or hidden presentations are HIDDEN, never OFFSCREEN. Hosts must
only enable this capability if they can reliably deliver these transitions.

The Card requests `ui/dev-bridge/recall-presentation` with `{presentationId}`.
The host focuses/scrolls the existing registered presentation into view and
responds with a JSON-RPC result. Unknown identities must fail, not mount a new
Card. Acceptance is not visibility confirmation: only a subsequent VISIBLE
telemetry event resets the consumed epoch. Never call a show tool, append an
iframe, change selection, or mutate the presentation lease to satisfy recall.

The Card owns a fixed 10-second timer. It requires an enabled preference, a
current Project running-development fact, and OFFSCREEN telemetry. At the
deadline it performs a silent workspace read, rechecks identity, running Task
set, preference, visibility and epoch, then makes at most one request. Failure
or timeout is also a consumed attempt. HIDDEN, task changes, disabling, and
teardown cancel pending timers. HIDDEN also invalidates an unconsumed epoch:
subsequent OFFSCREEN events cannot start another timer until VISIBLE rearms it.
Only VISIBLE rearms a consumed or HIDDEN-invalidated epoch. Canceled callbacks
are fenced before starting a read and again after the deadline's fresh read.
Enabling while offscreen in a still-valid epoch starts a new full delay;
changing the running Task set also starts a new full delay without rearming a
consumed or HIDDEN-invalidated epoch.

Preferences use the existing host widget state:
`privateContent.cardPreferences[JSON.stringify([aiSessionId, projectId])].autoRecallRunningTask`.
The default is false. Host storage failure falls back to mount-local preference
and displays that limitation. Neither Task nor Feature selection scopes it.

`featureAreas[].runningDevelopmentTaskId` is an optional read-only presentation
fact. It is computed from existing project-scoped workflow guidance for active
FeatureAreas, matching the current Task to a DEVELOPMENT Task, and only for
IMPLEMENTATION_RUNNING or VERIFICATION_RUNNING. Inventory read errors omit the
fact. It survives selected-Task filtering. This adds no durable schema/entity.

Offline validation:

- `node --test internal/mcpserver/dev_bridge_ui_test.js`
- `node internal/mcpserver/testdata/card_visual_host.mjs`
- Set `CARD_HOST_URL` to the printed loopback URL; run
  `node internal/mcpserver/testdata/card_recall_visual_test.mjs` with an installed
  Playwright (`PLAYWRIGHT_MODULE`) and optional isolated `CHROME_PATH`.
- `go test ./internal/mcpserver ./internal/controlapi ./internal/workflowpresentation -count=1`
- `go vet ./internal/mcpserver ./internal/controlapi ./internal/workflowpresentation`

## Current workspace-show flow assertions

This fixture contract does not create a second presentation protocol. A host receives
one explicit show result with an exact Session/Project/target identity and may mount
that projection directly. A valid initial projection is not an instruction to issue a
second full `workspace_get` before first paint. A later user expand can perform one
component-local exact fresh read; it never calls `workspace_show` or sends a natural
language message that asks GPT to select an action target again.

Background refresh must preserve expansion, scroll position, compatible drafts and the
last good snapshot. It does not display full-card loading, disable input, close a
popover, claim cancellation, or restore stale authority. Manual refresh, navigation,
retry and action readback are interactive: they block duplicate pointer/keyboard
submits until their read is settled. Older background responses cannot replace a newer
target or clear an interactive wait.

The host must treat `required` as a Bridge caller convention, not as a native rendering
command. It cannot prove that a returned result mounted, became visible, or that GPT
acted on a follow-up. A completed review updates an already-mounted exact component in
place and preserves collapsed/expanded state. A host that cannot offer UI leaves the
normal text and canonical business workflows available.
