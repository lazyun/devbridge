# Decision Card collision regression

The reported legacy request ID is `card-e1b4c7e8-confirm`. The supplied Final
Re-review request does not include the original two Decision identities. These
reconstructed, valid identities reproduce that exact 32-bit FNV-1a hash:

- `TASK_CONFIRMATION:collision-a-iDPqOb`
- `TASK_CONFIRMATION:collision-b-BrSLjc`

The Node regression executes the complete app through its JSON-RPC message-host
boundary and checks the exact legacy collision, receipt identity and option
validation, deterministic conflict retry, response-lost replay, hydration, and
fresh remount. It does not replace the app's render or action functions.

The new ID is `card-v2-<64 lowercase SHA-256 hex digits>-<option>`. The digest
input is UTF-8 JSON encoding of the ordered array
`["devbridge-decision-card-v2", decisionId, option, rejectedRequestId]`, with an
empty final string for the initial request. AI session and browser mount are
intentionally absent. A definitive conflict uses the rejected request ID as the
last element for the next user retry. An unknown outcome retains the same ID.
App-private `ui_decision_get` projects the current per-option IDs by walking
existing durable request bindings with this same chain. A binding to a different
Decision or option is a definitive rejection of that payload. Hydration walks
at most 32 candidates and fails closed on lookup failure or exhaustion. These
are read-only lookups; no new persistence, request reservation, or resolver is
introduced.

This reconstructs the prior rotated ID even when that attempt is still in
flight and fresh hydration therefore returns `PENDING`. The first retry after
remount uses that same rotated ID. There is no automatic resubmission and no
browser-local persistence.

`TestDecisionCardCollisionThroughFreshMCPTransport` additionally reads the
embedded app over MCP, runs it against real application-created pending
Decisions, forwards every app call over fresh HTTP MCP transports to the SQLite
resolver, injects the shared legacy request ID, drops a committed response,
replays it, and hydrates a fresh Card with a fresh AI session. The final database
assertions require exactly one receipt and pending Execution per Decision and no
Execution Run. All data and repositories belong to an isolated fixture.

`TestDecisionCardCollisionRotatedUnknownRemountThroughFreshMCPTransport`
hydrates B, then has a valid caller bind B's base-v2 ID to A. B receives a real
conflict, rotates, and its outgoing retry is held at the message-host boundary.
After timeout a fresh session still reads pending B. Its first remounted Card
submission uses the identical rotated ID; later delivery of the original held
request replays that same receipt. Node also covers this composed state path.

Run:

```sh
node --test internal/mcpserver/decision_ui_test.js
go test ./internal/localmcp -run TestDecisionCardCollision -count=1
```

Node and permission to bind ephemeral loopback ports are required for the live
transport proof. This harness proves actual app/message/MCP/durable behavior;
it does not claim a visual browser screenshot or a real ChatGPT host session.

## Active Execution cancellation surface

The complete-app Node regression also verifies that active Implementation and
Verification progress Cards have no legacy **Cancel Task** action, provide chat
guidance, and retain pending proposal **Cancel** / **Execute** actions.
`TestAppPrivateExecutionDecisionRejectsLegacyRunCancellationThroughFreshMCP`
opens fresh HTTP MCP sessions and checks the live private tool schema and stale
Run-only/Run-plus-proposal calls. They fail closed without invoking the legacy
runner or rejecting a proposal; a valid pending proposal cancellation still
rejects exactly once without starting a Run. The fake service counters isolate
the transport routing assertion. Formal active cancellation remains owned by
the shared `execution_cancel` operation.
