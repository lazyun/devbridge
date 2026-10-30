# Codex App Server integration

Codex App Server telemetry != Dev Bridge workflow authority.

Plan/item events only describe Codex activity. All Task/Run/Execution terminal
decisions remain under Dev Bridge authoritative finalization. An agent runtime
request is an input entity for an exact backend invocation, not a Development
Decision, Task, claim, or new cancellation workflow.

## Installed protocol contract

The integration was verified against `codex-cli 0.149.0`, generated on
2026-09-10 at 00:43:42 UTC using the installed executable's
`app-server generate-json-schema` and `app-server generate-ts` commands. No
separate schema protocol version was advertised. Generated schemas take precedence
over documentation spelling; the input method is `item/tool/requestUserInput`.

`internal/agentruntime/protocol.json` contains only five consumed request/response
schemas and method/item manifests. It preserves the generated constraints while
removing descriptions/titles. `scripts/extract-agent-runtime-protocol.py` reproduces
this subset from a fresh generated directory. Do not regenerate or enable new
capabilities implicitly when the executable changes. Review schema drift first.
The existing historical `api/codex-schema` vendor is not replaced by a full dump.

Initialize continues to enable experimental API only when the existing dynamic-tool
handler requires it. `mcpServerOpenaiFormElicitation` and `requestAttestation` are not
advertised. The generated capabilities also include `extensions` and
`optOutNotificationMethods`; this change does not activate them.

## Transport and ownership

The existing bidirectional JSONL transport retains its independent reader, bounded
request workers/queues, and separate response writer. A server request arriving
before the response to `turn/start` cannot block the reader. Requests are observed
on arrival before entering the worker queue, independently of the public
notification queue. Notifications and requests are folded under one session mutex.

`Client.StartTurn` buffers observations until the existing Coordinator's `OnStarted`
callback durably binds the exact Run/Thread/Turn. Only then may telemetry bind.
The identity includes Project, FeatureArea, Task, universal AgentExecution, Run, Thread, Turn, and a random
connection ownership nonce for this exact Turn invocation. This nonce is not a PID.

SQLite writes verify runtime epoch, current Run/claim authority, conversation,
Thread and Turn, then compare the monotonically increasing snapshot sequence.
Snapshot identity cannot be reassigned. Late old-Turn events never update the current
plan, item, or waiting state. After an official terminal only corresponding
`serverRequest/resolved` cleanup is accepted. There is no replay to a new connection.

## Notifications and activity

| Method | Persisted observation | Authority |
| --- | --- | --- |
| `turn/started` | Exact Turn in progress, activity timestamp | Existing durable Run binding |
| `turn/plan/updated` | Latest complete plan snapshot, revision, completed/total | None |
| `item/started`, `item/completed` | Item lifecycle, status, timestamps, bounded metadata | None |
| `item/commandExecution/outputDelta` | Arrival-ordered byte count and output tail | None |
| `turn/diff/updated` | Bounded agent-reported diff and original byte count | Never Git/evidence truth |
| `warning`, `configWarning`, `guardianWarning`, `deprecationNotice` | Timestamp, summary, bounded exact details | None |
| `error` | Timestamp, summary, official error details | None |
| `turn/completed` | completed/interrupted/failed, full bounded official failure | Existing Coordinator terminal observation input |
| `serverRequest/resolved` | Exact request confirmed cleared/resolved | Request cleanup only |
| `thread/tokenUsage/updated` | Activity plus existing token-accounting path | Existing usage semantics unchanged |
| Other generated Turn/item notifications | Activity; lifecycle remains sourced from `item/*` | None |
| Unknown correlated notifications/items | Bounded diagnostic or unsupported envelope | No reader crash or workflow mutation |

The plan status enum is `pending / inProgress / completed`. Total is always the
number of official entries, including zero. Missing plan means NOT_REPORTED, never
a plan synthesized from Task goals. All steps completed does not complete a Run.
Plan steps have no Task ID, claim, Decision, or cancellation authority.

The item manifest covers userMessage, hookPrompt, agentMessage, plan, reasoning,
commandExecution, fileChange, mcpToolCall, dynamicToolCall, collabAgentToolCall,
subAgentActivity, webSearch, imageView, sleep, imageGeneration, enteredReviewMode,
exitedReviewMode, and contextCompaction. Reasoning content is not retained; only its
lifecycle and id/type metadata are stored. Command metadata retains command, cwd,
actions, status, exit code, duration and bounded aggregated output. File metadata
retains changed paths, kinds, diff byte counts and truncation indicators. Full Git
and verification evidence continue to use existing mechanisms.

## Requests and responses

| Generated server method | Handling |
| --- | --- |
| `item/commandExecution/requestApproval` | Durable exact input; official command decisions and proposed rule amendments |
| `item/fileChange/requestApproval` | Durable exact input; official accept, acceptForSession, decline, cancel |
| `item/permissions/requestApproval` | Durable requested permissions; only a structural requested subset with turn/session scope |
| `item/tool/requestUserInput` | Durable questions/options/isOther/isSecret/timing fields; exact question-keyed answers |
| `mcpServer/elicitation/request` | Exact correlated form/url; accept, decline, cancel; form content validates requestedSchema |
| `item/tool/call` | Existing bounded dynamic-tool handler when configured; otherwise explicit protocol error |
| `applyPatchApproval`, `execCommandApproval` | Existing legacy safe-denial response path; no new automatic acceptance |
| `account/chatgptAuthTokens/refresh` | Explicit unsupported JSON-RPC error; no credential broker added |
| `attestation/generate` | Explicit unsupported JSON-RPC error; capability not advertised |
| Unknown methods | Explicit -32601/error diagnostic; never silently wait |

Command/file decisions are drawn from the installed response enum. The installed
stable command params do not declare `availableDecisions`; if a server supplies an
explicit restricted list, Bridge honors it. Exec-policy and network-policy amendment
choices must exactly match the server's proposals. Read-only Review ceilings suppress
permission/command/file grants as before. A networkApprovalContext is displayed as
“Network access request” using its host/protocol. This installed schema has no port
field; no port is inferred from shell text.

Permission grants may not add paths, access modes, network access, or unknown future
permission fields. Deny entries and traversal limits cannot be dropped while granting
filesystem access. Structural subset validation deliberately rejects equivalent
rewritings that it cannot prove safe. Empty permissions deny the grant. UI provides
full requested/empty choices; MCP supports validated partial subsets. No unavailable
permission is invented.

User input uses exact question IDs and offered labels unless free-form/other is
allowed. Supported timing fields remain visible, but Bridge does not auto-answer.
MCP form responses validate the generated response and server-provided form schema;
URL responses carry the official action without invented form content. URL links
permit only HTTP(S). Uncorrelated requests and `openai/form` are explicitly rejected;
Bridge does not advertise the openai/form capability.

## Durable lifecycle and cancellation

Normal response flow:

`PENDING -> INTENDED (durable) -> SENT (transport written) -> CONFIRMED_RESOLVED`

INTENDED and SENT remain waiting for official confirmation. Resolution checks the
Project-bound AI session, all observed identity fields, request pending state,
response schema/options, live connection owner, cancellation/RETIRE/finalization
fences. Duplicate or stale answers fail closed. The request waiter additionally verifies the exact observed method and payload,
so a late old-Turn request cannot consume a current answer after ID reuse.
The exact original JSON-RPC request ID is written to the owning transport. Request IDs use canonical string/integer
identity comparisons; they are never converted through floating point.

The writer rechecks ownership after acquiring the actual transport write lock.
A queued answer cleaned by an interrupt or fenced by another authority is not sent.
Persistence failures close the transport through the existing failure path rather
than leave an invisible wait. Delivery labels distinguish NOT_SENT, DISPATCHING,
TRANSPORT_WRITTEN, NOT_SENT_FENCED, and RESPONSE_UNCERTAIN.

Command/file `cancel` first admits cancellation through the existing supervisor.
An official cancel response is eligible only once that supervisor has claimed its
canonical interrupt. If it has not, the queued answer is fenced and the canonical
interrupt performs official cleanup. This preserves existing interrupted-terminal
and completion-vs-cancellation semantics without introducing a second Codex cancel.
RETIRE continues to use the same supervisor and existing finalization transaction.

Official terminal cleanup closes still-open requests as CLOSED_TURN. Official
`serverRequest/resolved` confirms the request was answered or cleared, not necessarily
that a local approval was applied. Missing confirmation after transport loss remains
CONNECTION_LOST with any intended response retained. It is never rewritten to accepted,
declined, or a fabricated Turn terminal. Existing UNKNOWN/recovery/UNKNOWN_ORPHANED
rules continue to decide backend disposition and claim release.

After restart, history remains readable. An old connected snapshot projects
ABANDONED_RESTART (or ABANDONED_RUNTIME after ownership ends); read operations do not
mutate history or resend. No new connection can resolve an old ownership nonce.

## Storage limits and projections

Migration v40 adds `agent_runtime_states` and `codex_server_requests`, their index,
and immutable identity trigger. Actual source baseline was already v39; v38 is a
historical reference. No existing Run or history row is rewritten. Old Runs return
telemetry UNAVAILABLE.

Per Turn: at most 256 plan entries, 128 item snapshots, 128 request identities,
16 diagnostics, 64 KiB per request/metadata/plan payload, and 16 KiB output/diff tails.
Prebinding is limited to 256 events/1 MiB. Old request identities are never evicted
within a Turn: exceeding capacity produces an explicit diagnostic/protocol error,
preventing an old answer from targeting a replayed request. Output cannot cause
unlimited SQLite blob growth. Client snapshot payloads are bounded; writes store the
latest snapshot, not each token/reasoning delta as a permanent event record.

`operation_get(include=["ACTIVITY", "RUNTIME_REQUESTS"])` and the typed
`runtime_request_respond` are registered in GPT MCP and Local MCP. Exact operation
targets come from `workspace_get` or `work_list(kind="OPERATION")`. Core also serves GET `/v1/runs/{id}/agent-runtime` and POST
`/v1/runs/{id}/agent-requests/resolve`, with AI-session scoping. Local operator Web uses
POST `/ui/api/runs/{id}/agent-requests/resolve` and existing trusted session binding.
All transports return the same neutral snapshot and exact identity.

Run/Console JSON, Task latest Run, and Execution detail expose plan, current item,
last activity, warnings, errors and WAITING_FOR_COMMAND_APPROVAL /
WAITING_FOR_FILE_APPROVAL / WAITING_FOR_PERMISSION / WAITING_FOR_USER_INPUT /
WAITING_FOR_MCP_ELICITATION. These are telemetry wait reasons, not Task statuses.
Web escapes all agent text/output and renders only offered actions. The Card exposes
a compact summary with completed/total and active step or wait reason, no full forms.
Task definition goals remain definitions without fabricated execution statuses.

No custom report_progress/progress_plan_set, PID inventory, watchdog, automatic
stalled status, automatic cancellation, polling UI, or Desktop progress toggle is
introduced. Reads are pure; last activity is a fact, not a timeout policy.

The current durable chain is [Task → AgentExecution → Run](agent-execution-ownership.md).
ExecutionProposal is only the DEVELOPMENT authorization subtype; Review,
Review and Inspection also own universal Executions with their original semantic authority.
