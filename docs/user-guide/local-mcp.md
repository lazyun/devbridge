# Local MCP adapter (v0.1)

Current Task/Execution authority and explicit approved Implementation start follow the [final v0.1 model](../architecture/task-execution-v0.1.md).


Dev Bridge has two independent ingress adapters. They may run at the same
time and keep independent presentation, session and decision handling, while sharing
product application logic and lifecycle/reset registration.

```text
ChatGPT   → OpenAI Tunnel → GPT adapter proxy (devbridge-mcp) ─┐
                                                              ├→ Core (devbridge) → Bridge application services
Local MCP client → Local MCP adapter proxy (devbridge-local-mcp) ┘
```

- The GPT adapter uses purpose-specific prepare tools and confirmation
  components. App-only confirmation actions do not appear in the model catalog.
- Local MCP keeps direct commands such as `project_open`, `task_create` and
  `execution_start`. It has no show tools, UI resources or ChatGPT dependencies.
  Both adapters share Core's workflow rules; their tool catalogs differ.
- Core is the only process that opens the DataRoot and holds its writer lock. Both
  adapters proxy to Core's private loopback adapter handlers and verify that Core
  reports the same canonical DataRoot.
- Both adapters bind loopback by default. Local MCP clients never route through the
  OpenAI Tunnel and no public MCP endpoint is required or created.
- Both MCP handlers reject cross-origin browser mutation requests. Native MCP
  clients and the tunnel use requests without browser `Origin` metadata.
- Bridge owns the durable workflow and the asynchronous lifecycle (Review,
  wake/resume in v0.1: when the user returns and asks "现在怎么样了？", the
  model reads `workspace_get` (or a detail read) and continues from durable
  state.

## Sessions

Each local MCP client conversation is one Bridge interaction:

1. `session_initialize({requestId})` — create the conversation's real unbound
   Session and retain its actual `aiSessionId`. A fresh intent uses a new random
   request ID; retry a lost response with the original ID.
2. Present its candidate Projects; use `project_list` for additional pages. A
   single candidate still needs the user's explicit selection.
3. `project_open({aiSessionId,projectId,requestId})` — bind that same Session to
   the explicit choice with a stable binding request ID.
4. Carry that ID in subsequent calls. `session_initialize({aiSessionId})` reads
   the actual binding before `workspace_get` and authorized engineering steps.
   Read failures never create a replacement. New conversations initialize a new
   Session; refresh, reconnect and same-Project confirmation do not.

Bridge does not store any Local-MCP-specific identity in domain objects.
Project, Feature Area, Document, Review, Task, and Execution state is durable
Project-owned state, so a ChatGPT session and a local MCP session (or two
local MCP client conversations) can work with the same Project and the same Feature Area.
Neither adapter owns a Feature Area or Task.

## Decisions

Local MCP clients have no card interaction. The user decides in natural language
("确认", "先别执行"); the local client model interprets that intent and calls
`decision_respond` with the structured choice:

```json
{
  "aiSessionId": "ai_session_…",
  "kind": "REVIEW_APPROVAL",
  "taskId": "task_…",
  "decisionId": "REVIEW_APPROVAL:review_…",
  "option": "APPROVE",
  "requestId": "client-stable-attempt-id"
}
```

- `workspace_get` projects the pending decision with the same
  exact decision identity, owner Task and valid capability targets used by the
  Task Card. Copy these targets; never construct a decision or operation ID.
- Bridge structurally validates that the decision exists, is still pending,
  that the session is authorized for the same Project, and that the option is
  valid; resolution is atomic.
- Generate one stable `requestId` per intended choice. If a response is lost or
  times out, retry the exact same Decision, option, and request ID. Never create a
  new request ID merely because the outcome is unknown.
- Decisions are durable Project workflow state: a local MCP session bound to
  the same Project may resolve a decision that another session caused.
- Concurrent resolutions commit exactly one durable transition; repeated
  Task confirmation returns its immutable resolution receipt and authoritative
  Task review gate result. Exact replay, same-option repeat, and
  conflicting repeat are distinguished and never create a duplicate Execution
  or Run.
- A workflow refresh problem after commit is reported separately from the
  durable resolution. Read `workspace_get` using the existing `aiSessionId` rather than treating
  the user's choice as failed.
- The adapter and Bridge never parse natural-language decisions; only the
  structured `decision_respond` call reaches Bridge.

`decision_respond` accepts only REVIEW_APPROVAL or TASK_ACCEPTANCE. Runtime
permissions use the separate typed `runtime_request_respond` branches and their
original retry contracts. APPROVE does not start Implementation; use
`execution_start` after explicit authorization. Read Review results with
`operation_get` using the target returned by `workspace_get` or `work_list`.

For a newly approved Coding Agent definition, read `workspace_get` with the exact
Task scope and `include: ["RUNTIME_CONFIG"]`. Use the returned runtime options and
Task options' backend candidates, including their `configFingerprint`. After the
user chooses a model, reasoning effort and backend, submit `execution_start`
with `aiSessionId`, `taskId`, a stable `requestId`, the observed `expectedTarget`,
`runtimeConfig` and `backend`. These fields form one command; a repeated request
must preserve all of them. Core records a START_APPROVED confirmation and returns
the original Execution/Run on replay. Do not copy a Review's selection or infer a
backend fingerprint from its name.

When the Task reaches Verification, use the exact existing Execution target,
including `primaryMemberId` and `approvedWorksetHash` when present. Do not send
new runtime/backend selections or an implementation-start request ID for this
continuation. Verification uses the already frozen Execution configuration.

Historical operations whose Core records have no exact semantic target remain
visible in `work_list.result.unavailableOperations`, within the same bounded
page. This diagnostic branch preserves the original Core facts and marks them
read-only; it supplies no guessed target or action authority.

## Running it locally

Build Core and both adapters from the repository root:

```sh
go build -o ./bin/ ./cmd/...
```

Start Core first. Per-model runtime defaults are configured on Core (or in Desktop Settings):

```sh
bin/devbridge \
  -mode server \
  -data-root /path/to/devbridge-data \
  -listen 127.0.0.1:8081 \
  -allowed-models gpt-5.6-luna,gpt-5.6-terra,gpt-5.6-sol,gpt-6-astra
```

Without an explicit configuration, each model uses its catalog default. For
independent defaults, pass `-coding-agent-model-configs` a JSON array of
`modelId`, `enabled`, `supportedReasoningEfforts`, and
`defaultReasoningEffort` entries matching the Core capability catalog. Desktop
saves the same contract as configuration version 2. The legacy
`-allowed-reasoning-efforts` flag is normalized once at ingress; it is not a
global override of operation settings. Exact approvals freeze model, reasoning
and Codex Fast Mode on the successor Execution.

Then start the Local MCP proxy against that Core. `-data-root` is used to
verify proxy/Core identity; the proxy does not open SQLite or become a writer:

```sh
bin/devbridge-local-mcp \
  -data-root /path/to/devbridge-data \
  -core-url http://127.0.0.1:8081 \
  -listen 127.0.0.1:8090
```

- The listener is loopback-only by default. For a Docker-based local AI client on
  the same machine, bind a specific loopback port and mount or forward
  accordingly; `-allow-non-loopback` exists for explicit, unauthenticated
  non-loopback exposure and must never be combined with a public interface.
- The Local MCP proxy has no model or reasoning flags. Both adapters use the
  allowlists published by the single Core process.

Minimal MCP client registration (the following `librechat.yaml` snippet is one non-normative example):

```yaml
mcpServers:
  devbridge:
    type: streamable-http
    url: http://127.0.0.1:8090/mcp
```

Readiness for scripts: `GET http://127.0.0.1:8090/readyz` returns
`{"status":"ready"}` once its configured Core is reachable and reports the
same DataRoot.

No ChatGPT smoke and no real external MCP client smoke are implied by this document; the
endpoint example above is the basis for the first manual integration run.
