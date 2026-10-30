# Codex runtime requests and explicit user decisions

Status: **PARTIAL_IMPLEMENTATION**. `CODEX_REQUEST_USER_DECISION_CLOSED` is
**not established**. The live-connection user-decision chain and fail-closed
restart retention are implemented; automatic continuation of a recorded,
unsent response across Core restart is not supported by the pinned protocol.

This increment extends the existing agentruntime request ledger. It does not add
Task phases, a second execution workflow, or a model-authored answer policy.
The prior Coding v1 / RuntimeWorkset v77 and Direct File / delivery v76 contracts
remain in force. Validation results for this increment are recorded below only
after the production fixtures run.

## Authority and persistence

A semantic `CODEX_REQUEST` belongs to its exact Project, applicable FeatureArea,
Task and definition revision, operation kind/id, Review or Execution, Run,
backend Thread/Turn/connection, runtime epoch and applicable AgentConversation.
`runtimeRequestId` identifies the durable request; the original backend request
id and payload are retained, along with response schema and SHA-256 values.
Missing historical identity does not acquire authority through current defaults.

Schema v78 adds `runtime_user_decisions`, linked to its Run and the existing
`codex_server_requests` composite key. One request has at most one immutable
UserDecision. Database triggers forbid replacing or deleting an answer. A stable
submission id and canonical input hash make exact retries readable; changed
answers or changed identity conflict. The server derives the submitting actor
from the scoped session or local operator. The hash excludes that derived actor,
the hash field itself, and generated receipt fields. It includes the explicit
source surface/event, action and exact selected options or user text.

v78 also extends the existing cancellation ledger owner constraint for
Inspection. It preserves the ledger and does not introduce a separate cancel
workflow. Migration compatibility is verified against existing schema history.

## Question lifecycle

`WAITING_FOR_USER` has no answer, no timeout default and no preselected option.
Submitting an explicit user event records `USER_DECISION_RECORDED` before any
backend effect. `RESPONDING` is persisted before writing the official response.
Transport write alone is `UNKNOWN`; only the backend's exact
`serverRequest/resolved` acknowledgement establishes `RESPONDED`.

A recorded answer with unknown delivery is displayed as “回答已记录，响应状态未知”.
It must not be represented as an unanswered question. Reads, refresh, wakeups,
process restart and reconnect never send it automatically. Exact replay returns
the immutable decision; unknown delivery is not blindly retried. A response
known not to have been sent can reuse that same durable decision with the exact
original identity. Epoch/run/turn changes do not authorize a substitute target.
Canceled and stale requests retain evidence and expose no answer action.

`RESPOND` serializes only the user's selected option or raw answer. It keeps the
same Run, Thread and frozen workset; it does not approve a wider definition.
`STOP_AND_REVISE` and `CANCEL_OPERATION` send no semantic answer. They enter the
existing operation cancellation chain and require confirmed quiescence before
conflicting work may start. A revised Review or Execution still requires its
own purpose-scoped confirmation and immutable successor; the old definition is
not edited. There is no automatic successor.

`RUNTIME_APPROVAL` remains a separate request kind. Its existing permission
policy cannot consume a `CODEX_REQUEST`. Raw `runtime_request_respond` cannot
answer a semantic question without the exact durable UserDecision reference.

## Surfaces and mandatory user response

GPT must present the original persisted questions and options, keeping its own
explanation separate. AGENTS.md, project documents, previous preferences,
recommendations, successful notification and missing UI buttons are never an
answer. Draft answer text requires explicit user confirmation before submission.
Multiple requests require explicit disambiguation; ordering is not authority.

The canonical MCP command is `runtime_request_user_decision_submit`.
The read-only `runtime_request_user_decision_prepare_hash` returns the exact
input and canonical checksum with `recorded:false`; it records no decision and
sends no backend response. It lets adapters avoid inventing a hash.
`operation_get` with `RUNTIME_REQUESTS` returns pending and recorded/unknown
requests. The HTTP adapter uses
`POST /ui/api/runs/{id}/agent-user-decisions` or
`POST /v1/runs/{id}/agent-user-decisions` with the same contract and exact URL Run
fence. Browser hashing follows Go JSON field order and omitempty rules.
The Card is read-only: it can claim and deliver a notification for an exact
persisted request, but cannot manufacture or submit a UserDecision.
Notification failure does not answer or cancel the operation.

## Isolated validation and real smoke

Production tests reuse the existing three-repository fixture (two approved
members; one excluded). The fake App Server emits official requests through the
production Client. AGENTS.md and project advice deliberately recommend an answer;
the test does not claim the fake interpreted them. Only a separate simulated
current-user event submits a decision. Tests assert raw wire response bytes,
Task phase, runtime identity and retained workset, then continue through actual
Verification, Acceptance and delivery where applicable.

`TestRealCodexRequestUserDecision` is default-off. Set
`DEVBRIDGE_REAL_CODEX_REQUEST=1` and explicitly select `DEVBRIDGE_REAL_CODEX` to
run it against test-owned repositories, DataRoot and managed worktrees. It prints
the current request and an operator answer-file location. The operator must
explicitly author the exact decision and current user event; no answer is supplied
by the test. The gate requires a real question, an explicit operator response,
and continuation to acceptance. A skip is `REAL_CODEX_REQUEST_E2E_NOT_RUN`, not
capability PASS. This increment does not call real models or modify live DataRoot.

## Remaining restart capability boundary

The checked-in `api/codex-schema/v2/ThreadReadResponse.json` and
`ThreadResumeResponse.json` expose Thread/Turn summaries, not a durable pending
server-request identity or response-consumption proof. The Client request catalog
has no pending-server-request query or rebind method. The
`ServerRequestResolvedNotification.json` acknowledgement is live notification
only; reading a Turn is not proof that a response was unconsumed.

Session waiters currently belong to one connection. After Core restart, an old
`WAITING_FOR_USER` request is retained as historical/stale evidence, and a
`USER_DECISION_RECORDED` with `NOT_SENT` is retained without dispatch. UNKNOWN
remains answered and unknown. This prevents duplicate responses, but does **not**
meet the requested cross-restart continuation of the same pending question or
recorded response. No replacement Run/Thread, inferred response, fabricated
pending-proof API, or model call is used to conceal this limitation. Full closure
requires an authoritative backend request restoration/proof contract and its
production implementation; a fake-only capability would not establish it.
