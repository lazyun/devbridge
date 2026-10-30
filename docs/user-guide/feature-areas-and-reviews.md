# Feature Areas and Reviews

## Feature Area semantic ownership

GPT decides whether to create a Feature Area, continue an explicitly identified Feature Area,
or use no Feature Area for ordinary conversation. Development requires a FeatureArea and an explicitly confirmed implementation definition. Reviewed Tasks use Review Approval; a user who explicitly skips Review confirms the implementation separately. Bridge exposes durable facts and performs the chosen action; it
does not infer the decision from consultation/implementation keywords, importance,
title similarity, fuzzy matching, or semantic deduplication.

A Feature Area can exist with zero Reviews, Tasks, and Documents. Ordinary conversation
and automatic discussion summaries are not persisted. When the user explicitly
asks to record durable knowledge, GPT authors a Document and chooses whether to
associate it with the current Feature Area.

## A Feature Area belongs to its Project

A Feature Area represents an engineering problem, change request, or development goal.
It is owned by the Project rather than by an AI session. This gives the work a
durable identity across ChatGPT reconnections or replacement sessions.

For example:

```text
ChatGPT session A -> creates Feature Area X -> Review fails
ChatGPT session B -> explicitly selects the same Project
                  -> continues Feature Area X -> retries Review under Feature Area X
```

No duplicate Feature Area is required. Session provenance and storage identifiers are
internal bookkeeping and do not change this user-visible rule.

Creation Cards identify the operation with a prominent **创建领域** (purple) or
**创建任务** (blue) badge and label the object name as **领域名称** or **任务名称**.
When creating an area also creates an initial Task, **同时创建初始任务** shows that
Task's name separately. The status badge still indicates whether confirmation is
pending or processed; the operation label alone does not mean creation succeeded.

## Continue an existing Task or create an independent Task

When the selected FeatureArea has unfinished development Tasks, the creation Card
shows their names, current stages and next-step summaries before creation:

- **继续原任务** keeps the Task and its Review, Execution and analysis history. During Review, **继续当前审查** asks GPT to verify the exact current owner; for an already authorized GPT Review, read the permitted files and formally submit the report. Expired authorization requires the original Review confirmation before reading. Other executors are not replaced.
  The button asks the conversation to read that exact Task and explain its current
  continuation path. It does not approve, recover, retry or start work.
- **创建独立新任务** requires an explicit choice and warns that the original Task
  remains unfinished and will not be closed automatically.
- **取消本次选择** leaves both the original Task and the unsubmitted proposal intact.

These are read-only creation hints, not new workflow authority. The creation
confirmation retains its original identity and content. A changed inventory is
read again before submission and requires renewed acknowledgement. If the Task
inventory cannot be read, the Card states that explicitly. Up to 20 recent
unfinished Tasks are displayed with the full count; other Tasks can be named in
the conversation. A continuation message that cannot be confirmed is offered as
copyable text rather than automatically resent.

## Choose the review method when creating a Task

The Task confirmation Card requires the user to select one of three methods:

| Card choice | Result |
| --- | --- |
| **GPT 审查（在原对话读取文件并提交报告）** (`UPPER_LLM`) | Prepare read-only access. The AI in the initiating conversation must read authorized files and submit the report. No Codex background process or Codex conversation is created. |
| **Codex 审查** (`CODING_AGENT`) | Start the prepared Codex Review with the user's explicit available backend, model, reasoning effort and Fast Mode selection. |
| **不审查，进入实施确认** (`NONE`) | Create the Task without a Review or Run, and await a separate confirmation of the complete implementation and verification plan. |

No method is selected by default. Prepare both executor-specific instructions, depth,
scope and an explicit target Repository so all three choices are available. An older
proposal lacking one method shows it as unavailable; prepare a new proposal to use it.

Skipping Review does not authorize execution. For the current single-repository
Coding implementation path, the AI prepares an `execution_prepare` Card with action
`CONFIRM_AND_START_IMPLEMENTATION`, the exact Task definition revision, complete
`workSpec` and `verificationSpec`. In that one confirmation the user also selects
the backend and runtime. Core freezes the definition and starts the confirmed plan.
Legacy `CONFIRM_IMPLEMENTATION` receipts remain definition-only; an already
confirmed legacy plan can still use `START_APPROVED`. File implementation and multi-repository plans continue
to use their existing reviewed approval flows.

The selected method belongs to the original immutable receipt. If a response is lost,
read that receipt instead of submitting another method. Historical requests with
`startReview: false` and no `reviewMode` retain their original defer-review behavior.

For AI file Review, the task and review views show the execution method and last
Bridge-observed activity. After five minutes without a new observable file-review
operation, they prompt the user to continue in the initiating AI conversation. This
is an inactivity notice, not a failure diagnosis or evidence of model thinking.
Reading or refreshing status does not count as activity or restart the Review.
Canceled and completed Reviews do not display an active inactivity warning.

## Reviews build understanding

A Review asks what production implementation matters for one Feature Area. The trusted
backend starts from Feature Area-relevant areas and may follow production dependencies
that materially affect the engineering decision. Its complete GPT-authored
instruction is persisted and sent verbatim.

One Feature Area can have multiple independent Reviews:

```text
Feature Area: improve startup readiness
  Review #1: trace readiness ownership and startup entrypoints
  Review #2: revisit shutdown coupling and a missed production dependency
```

A Task owns each formal Review. Select GPT Review (`UPPER_LLM`) for an upper LLM
that will read authorized files and submit a structured report, or Codex Review
(`CODING_AGENT`) for the existing Coding Agent execution channel. Review depth
FAST/DEEP is separate from executor and model reasoning effort.

Only Coding Agent Reviews use the FeatureArea's canonical AgentConversation. Both
executors share its operation claim; upper Reviews do not create or rotate a
Coding conversation. Upper model and token telemetry are shown as unavailable
when no trusted runtime identity exists.

Review produces understanding under its existing Task. It is not itself the
implementation and does not authorize changes.

## Review repository provenance and drift

Each Review records the repository version it analyzed: the durable commit the
Review's work was based on, captured when the Review started.

Bridge can report objective Git facts between that recorded commit and the
current repository state: whether the repository changed, how many tracked
files changed, and which paths explicitly recorded in the Review focus changed
(added, modified, deleted, or renamed). Focus-path drift means paths from the
Review's authored focus changed — it does not prove which files the Agent
actually inspected.

Bridge does not decide whether a Review is still valid. Team changes can make
previous assumptions worth re-checking, but GPT and the user make that
semantic decision. Later Reviews may supplement, contradict, or replace
earlier conclusions; each Review keeps its own independent provenance. When
drift facts are unavailable (for example, historical Reviews recorded before
this capability existed), Bridge reports that fact neutrally.

## Explicit lifecycle decisions

A new FeatureArea is DISCOVERING and permits semantic Task creation. The Task's
first approved review gate activates the area atomically. An empty area can also
be explicitly activated or decomposed into independently authored children.
Decomposition does not transfer Task history or agent context.

ACTIVE permits Reviews, Tasks and Executions. Retire a capability
when the capability itself is retired, not when a Task finishes. DECOMPOSED and
RETIRED are terminal and keep history readable. There is no reopening workflow.


Choose **关闭功能领域** for a DISCOVERING or ACTIVE FeatureArea, even while a Task,
Review, Decision or unknown Agent operation is present. Confirmation stops future
development and automatically stops existing Tasks and Reviews. The accepted
request immediately shows **关闭中**, with server-provided stop progress; Bridge
continues after restart without Refresh or another close request.

Completion shows **已关闭**. Closed / All filters and the Card's Feature and Task
selectors preserve history. Engineering actions are unavailable while closing and
after closure. Review, Task, Execution, Run and Decision records are
retained. **Agent结果未知 · 已隔离** means Bridge authority is permanently revoked;
it does not claim the historical backend process is physically dead.

## Review Approval before development

A Review owns findings under its existing Task. Only the Task's current Review
can satisfy REVIEW_APPROVAL. Approval freezes that Task's initial revision;
rejection cancels it, and revision requests another Review on the same Task.
GPT, Local MCP, Web and Card use the same Core decision.

## Review activity and Token usage

当前活动默认折叠，点击或使用 Enter/Space 展开。普通刷新保留当前抽屉的
展开状态，切换 Review/FeatureArea 后重新折叠。终态显示“最后活动”；等待
权限或输入的摘要优先显示。活动诊断仍按需展开，不重复完整 Agent message。

Review 详情和 Task 中 Review 活动的 Token 来自同一个 exact Review Run。可用时显示
实际提供的精确值（含可选上下文窗口），统计中显示“统计中”，历史缺失显示
“不可用”。Token 状态不会改变 Review 是否完成。

已挂载的 ChatGPT Dev Bridge Card 会在工作活跃时每 5 秒静默刷新并原位显示
下一阶段/审批，等待用户或终态后停止。此功能不会主动唤醒 GPT 发新消息。

## GPT Review completion

Start or continue a GPT Review from the bound upper LLM. Standalone WebUI can read
its status/report and present authorized decisions, but does not impersonate GPT.
The upper LLM supplies the complete review instruction and explicit scope, uses
the returned Review file target, then submits the formal structured report to Core.
Use `forbiddenPaths` for actual path prohibitions; ordinary tests filenames are not
automatically converted from prompt text into policy.

Ending file access means **等待 GPT 提交审查结果**. Only accepted report submission
closes authorization, releases the claim, and completes the Review. BLOCKED or
FURTHER_REVIEW_REQUIRED concludes that Review without permitting implementation.
APPROVAL_READY still requires the existing explicit user decision. None of these
states means the Task has been accepted.

After a restart, upper access is **待恢复**. Explicit recovery preserves Review/Run
identity and baseline, changes its access generation, and requires replaying the
original start request. Canceling a Review does not require a fabricated report.

See the [canonical contract](../architecture/unified-review-executors.md) for exact
requests, evidence ownership, immutable receipts, scope snapshots and error states.

When a create-only Task has not selected a Repository yet, `NONE` may still create
it. The later `CONFIRM_AND_START_IMPLEMENTATION` proposal must explicitly supply
`execution.repositoryId` and its full immutable `baseCommit`. The Card displays
that target before the user confirms; an already selected Task baseline is retained.
