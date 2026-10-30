# Runtime request payload schema closure

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-bef401904f7ae6f6)。

Starting branch: `dev_20260908_1913`.
Starting HEAD: `0b45b5049a2bf17de087b49273176afae2b64cf8`.
The eight uncommitted bootstrap-contract files present at task start were preserved.

## Cause and contract

`jsonschema.For` infers `json.RawMessage` as nullable integer arrays, whereas
`encoding/json` emits the contained JSON value. Presentation schemas previously
normalized only `agentruntime.Item.metadata`. Request objects therefore failed
SDK output validation when their payload and other raw fields were populated.

All five supported methods in `internal/agentruntime/protocol.json` declare
object request Params and object responses: command approval, file approval,
permission approval, requestUserInput, and MCP elicitation. `Session.observe`
validates Params before retaining a request. The payload output contract is:

```json
{"type":"object","additionalProperties":true}
```

Method-specific validation remains in `agentruntime.Validate` and the resolver.
The output schema does not authorize a request or replace that validation.
`Request.Payload` remains RawMessage. No data, identity, response options,
permission semantics, or SDK validation behavior was changed.

Presentation normalization now reuses the handwritten runtime schema for each
Request and Diagnostic raw leaf, including nested pending-decision requests.
It asserts the expected nullable integer-array inference before replacement;
new unaudited raw fields and changed generator inference fail explicitly.
Parent properties, closed-object boundaries, and required fields remain intact.
The existing metadata optionality normalization is unchanged.

## Public interface matrix

| Interface | Runtime request location | Contract and verification |
| --- | --- | --- |
| GPT / Local `workspace_get` | `workflow.pendingDecisions[].runtimeRequest`; `result.taskView.activity.runtime.requests[]` with TASK; `result.navigation` with NAVIGATION | Shared Surface type mappings already use the handwritten Request schema. SDK tests cover pending requests, TASK and NAVIGATION; production GPT and Local handlers return the same request. |
| `workspace_show` | `_meta.workspace.presentation.{taskView,latestTaskView}.activity.runtime.requests[]`; project pending decisions in private workspace | Model-visible output remains the compact envelope. Actual SDK call succeeds; private workspace is separately validated against the Card output schema. |
| app-only `dev_bridge_card_get` | `presentation.{taskView,latestTaskView}.activity.runtime.requests[]`; `pendingDecisions[].runtimeRequest` | Normalized Request leaves; actual SDK calls and standalone pending-decision schema validation. |
| app-only `feature_area_presentation_get`, `task_presentation_get` | `{taskView,latestTaskView}.activity.runtime.requests[]` | Both use the same normalized Projection schema; actual SDK calls with populated request and diagnostic JSON. |
| GPT / Local `operation_get` | `result.runtimeRequests[]` with RUNTIME_REQUESTS; telemetry diagnostics under ACTIVITY | Shared handwritten Request schema; actual production-adapter calls preserve all request fields. ACTIVITY keeps its existing omission of requests. |
| GPT / Local `runtime_request_respond` | business `result.runtime.requests[]` | Same handwritten Snapshot/Request contract; populated output validated without dispatching a real response. |

## Targeted RawMessage audit

| DTO field | Wire value / schema decision |
| --- | --- |
| `Request.payload` | Object; generated presentation mismatch fixed; shared handwritten schema retained. |
| `Request.requestId` | String or integer; generated presentation mismatch fixed. |
| `Request.availableResponses[]` | Object elements; generated presentation mismatch fixed. |
| `Request.resolution` | Optional object; generated presentation mismatch fixed. |
| `Diagnostic.details` | Optional object from observed runtime notification; fixed in lastError, lastWarning and diagnostics arrays. |
| `Item.metadata` | Existing object/array/null normalization retained, including optional metadata. |
| `Decision.permissions` | Raw presentation JSON; generated mismatch fixed at the leaf, using the same JSON-value kinds already allowed by Surface. |
| `PendingDecision.runtimeApprovalRequest` | Bounded/redacted raw JSON; sanitizer preserves object, array, string, number, boolean and null. Generated mismatch fixed at the leaf. |
| approval output `request` and legacy workflow approval `request` | Handwritten JSON-valued schemas already accept raw structured JSON; no change. |
| resolver/input `permissions`, `Resolution.requestId/response` | Input contracts, not new model/app output inference; no change. |
| tool InputSchema/OutputSchema values and embedded protocol schemas | Schema declarations, not business DTO payloads; no change. |

No other raw-field mismatch was found in the audited model/app output DTOs.
No broad parent schema was replaced with an unconstrained object.

## Validation

- All five installed method contracts are tested as object-only request/response
  roots; array/null/scalar roots are rejected. Valid observed requests remain PENDING.
- A realistic command payload with availableDecisions, command, cwd and
  commandActions passes actual MCP SDK output validation.
- Requests are compared in full after SDK serialization, including requestId,
  itemId, threadId, turnId, connectionId, payload and availableResponses.
- Both string and integer request IDs and an explicit resolved-response object
  pass the normalized contract. Negative payload-shape tests remain strict.
- Existing Card tests cover explicit response clicks and exact identity; no
  read or schema normalization approves a request.
- Card JS: 436 total, 432 passed, 0 failed, 4 conditional fixture tests skipped.
- Focused MCP, agentruntime, workflowpresentation and Local MCP package tests passed.
- Final `go test ./...`, `go vet ./...`, and `go build ./...` all passed (exit 0).
- `git diff --check` passed. Status: `RUNTIME_REQUEST_PAYLOAD_SCHEMA_CLOSED`.

Logs: `/tmp/devbridge-payload-{packages,card,all-test,vet,build}.log`.

Only test transports, fixtures and temporary test data were used. No formal
Desktop, browser, device, live SQLite or real runtime approval was operated.
No commit, push or publication was performed.
