# Session-first Project initialization

The model obtains one real AISession before showing Project choices. Its Project
is initially NULL. Explicit selection binds that same Session once; display,
readback and later engineering calls never exchange it for a different Session.

## Public routing

| Situation | Route |
| --- | --- |
| New conversation with no exact Session | `session_initialize({requestId})` with a new random request ID |
| Initial response lost | Retry that exact request ID; do not generate a new intent |
| Existing Session, including after conversational Project selection | `session_initialize({aiSessionId})` reads its current binding |
| `PROJECT_SELECTION_REQUIRED` | Explain candidates in conversation and request explicit choice; `project_selection_show` is optional read-only display |
| User explicitly chooses a Project | `project_open({aiSessionId, projectId, requestId})` binds the same Session |
| `PROJECT_BOUND` | Reuse the same Session with `workspace_get`, then the user's authorized operations |
| Binding response lost | Read the known Session; only the original request and payload may be retried |
| Session missing, inaccessible or required facts unavailable | Report the actual blocker; never initialize a replacement automatically |

The two `session_initialize` inputs are mutually exclusive. Neither, both, blank
IDs or unknown fields are invalid. Read mode needs no request ID and cannot
create anything. The tool has creation capability, so its overall annotation is
state-changing even though the Session-ID branch is pure read.

The first result exposes the actual `aiSessionId` in model-visible structured
content. An unbound result has `state: PROJECT_SELECTION_REQUIRED`,
`boundProjectId: null`, and a bounded `availableProjects` candidate list with its
own read status/error. A bound result has `state: PROJECT_BOUND`, the actual
`boundProjectId` and minimal `project` identity. Candidate and bound Project fields
are distinct. Inventory failure after initialization retains the committed
Session ID and reports the list error; it does not claim there are no Projects.
Use `project_list` for further pages.

Connector selection, one inventory item, Console selection or a prior Card cannot
bind a Project. A uniquely named Project still requires initialization followed
by binding the same Session; ambiguous names require a choice. Project binding
selects no FeatureArea, Task, Decision, operation or action target.

## Durable identity and transactions

The real `ai_sessions` table represents UNBOUND by SQL NULL `project_id`; no
placeholder Project or second temporary Session table exists. A non-NULL Project
is immutable. Losing access to that Project, or finding a missing reference, does
not turn a formerly bound Session into an unbound one.

Existing `aisession.Session`/`Get` remain a bound engineering view and reject
UNBOUND explicitly. Initialization has a separate nullable binding view of the
same durable row. This keeps existing engineering consumers from interpreting
an empty Project as an omitted query filter. Store scanning handles NULL before
constructing a bound view; Console's Project-session listing only returns bound
Sessions. Neither this view separation nor request receipts create a second
Session identity or a copied state machine.

Initialization commits the real unbound Session and its operation-scoped request
receipt together. The receipt key includes caller scope and stable request ID;
operation kind and normalized payload must match. A duplicate returns the original
ID but reads its **current** state, including a binding made after initialization.
The operation receipt and current-state read are separate facts.

Binding uses the existing write-authority transaction. It checks exact Session,
caller scope and Project, records the original explicit ProjectSelection,
consumes it in the legal order, CAS-binds the existing Session and stores the
binding receipt before commit. Concurrent different-Project choices cannot both
bind the Session. Same-Project repeats return its original identity and never
consume a second selection. Reusing a request ID with another operation, Session,
Project or payload is a conflict. A new request confirming an existing binding
can record that confirmation but cannot repeat the original binding or preparation.

`finishNewSession`'s original Project-level preparation runs only after the first
successful Project binding. Initialization, getter calls and receipt replay never
run it. A preparation failure preserves the committed Session/Project and reports
preparation/context availability separately. It does not roll back identity,
create a replacement or claim the Project is ready. A repeated `project_open`
is not an unsafe preparation retry mechanism.
`preparationStatus: COMPLETED` means the original preparation call returned
successfully; Project Context and Development Branch readiness still come from
the subsequent authoritative workspace read.

Reads do not update usage, runtime epoch, request receipts or workspace state.
An UNBOUND read after a lost binding response does not prove the request could
not still commit. Keep its original request ID and payload, and keep UNKNOWN
until an authoritative result resolves it. Complete data loss can also destroy
receipts, so exactly-once is not promised across that loss.

## Scope and require-bound admission

Caller scope comes from the existing trusted adapter/authentication boundary,
not tool arguments. Anonymous local access remains a shared local-service scope,
not authenticated ChatGPT-conversation ownership. A new initialization request
uses a new random ID; exact retries reuse it. Deliberately reusing an ID in the
same scope is indistinguishable from retrying that intent. Request IDs are not
authentication. Host conversation metadata and connection pointers are unnecessary
for the new initialization flow.

| Production entrance | UNBOUND handling |
| --- | --- |
| Initialization Session-ID read | Allowed, pure read with scope verification |
| Candidate Project inventory and selection display | Allowed; neither binds nor prepares a Project |
| Explicit `project_open` | Allowed only with existing Session, Project and stable request ID |
| Independent diagnostics | Existing non-Project diagnostic contract |
| Workspace, repository/file access, documents, FeatureArea/Task/Review/Execution, approvals, cancel/recover | Require an actual bound Session and existing exact Project/owner checks |
| Console Project-session listing/selection | Bound Sessions only; no auto-binding of UNBOUND entries |

MCP dispatch injects trusted caller scope before business handlers. Session-bound
engineering calls are checked before dispatch, including direct-SQL capabilities.
The original bound Session getter and Facade Project guard provide the common
require-bound boundary. Raw SQL equality joins do not match NULL Project IDs;
NULL scans never become empty-string wildcard filters. All command-side lifecycle,
Upper expected versions and durable replay checks remain authoritative.

Initialization, binding and selection-show results bypass enrichment that assumes
a Project exists. A successful initialization cannot lose its Session ID because
a generic middleware subsequently attempted a workspace read. GPT and Local MCP
share registration, schemas and handler contracts; tools/list performs no Session
initialization or business read. Text clients use the same flow without a Card.

## Read-only Card and conversational control

The model creates or validates the real Session and performs explicitly authorized
Project binding. `project_selection_show` receives that existing Session and reads
its facts; the Card displays candidates and asks the user to choose in conversation.
Zero, one or many candidates never triggers selection in the component.

The new Card reads Session state through
`dev_bridge_card_get({aiSessionId, readMode: "SESSION"})`. This route returns the
existing Session DTO through the ordinary envelope and cannot create a Session.
The Card does not call `session_initialize`, `project_list` or `project_open`.
The model retains the original two-mode `session_initialize` tool; it may call
`project_list` for inventory and `project_open` after explicit conversational choice.
`workspace_get` and normal Card reads require actual Project binding.

Entry, generation, Session, Project and response-identity checks still protect
readback, remount and local navigation. Historical/detail targets remain exact.
A Task/Review selected inside Card is only a browsing target; GPT rereads and
resolves an accurate command target when the user asks for an operation. A missing
Development Branch is a bound Project fact, not an initialization request.

Card sends no `ui/update-model-context`, `ui/message` or follow-up message for
binding, approval or completion. No push acknowledgement or automatic wake is part
of the workflow. Widget state retains only view restoration information; old
pending mutations, approval drafts and runtime configuration drafts are ignored.
Card read-only policy is component-level and leaves domain `readOnly`, Core
capabilities, Task lifecycle and WebUI/Desktop controls unchanged.

The resource `ui://devbridge/card/read-only-v1` identifies the new component; the
old `ui://devbridge/card` URI is a compatibility alias for current source. This
source mapping does not establish that an already mounted host component reloaded.
No cache deletion, rebind or new business object is required for the new flow.

### Preserved readback diagnostics and historical incident evidence

Session reads retain their original identity and never enter initialization creation
mode. The read-only Card retains distinctions between HOST_REQUEST, HOST_RPC, MCP_TOOL, RESPONSE_DECODE,
SESSION_VALIDATE, LOCAL_APPLY and LOCAL_RENDER; unidentified exceptions remain
UNKNOWN. Only an explicit AI_SESSION_NOT_FOUND Core code reports a missing Session.
Numeric JSON-RPC codes and explicit tool/Core codes keep their separate sources.
A validated UNBOUND result, nullable Project and optional receipt remain valid;
inventory UNAVAILABLE remains a separate list failure. Local display failure does
not erase a successful Session read or grant business authority.

The Card collects bounded diagnostics without displaying them by default. Open
**Card 设置 → 显示 DEBUG** to view the shared DEBUG dialog, then use **复制 DEBUG**
for a one-click redacted snapshot. Close or Escape dismisses the dialog without
clearing records. The dialog lives outside the business root; business redraws do
not remove it. Selectable text remains available if clipboard access fails.
The static HTML declares `CARD_DEBUG_V2`; the independent bootstrap and main script
continue sharing startup observations and diagnostic state.

The panel shows observed host initialization, entry/generation, Session aliases and
exact identity comparisons, Project binding, actual RPC method/tool/ID, parameter
flags, response shape, precise validation rule, read success and local failure, and
interaction holds. Events are recorded at real call sites; RPC_SENT proves only the
component completed its send. A single local timer updates pending elapsed time,
stopping while the dialog or document is hidden, closed or without pending RPCs. It adds no request or retry.

The component retains at most 60 short events, eight attempts and 128 stable identity
aliases (IDs over 128 characters are not aliased). Session/Project aliases include a
four-character hexadecimal tail when available; comparisons use the full original ID.
Copied structured snapshots are capped at 32 KiB with explicit truncation. Arbitrary
message words, unknown response keys, credentials, work content and raw responses
are not exported. Message vocabulary is deliberately conservative; redacted text is
not evidence of the original full message. There is no upload or diagnostic service.

`CARD_DEBUG_V2` is a code marker, not a hash or Git commit. The main script captures
its own inline text synchronously and, when supported, computes `mainScriptSHA256`
once asynchronously without awaiting it in initialization or readback. This identifies
the currently executing inline main script, not the whole HTML or server binary.
Unavailable hashes, server versions, server request IDs and host-loaded HTML identity
remain UNKNOWN. No disk HEAD is presented as a running version.

Once S/P is verified BOUND, local failure preserves that binding and recovery uses
existing workspace reads, including historical bound entries. Read loading holds are
released even when rendering fails. A later successful Session read clears the current
error view while bounded earlier failed attempts remain unchanged. This does not
change Core binding receipts or resolve an uncertain binding command. Card contains
no executable binding intent; GPT recovers the original request in conversation.

For an actual incident, after manually loading a version containing this code, retain
the original entry and S, use the current pure-read refresh once and open **Card 设置 → 显示 DEBUG** and screenshot the dialog
or use **复制 DEBUG**. No DevTools navigation is required. Do not select a Project, initialize a replacement, clear data or restart
to change the evidence. A current NOT_FOUND does not explain an earlier incident.
A later real Card trace correlates a loaded script with the omitted-field rejection
described below. This establishes that immediate failure condition, not the upstream
omission source or successful recovery; diagnostic improvements are not host or
end-to-end acceptance.

The official [API reference](https://developers.openai.com/plugins/reference) and
[state-management guidance](https://developers.openai.com/plugins/build/chatgpt-ui)
describe these host APIs. They do not prove that the deployed host supplied
metadata or performed a later binding read. Context updates are no longer part of
the read-only Card workflow.

## Migration and historical compatibility

Migration 50 and all earlier migrations remain unchanged. Migration 51 is appended
as source only. It rebuilds `ai_sessions` with nullable Project and nullable caller
scope while retaining original IDs, Projects and creation/update timestamps.
Historical principal, initialization request and bind time are not invented.

The migration preserves Session indexes, including the composite `(id,project_id)`
key used by child foreign keys. It saves and restores dependent trigger definitions
around table replacement, replacing only the old Session immutability rule with
NULL-aware one-time-binding rules. The original selection constraints and v50
binding triggers remain in force. The existing migration runner turns foreign
keys off outside the migration transaction for replacement, checks all foreign
keys before commit and restores enforcement afterward. Referencing tables keep
the same `ai_sessions` name and IDs; no Task/Review/receipt history is recreated.
No migration is executed by this task and a clean database is not a prerequisite.

Existing v50 BOUND handles can still be read to their original Session. A v50
UNBOUND handle cannot create or exchange itself for a Session. Historical
no-Session `project_open`/primitive Session-open creation paths are closed; they
cannot act as hidden compatibility writers. Existing successful historical
bindings remain readable without changing their ID or original selection relation.
An unprovable or missing historical relationship fails rather than using Session
inventory to invent one. New default Card/model/Local flows do not need a handle.

## Manual host acceptance

After the user deploys the source and applies migrations through the normal
upgrade process, capture actual host tool-call evidence:

1. In a new conversation, initialize with a fresh random request ID. Record the
   actual Session S, unbound state and NULL Project. Lose the response and retry
   the original ID separately to verify S is reused.
2. Optionally show the selection Card with S. With zero, one or many candidates,
   verify Card performs only allowed reads and local viewing, never Project binding.
   Inventory failure must retain S and show failure rather than a successful empty list.
3. Choose P in conversation. Verify GPT uses `project_open(S,P,requestId)` to bind
   S/P. Original-payload replay returns S; changing Project/payload is rejected.
4. Say “创建领域完成此需求”. Verify GPT reads `session_initialize(S)` and obtains
   P, then `workspace_get(S)` and only the already authorized Core steps. Card
   context push, manual synchronization, ID copying and replacement Sessions are absent.
5. Lose a binding response, then request status in conversation. GPT rereads S and
   retains the original request/payload if retry is needed. Remounting Card issues
   only pure reads; old pending mutation state is ignored, not replayed. Late or
   unrelated view state cannot replace S or become the next command target.
6. Check an old bound Session and v50 bound handle after upgrade. Check stale
   references fail, old unbound handles cannot create Sessions, and new initialization
   retries after binding still return S with current PROJECT_BOUND state.
7. Verify UNBOUND S cannot access workspace, files, engineering reads/writes,
   approval/cancel/recover. Verify existing bound history, shared Task actions,
   Upper Recover→PREPARING→explicit Prepare, version fencing and receipts still apply.

Static source review, gofmt, script syntax and diff checks do not establish host
routing, migration execution, concurrent writes or durable replay at runtime.
Keep these acceptance states separate:

`PROJECT_SELECTION_TO_NEXT_TURN_ACCEPTANCE_PENDING`

`FIRST_USE_HOST_ROUTING_ACCEPTANCE_PENDING`

`HOST_UI_ACCEPTANCE_PENDING`

`RUNTIME_COMMAND_REPLAY_ACCEPTANCE_PENDING`

### UNBOUND Project field presence at the Card boundary

The canonical Session DTO, produced by model `session_initialize` and the Card-only
pure `dev_bridge_card_get(readMode="SESSION")` read, keeps `boundProjectId` required and
nullable: PROJECT_SELECTION_REQUIRED emits JSON null; PROJECT_BOUND emits the
actual nonempty Project ID with the matching project. `InitializationResult` has
no `omitempty` on this field, and the tool envelope retains that DTO. Inventory
availability remains independent. No local null-omission boundary has been proven;
the observed missing field does not establish which upstream layer omitted it.

The Card Session DTO boundary accepts an absent own `boundProjectId` field in
explicit PROJECT_SELECTION_REQUIRED, after exact Session, project/context and
inventory validation. It returns a copied result with null; it does not modify the
RPC response. An own field must be strictly null: undefined, empty string, false,
zero, strings, arrays and objects are rejected. `project` must be absent or null;
explicit context identity conflicts are rejected. PROJECT_BOUND still requires a
nonempty string ID without whitespace/NUL, at most 128 characters, matching project
and any supplied context identity. Missing or unknown state never gains a default.

DEBUG records the original `boundProjectFieldState` (MISSING / NULL / VALUE) before
validation and `unboundProjectNormalized` only after successful normalization.
Missing is not evidence of a real Project. No raw Project value is logged. The
script hash continues to identify the loaded inline script. Core pending/OPEN_UNKNOWN
binding receipts remain authoritative; old Card pending mutations are ignored,
not restored. Exact old-entry guards remain, and a valid
UNBOUND read does not prove that an earlier binding request failed.

A real Card trace now identifies the immediate UNBOUND_PROJECT_NOT_NULL rejection
on an omitted field; the upstream omission source remains unconfirmed. After
manually loading this revision, keep the original Session and entry and click
the current pure-read refresh once. Expected: RPC_RESULT → RESPONSE_DECODED → SESSION_VALIDATED →
PROJECT_LIST_RENDERED. If the field remains absent, DEBUG should retain MISSING
and show unboundProjectNormalized=true. Only that observed success closes this
specific Card read failure; selection, binding and command replay acceptance remain
independent. Do not initialize, project_open or clear data to obtain this evidence.
