# The development workflow

```text
Project → FeatureArea → Task → Review → Approval → Execution
→ Implementation → Verification → Task Acceptance → Commit → FeatureArea Merge
```

Create a Task for the engineering intent in a DISCOVERING or ACTIVE FeatureArea.
Its immutable initial definition keeps the original title, problem and constraints. The creation confirmation inside the right-side Dev Bridge panel asks you to choose AI file Review, Codex Review, or explicitly skip Review.
A Review is one read-only operation under that Task. Several Reviews can precede
a satisfied gate; findings and previous attempts remain readable.

If you select **不审查，进入实施确认**, the Task waits for a separate confirmation
of its complete implementation and verification plan. The current
CONFIRM_AND_START_IMPLEMENTATION form also selects backend/runtime, then freezes
and starts the plan in that one confirmation. Legacy CONFIRM_IMPLEMENTATION
remains definition-only. This Alpha
path supports a single explicit Repository and Coding implementation. See the
[review-method choices](feature-areas-and-reviews.md#choose-the-review-method-when-creating-a-task).

Review Approval always applies to the Task's current/latest Review. The default
Coding approval is **批准并开始实施** (`APPROVE_AND_START`): one user confirmation
freezes the complete plan and selected backend/runtime configuration, then Bridge
prepares and starts that exact Execution. No second startup confirmation is needed.
REVISE requires a new Review on the same Task; REJECT cancels the Task. Explicit
approve-only (`APPROVE`) remains available when startup is intentionally deferred.
Neither action creates another Task. Retry the same requestId after response loss.

Start an approve-only Task explicitly, choosing its backend, model and reasoning
effort from current runtime options. A Coding Review's earlier selection is not
an implementation selection. Each Execution contains at most one
Implementation Run and one Verification Run. Verification reports evidence;
The verification checkpoint offers three primary choices: **延续任务**, **完成并合入**, and **暂存任务**.
The compatibility acceptance operations remain available for existing clients: REJECT cancels,
REQUEST_CHANGES opens a new revision and delivery cycle on the same Task,
and REQUEST_REVERIFICATION opens a verification-only cycle on the same revision.
Verification continues the existing Execution with its frozen runtime selection;
it does not choose another model or backend.

For GPT file implementation, all `RUN` verification is also executed by Codex.
`START_DIRECT_FILE` confirms the verification backend and runtime; submitting the
file result closes write access and starts a separate verification-only Execution.
See [Codex verification](codex-verification.md) for evidence and retry semantics.

**延续任务** keeps the same Task and its Review, Execution, Run and GPT analysis
history. Confirm it to release a saved checkpoint, then use the current same-Task
reverification or supplemental Review qualification to prepare the remaining work.
A revised plan still requires its formal Review and implementation approval.
An old unknown result stays unknown; continuation does not declare it passed or
start Codex by itself. Existing runtime writer and permission guards still apply.

**完成并合入** presents the verified source and every target development branch.
One explicit confirmation covers acceptance, commit and merge. Core freezes those
targets before confirmation and marks the Task successful only after all merges
succeed. A failed or partial delivery keeps the Task unfinished, with its exact
receipts and a continuation action for the already confirmed delivery. Target or
source drift stops progression; it never silently chooses a new target. This
operation does not push a remote branch. Required verification and acceptance
criteria must pass, unless verification was explicitly waived by the user.
For GPT file implementation, prepare this confirmation in the original AI Session.

If a target branch advances after completion was confirmed, follow the exact merge
receipt. When Core permits releasing a settled plan, release it, then prepare and
confirm a **new merge plan** for that exact original Task in the same panel. The new confirmation shows the
current target branches and commits. It keeps the original Task, acceptance and
completed repository receipts; the Task finishes after the remaining integration
succeeds. An unknown Git outcome must be resolved through its original receipt.

**暂存任务** records progress, verification results and remaining work, preserves
all evidence and stops further implementation or verification until **延续任务**
is confirmed. This is a business checkpoint and never runs `git stash`.

Legacy `ACCEPT` remains acceptance-only for existing clients; it can commit a Task
without merging it. The new primary completion action combines those operations.
FeatureArea merge remains available for previously accepted deliveries.

Infrastructure failures keep the Task's business phase and expose recovery or an
explicit next decision. Runtime permission requests remain separate. Follow
currentState and nextAction; reads and refresh do not advance the workflow.
FeatureArea stays available for later Tasks after a Task ends.

See the [authoritative workflow contract](../architecture/current-workflow-contract.md).

Upper LLM Reviews use the same Task and approval flow. GPT must submit a formal
structured report; closing file access or saying “review complete” in chat does
not complete the Review. Report completion does not approve implementation.
See [Review executors and completion](feature-areas-and-reviews.md#gpt-review-completion).


## Supplementing a running turn

Open the exact Run in Desktop or Web Console → 运行会话. While Core owns the
current live Codex turn and no runtime approval or user question is pending,
“执行中追加指令” accepts an extra execution clarification. The task scope, model,
file permissions and required checks remain those of the approved plan. To
change the plan, stop and revise it through the existing workflow.

“Codex 已接收” means the native turn accepted this input; it does not mean the
clarification has been implemented or the Task has passed verification. Core
records an immutable submission and a single dispatch attempt. If delivery is
unknown, read the original receipt; refreshing never resends it. A new request
ID cannot bypass an unresolved send. Completed Runs retain their receipts.

Native thread status distinguishes active, waiting on approval, waiting on user
input, idle, unloaded and system error. It is an observation alongside Core's
workflow state. After a disconnect, the UI labels it as the last recorded state;
it does not infer completion or approve an outstanding request from that state.

This input entry is currently in Desktop / Web Console. Ordinary ChatGPT
conversation and Work / Cloud workflows retain their existing tools and approvals.

View a completed Run's **结果与 GPT 分析** tab in Task details or the Run session.
It shows the native Codex output separately from GPT's persisted analysis, including
checks and acceptance criteria. A chat-only analysis is not automatically stored:
when requesting analysis, ask GPT to analyze and submit the report to Dev Bridge
with the exact Run's `run_analysis_get` / `run_analysis_submit` contract. Implementation
analysis remains optional for Core admission. Reading or submitting analysis does
not approve implementation or accept delivery.

In ChatGPT, open the Dev Bridge panel once. When a Task is selected, 后续操作 shows confirmations for that Task. Unsubmitted proposals to create another Task or FeatureArea, and confirmations for other Tasks, stay folded in 历史操作 as 其他提案; they are not prerequisites for the selected Task and are not canceled by browsing. When no Task is selected, creation proposals remain available in 后续操作. Results and details are read inside that same panel. No additional inline confirmation Card is needed for later stages. PiP placement is negotiated with the host.

## 实施范围冲突后怎么继续

实施返回 SCOPE_CONFLICT 时，本次实施停止，任务进入“需要恢复”；已修改文件和证据保留。
先在面板核对这次准确运行的失败原因、获批方案、实际改动及可用操作，不直接重试、验证、验收或合入。

**不需要一律重新审查。**

- 范围和目标不变：Core 完成正式恢复核验，并给出“可续作”的资格后，才能沿原版本续作。
- 需要改变范围：恢复结果为 REVISION_REQUIRED 时，准备 Core 绑定的完整后继方案，
  在 RECOVERY_PURPOSE 正式确认中批准新版本。该恢复修订路径本身不自动增加一次 Review。
- 需要重新判断工程方案：只有 Core 提供同任务的补充审查资格时，才准备补审，再按审查结果批准实施。
- 实际越界、证据冲突或恢复条件不满足：继续保留阻塞，说明具体原因，并使用 Core 当前提供的停止、
  修订或取消选项。不能靠重新审查绕过尚未解决的运行权限与范围问题，也不能新建任务绕过。

最终原生结果未知时，先找回准确结果；“已记录范围冲突”和“结果仍未知”可以同时成立。
[完整分流合同](../architecture/engineering-scope-responsibility.md#implementation-scope_conflict-handling)
区分了同版本续作、正式恢复修订和补充审查。
