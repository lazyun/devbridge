# Desktop and Web Console

The 运行会话 page opens each Run in a 进度 view with its current observed activity,
elapsed time, last activity, optional native plan and pending requests. Project
and development-branch context stays in the appbar; it is not repeated above
the Run list. 会话记录 keeps messages with commands and file changes folded;
结果与分析, 指令 and 文件变化 remain separate views. Diagnostic identifiers and
capture details are available from 诊断. Step counts describe the current plan,
not a task completion percentage. A GPT review shows its recorded file-read
evidence and waits for the report from the original GPT conversation.

Selecting an in-progress Run attaches this view to that same Run's read-only
snapshot and live update stream. Reopening reconnects using the recorded Run
identity; closing the view only closes the browser subscription. It does not
start, resume or cancel Codex work. Sending an additional instruction is a
separate explicit action on the current eligible turn.

Dev Bridge separates local runtime operations from its main product workflow.

## Web Console

The Web Console is the main Dev Bridge product and business UI. It contains, or
is the intended home for, Projects, Feature Areas, Reviews, Tasks, Executions,
Workspaces, logs, and diagnostics.

Selecting something for viewing in the Web Console does not count as selecting
a Project for a ChatGPT session. Project selection in the AI workflow remains
an explicit user decision.

### Business actions and decision sources

Task details combine two separate facts before enabling a command: the shared
domain capability for the exact Task/Review/Execution target, and the trusted
local-operator adapter for that same target. A capability available to a bound
ChatGPT Session is not automatically available to the Web Console. Missing
domain eligibility, a stale target, incomplete input, an unavailable local
channel, and an unknown result are reported separately. Desktop uses this same
Web component and Core endpoint; it does not maintain a second approval state
machine.

The local loopback ingress supplies the local operator identity. Browser JSON,
headers, a Card's visibility, and an empty or borrowed AI Session cannot assert
that identity. New decision receipts record their actor source. Historical
receipts without actor evidence remain source-unknown. A Review's persisted
author remains distinct from the user deciding it: approving an existing
Direct File proposal can authorize the original author scope, but does not let
the local operator read or use that file grant.

Actions that need new engineering meaning use fixed forms. A change request
requires complete replacement implementation and verification specifications;
Core does not copy the old definition and call it a change. New Coding Agent
work selects from Core's current model, reasoning-effort, and Fast Mode
inventory. Frozen Execution configuration is displayed as immutable, and
Verification or a verification-only successor inherits its source Execution
configuration. Approval without execution, approval-and-start, supplemental
Review request, Review start, rejection, cancellation, and Task completion are
different commands with different consequences.

The local channel currently exposes supplemental Review request but reports
`START_TASK_REVIEW` as unavailable until Review author/source admission and
dispatch can be persisted without borrowing an AI Session. Likewise,
`APPROVE_AND_START` remains a single ChatGPT confirmation orchestration rather
than being emulated by two Web commands; Web users can approve without starting
and later use the exact `START_EXECUTION` capability.

ChatGPT uses purpose-specific confirmation Cards. The result/status Card stays
read-only. Review `REVISE`/`REJECT` and Task acceptance
`ACCEPT`/`REJECT`/`REQUEST_REVERIFICATION` are prepared as closed Review or
Execution confirmation intents, shown, explicitly confirmed, committed with a
stable subrequest, and read back through the original receipt chain. These
decision variants accept no placeholder specification, instruction, model,
reasoning effort, or Fast Mode. The former generic `decision_respond` ChatGPT
route is retired; existing runtime-request, cancellation, recovery, and commit
retry protocols remain separate.

Task phase, current Execution outcome, exact Implementation and Verification
Run transport status, decision receipt, pending decision, and current activity
source are displayed independently. A completed confirmation step does not
mean a Task succeeded, a consumed choice does not prove backend dispatch, and
a failed Run does not rewrite the durable Task phase. Run failure text is
derived from typed Run/Execution status and failure code, never from keywords
in an error message.

Operation token usage always shows an accounting status. **Available** with
`0` is an observed zero; **Pending** means accounting is not finished; and
**Unavailable** means no authoritative total is available. The Console does
not substitute zero for either of the latter states. Optional cached-input and
reasoning-output values may remain unknown even when an authoritative total is
available.

## Desktop

Desktop is the local operations shell. Its responsibilities are to:

- start, stop, and restart Core and MCP services;
- list Projects and register a user-selected local Git repository through the
  production Core application API;
- select the DataRoot;
- inspect runtime health and Codex availability;
- provide operating-system and native integration; and
- embed or reuse the Web Console.

Desktop is not a second business or control plane. The same Dev Bridge product
workflow remains centered in the Web Console; Desktop supplies the local
runtime and native shell around it.

## Run sessions

Choose **运行会话 / Run sessions** in Desktop, or **运行会话** in the Web
Console. Desktop uses the content-only
`/ui/?workbench=sessions&layout=embedded` presentation inside its own single
navigation shell; `/ui/` and `/ui/?workbench=sessions` remain full Web Console
entries. The presentation parameter grants no identity or authority.

The left column lists the selected Project's durable Runs, including reserved
work without a Codex Thread or Turn. Filter by Feature Area and
in-progress/history/all, then page through bounded groups of 20 Runs. New
views default to **All**; a valid scope explicitly saved for that Project is
retained. **In progress** means `CREATED`, `PREPARING`, `RUNNING`,
`WAITING_APPROVAL`, or `COLLECTING`; **History** means `SUCCEEDED`, `FAILED`,
`INFRASTRUCTURE_FAILED`, or `CANCELED`. Task Run
history, Review details and Execution details link to their exact Run; a
historical selection is never silently replaced by the latest Run.

The Run list refreshes independently even when no Run is selected. While the
view is visible it uses one bounded request at a time, normally every five
seconds, and backs off to 30 seconds after a failure. Hiding or leaving the
view stops that timer; returning triggers one refresh. A failed refresh keeps
the last valid page and marks it stale. Empty pages explain the active filter
and offer explicit actions to view all, view history, clear the Feature Area,
or return to the first page. None of these actions selects or creates work.

The center contains **会话 / Conversation**, **指令 / Instructions**, and
**文件变化 / File changes**. The details column separates Task phase, Core Run
status, handoff evidence, backend connection facts, configuration provenance
and technical identity. A ready Core, an HTTP heartbeat, or an unavailable
runtime snapshot is not evidence that Codex accepted a Turn. Missing usage or
command exit codes remain unavailable, rather than becoming zero.

Conversation items use bounded records from the original runtime observation
path. Each item identifies its source; messages and plans remain separate from
Core workflow events. Unknown item kinds remain unsupported, and private
reasoning is not reconstructed. Commands and output are inert text. Message
rendering supports a limited escaped Markdown subset without external image
loading, raw HTML, or active links. Final item snapshots replace older item
revisions. Open request forms do not rebuild on every message update.

The first item page contains at most 50 records. **读取更早记录** reads stable
ordinal pages, with at most 1,000 older records materialized in one view.
Loaded history is reread when the session refreshes so earlier items cannot
retain stale revisions. `PARTIAL`, `GAP`, `TRUNCATED`, and `UNAVAILABLE` coverage
and reasons remain visible. A final assistant claim never fills missing
historical messages. The browser retains only view identity, history page
positions, filters and scroll position in session storage; it does not persist
credentials, prompts or approval payloads there.

Instructions distinguish frozen author instructions from the exact recorded
Run input. **复制接口原文** copies the authorized response text directly, without
UI labels. Having a stored input does not imply it was dispatched: the page
shows the separate dispatch fact. File changes distinguish App Server reports
from Bridge's already collected start/end snapshots and Git evidence. Reading
or refreshing does not collect new Git evidence or create backend work.

### Connections and explicit actions

After an exact Run snapshot is read, each non-terminal viewer opens its own
HTTP update subscription using the snapshot's Run, generation and cursor.
Updates cause bounded snapshot/history rereads and monotonic item replacement.
An invalid cursor, changed generation, stream failure or 30 seconds without
stream data disables action buttons and rereads the snapshot before
reconnecting. Heartbeats affect connection liveness only; they do not advance
the last Codex activity timestamp. An unselected viewer shows a neutral prompt,
not a disconnection. Initial read failure, stale-snapshot disconnection,
connected updates, and terminal Run results are separate states. A terminal
Run closes its viewer subscription without describing the Run itself as
disconnected. Scrolling upward keeps the reading position; new content offers
a follow button instead of forcing the scroll to the bottom.

`CODEX_REQUEST` forms use the original exact request identity and available
responses. Unsupported forms and numeric request IDs outside JavaScript's safe
integer range are read-only. `RUNTIME_APPROVAL` decisions remain separate and
use the original persisted approval service. Permission grants cover only the
request's original permission set. No action defaults to approval. An uncertain
response is read back without automatic resubmission. Transport writing and
request resolution are not proof that a command succeeded.

**取消整个运行** uses the exact original cancellation capability and AgentExecution
envelope, through Core's existing cancellation endpoint. The receipt is an
acknowledgement; cancellation remains in progress until Core records the actual
terminal result. Declining a request is a separate action. There are no new
force-unlock, process-kill, automatic retry or generic recovery controls.

### Native Desktop embedding and lifecycle

Desktop's production Wails `AssetServer.Middleware` serves the same Console
inside the native origin at `/ui/?workbench=sessions&layout=embedded`. This is an actual iframe
inside the application, not an external-browser shortcut. The native handler
forwards only Console paths to the configured, attached or managed Core on an
explicit loopback HTTP address. It rejects redirects and arbitrary proxy
targets, and rebuilds the outbound request without browser authentication,
cookies or caller-supplied forwarding headers. Core still establishes the
local operator through its original listener admission and checks exact
Project/object authority.

Each Desktop process generates an unguessable document capability, injected
only into its embedded Console HTML and sent in a same-origin request header.
It is never included in a URL or browser persistent storage. Native Host,
Origin and capability checks protect the adapter. Only native adapter responses
allow same-origin framing; the directly served Core Console retains its
original anti-framing, Host, Origin and CORS rules. The Wails WebKit response
writer delivers SSE chunks directly; stream contexts are bounded and canceled
on application shutdown or client disconnect. A Core endpoint that is not an
explicit loopback HTTP address is not accepted by this adapter.

Closing a session or navigating away only releases that viewer's subscription,
requests and timers. Closing Desktop follows its existing `OnShutdown` path,
which stops its managed services; active Runs in its managed Core can therefore
be interrupted. An attached external Core follows the original ownership
policy. The runtime owner is `IN_PROCESS_SUPERVISOR`, and
`supportsCoreRestartReattach=false`. Reopening the UI restores reading only;
it does not resend a prompt or reconnect an orphaned Codex process.

Desktop owns the outer window height. Its session iframe fills the remaining
content area without another viewport subtraction; inside the document the
Run list and current conversation own separate scroll regions. Technical Run
details are closed by default and open as a bounded, independently scrolling
panel. At narrow widths the Run list is also a closable panel; its controls,
the details control, keyboard focus, and Escape remain available without
turning the workbench into three vertically stacked pages.

This change has source and syntax checks only. Desktop packaging, native
WebView streaming, multi-viewer reconnects, runtime request delivery and
cancellation races require separately authorized manual acceptance. No build,
browser, service restart or live Run operation was used to claim runtime
acceptance.

For first use: start the runtime, open **Projects**, choose **Add Project**, use
the native directory picker, and confirm the proposed name. The durable Project
then appears in `project_list`; `project_open` remains part of the explicit AI
selection flow.

## Runtime and recovery states

The Web Console projects the authoritative Agent operation state of each
Feature Area as reported by Core: `READY`, `BUSY`, or `RECOVERY_REQUIRED`. A
`RECOVERY_REQUIRED` state means the previous Coding Agent operation ended with
an unknown backend outcome and cannot be safely resumed. The Console shows this
state read-only with guidance; the actual recovery decision stays with the
explicit `agent_operation_recover` product command in the AI session. Recovery
abandons the stranded operation, retains all history, and starts fresh Agent
context for future work; it is distinct from the ordinary idle-only Agent
Context Reset.

If another Dev Bridge Core already owns the same DataRoot, Desktop reports the
ownership conflict directly ("Dev Bridge DataRoot is already in use by another
running Core") instead of a generic connection error. Close the other Core
instance to proceed; there is no force unlock.

审查的执行者以实际 Review 记录为准：GPT 审查由原 GPT 对话读取文件并提交报告，页面的等待状态不表示后台存在 Codex 进程；Codex 审查由后台 Codex 运行，原生活动可在运行会话中查看。创建选项、当前任务、审查历史及运行列表均明确标识执行者。

服务重启后，GPT 审查若显示「待重新授权」，请在当前任务点击「重新授权 GPT 审查」，然后回原 GPT 对话继续。该操作沿用原审查；不代表找回 Codex 结果，也不会代替 GPT 完成审查。
