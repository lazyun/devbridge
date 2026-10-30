# Official App Server integration closure evidence

## Baseline and scope

Starting branch: `dev_20260908_1913`.
Starting HEAD: `f50adce9b1d634f74b3290f773bb0fd7f6ef7d21` (clean).
Implementation branch: `codex/official-app-server-integration`, isolated worktree.
Source schema was v39 (dynamic models), so this change adds v40. The historical
v38 expectation was not used to overwrite the existing migration.

Production port 8081, live SQLite, and formal runtime were not modified. No push
or Release was performed. Commit/final HEAD are reported in the delivery message;
this document is part of that commit, so it does not embed its own circular hash.

## Exact protocol evidence

Installed executable: `/Users/phoenix/.nvm/versions/node/v23.6.0/bin/codex`.
Version: `codex-cli 0.149.0`.
Generated JSON + TS: 2026-09-10T00:43:42.540443+00:00.
Separate protocol schema version: not advertised.

| Generated artifact | SHA-256 |
| --- | --- |
| ClientRequest.json | `1024db9d23b156e04b020a5d101982841dab158b84ecc9ecec399ab119ac9a1f` |
| ServerRequest.json | `e5ae9078cddac333262ae392fe25558d6054b77e4beb00784b77cf606849d063` |
| ServerNotification.json | `be5e648ebf969cba14befd535a20463befb0ea99ae03a923471547b7f473161e` |

The generated manifest contains 10 server requests, 75 notifications and 18 item
variants. The reduced committed contract was regenerated independently with
`scripts/extract-agent-runtime-protocol.py` and compared as parsed JSON: identical.
The full JSON/TS generation remains temporary evidence, not a committed vendor dump.
Official reference: [Codex App Server](https://developers.openai.com/codex/app-server/).

## Notification implementation matrix

All paths were followed through stdio -> JSONL parser -> arrival dispatcher ->
exact Coordinator-bound session -> SQLite -> shared projections.

| Method/group | Baseline | Closure |
| --- | --- | --- |
| turn/started | Existing Turn lifecycle/pre-response tracking | Durable telemetry after exact Run attachment |
| turn/completed | Existing terminal and usage path | Preserved finalization; bounded official error details retained |
| turn/plan/updated | No durable shared plan projection | Latest official plan/revision/actual count |
| turn/diff/updated | No shared bounded projection | Agent-reported tail + original size; no Git authority |
| item/started, item/completed | Command/final-message handling | All current item variants; bounded lifecycle/metadata |
| command outputDelta | No durable output tail | Ordered tail, byte count, activity timestamp |
| warning/configWarning/error | Transport handling/logging | Shared timestamped error/warning facts |
| serverRequest/resolved | Existing approval handling | Exact durable confirmation/cleanup for runtime requests |
| tokenUsage/updated | Epoch-aware accounting | Existing attribution, counters and terminal accounting preserved |
| Other/unknown | Transport remained extensible | Correlated activity, bounded unknown diagnostics; no crash |

## Server-request implementation matrix

| Exact generated method | Baseline | Closure |
| --- | --- | --- |
| item/commandExecution/requestApproval | Legacy runtime approval broker | Durable intent/dispatch/confirmation; network context and proposed amendments |
| item/fileChange/requestApproval | Legacy runtime approval broker | Exact official response and read-only ceiling |
| item/permissions/requestApproval | Existing approval semantics | Requested-subset validator; deny/depth preservation; turn/session |
| item/tool/requestUserInput | Unsupported | Exact questions/options/other answers and explicit wait state |
| mcpServer/elicitation/request | Unsupported | Form + URL, official actions, server-schema validation |
| item/tool/call | Existing dynamic tools | Retained bounded handler, configured capability only |
| applyPatchApproval, execCommandApproval | Safe legacy response | Retained explicit safe denial |
| account/chatgptAuthTokens/refresh | Unsupported | Explicit error; no credentials exposed or autoaccepted |
| attestation/generate | Unsupported | Explicit error; capability stays disabled |

OpenAI form elicitation is intentionally unadvertised. Uncorrelated elicitation,
unknown methods and over-capacity input are explicit unsupported/error paths.
See the canonical integration document for Plan/Item contracts, bounded persistence,
last activity, exact resolution identities, replay, loss and cancellation semantics.

## Candidate acceptance

`scripts/official-app-server-acceptance.py` runs a production Core binary, a disposable
repository and DataRoot, an ephemeral loopback port, and an actual stdio App Server
fixture process. It exercises real Coordinator claims, session/project binding,
migrations, SQLite, Web JSON and HTTP resolution, GPT MCP resolution, and Card
projection. Every pending scenario has five actual official plan entries, a current
command item and nonzero ordered output. The Run remains RUNNING while waiting.

| Scenario | Run result | Request result / boundary |
| --- | --- | --- |
| command/network approval via Web HTTP | SUCCEEDED | CONFIRMED_RESOLVED |
| file approval via MCP | SUCCEEDED | CONFIRMED_RESOLVED |
| permissions via MCP | SUCCEEDED | CONFIRMED_RESOLVED |
| user input via MCP | SUCCEEDED | CONFIRMED_RESOLVED |
| MCP form elicitation via MCP | SUCCEEDED | CONFIRMED_RESOLVED |
| command cancel through canonical supervisor | CANCELED | Official interrupted + cleanup |
| RETIRE while waiting | CANCELED | Existing retirement cancellation + cleanup |
| transport lost while waiting | INFRASTRUCTURE_FAILED | CONNECTION_LOST, no resolution; boundary UNKNOWN, quiesced=false |

Card projection was checked for the exact Run and five-step plan. Web workspace JSON
contains the same state, and the actual Web resolve handler continued the Run. The
in-app browser returned ERR_BLOCKED_BY_CLIENT for the candidate loopback URL; no live
screenshot or visual-click acceptance is claimed. Rendered DOM/escaping/available
buttons are covered by the repository's JS tests. No browser safety override was used.

## Real installed Codex smoke

A separate subprocess with temporary CODEX_HOME and an isolated read-only thread
completed initialize, initialized, thread/start and thread/read. The JSONL reader
parsed thread/started and remoteControl/status/changed without protocol failure.
Schema generation used the same installed executable.

`REAL_CODEX_PLAN_NOT_RUN` and `REAL_CODEX_APPROVAL_NOT_RUN`: no paid model Turn was
started. Real-model-generated plans/approvals are not claimed. Full request/telemetry
flows instead use deterministic stdio fixtures plus the production candidate.

## Review findings and limits

The final review checked all 20 requested authority, ordering, response, replay,
UI, cancellation, RETIRE, usage and deferred-scope points. Concrete issues found
and fixed during candidate/review work:

- Request JSON-RPC IDs were initially inferred by MCP as byte arrays; explicit
  string/integer input and complete closed output schemas now match the contract.
- A typed nil facade error initially made successful responses appear unavailable;
  success now returns a true nil error.
- Official cancel could beat canonical interrupt claim; outbox dispatch now fences
  that race and uses existing interrupt cleanup.
- A late server request must match the observed method and payload as well as ID,
  so a previous Turn cannot consume the current Turn’s waiter after ID reuse.
- Response validation now occurs under the wire write lock; stale queued answers
  cannot bypass a later cancellation or terminal cleanup observed before dispatch.
- Asynchronous Web answer readback is fenced by Project and drawer generation.
- Unknown permission fields cannot be echoed into grants; requested deny entries
  and filesystem traversal limits survive subset selection.
- Request identities remain retained for the entire bounded Turn so replay cannot
  create a new answerable identity after history eviction.
- Shutdown errors no longer falsely label a lost pending request as rejected.

No known new P0/P1/P2 remains after the final gates. Existing token counters, runtime
approval accounting, finalization, cancellation, UNKNOWN_ORPHANED and RETIRE protocols
were retained. The old v39-specific migration test now explicitly stops at v39 rather
than asserting that all future migrations add no triggers. A stale pre-existing JS
model-label expectation was aligned with the already-present dynamic-model behavior;
production model selection behavior was not changed.

Operational limits: bounded per-Turn request/metadata capacity; conservative
structural permission subset validation; old request IDs are read-only after restart;
no automatic reconnect/resend; no real paid approval Turn; browser visual QA unavailable.
Read-time projections can be refreshed through existing user actions, but no polling
or stale watchdog was added. Future Codex versions require schema-drift review.

## Validation record

| Gate | Result |
| --- | --- |
| Full `go test -p 2 ./... -count=1` | PASS |
| Codex client/parser/transport and real SQLite fixtures | PASS |
| Coordinator/supervisor, cancellation, UNKNOWN_ORPHANED, RETIRE regressions | PASS in the full suite |
| Control API, Console API, HTTP, GPT MCP, Local MCP, workflow presentation | PASS |
| Focused latest plan/item terminal and response/loss `-race` fixtures | PASS |
| Affected package `go test -race` | PASS; all listed affected packages, plus focused latest-change rerun |
| `go vet -p 2 ./...` | PASS |
| Production Core / GPT MCP / Local MCP builds | PASS |
| Web/Console/Card DOM and transport JS tests | PASS, 122 tests |
| Production ChatGPT UI asset build | PASS, two self-contained assets; Web assets embedded in production Core |
| Changed Go `gofmt -l`, `git diff --check` | PASS |
| Installed schema subset reproduction | PASS, parsed JSON identical |
| Real installed App Server smoke | PASS, no paid model Turn |
| Isolated candidate A–G plus file/elicitation/cancel/RETIRE | PASS |

Logs and fixture JSON were generated
in task-specific temporary paths. Initial full-test attempts exposed a sandbox loopback
restriction and later local disk exhaustion; required loopback permission was granted,
only this task's cache was cleared, and build parallelism was reduced for the rerun.
The final result is based on rerun outcomes, not those failed attempts.
