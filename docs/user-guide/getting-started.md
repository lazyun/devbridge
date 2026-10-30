# Getting started

独立工程状态恢复的合同与验收见 [Recovery Status 记录](../architecture/codex-recovery-status-reconciliation-v1.md)；Project Inspection owner 变体见 [G10 closure](../architecture/codex-recovery-status-inspection-g10-closure.md)。新 Recovery Turn 不证明旧回答被消费；旧 UNKNOWN 与普通 finalization guard 保留。

本轮 CODEX_REQUEST v1 的权威状态、UNKNOWN 阻断、取消/successor、统一客户端投影及验收见 [回答消费状态合同](../architecture/codex-request-unknown-disposition-v1.md)。该合同保留固定 0.149.0 的 `CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED`，历史报告中的阶段性状态不代表本轮验收结论。

Projects may have zero or multiple repositories; Session selection is Project-only.
Review and implementation use an explicit repository target set. Workspace
membership alone never authorizes work in another repository. Configure a
Development Branch for every repository needed by the proposed work.

This path focuses on using Dev Bridge. Installation and packaging commands are
not included here because they are not yet a stable part of the user contract.

1. Launch Dev Bridge Desktop and start the local runtime.
2. Open **Projects**, choose **Add Project**, select a local Git repository, and
   confirm its Project name. Skip this step when the Project is already listed.
3. Explicitly activate Dev Bridge in ChatGPT or another Local MCP client.
4. Select a Project when Dev Bridge presents the available Projects.
5. Let Dev Bridge establish or reuse Project Context.
6. State the engineering goal you want to pursue.
7. Create the Task for that intent, then start its read-only Review.
8. Inspect the current Review proposal and approve, revise or reject the Task gate.
9. To start a Coding Agent implementation, explicitly choose its backend, model
   and reasoning effort, then confirm the exact approved Task. Follow
   Implementation and Verification.
10. Resolve Task Acceptance, inspect the resulting Git commit, then explicitly
    merge the FeatureArea when its work is ready for the Development Branch.

Project selection is always explicit. Dev Bridge does not choose a Project from
an exact name match, a single candidate, the current Web Console view, or
history.

Add Project is registration, not Project Discovery: it records the Project and
canonical local Repository binding without creating a Feature Area, Module,
Task, Review, Execution, or AI session.

Projects also show current and removed Repository sections. **Remove from
Project** first asks Core for current blockers. When confirmation is available,
unlink removes the Repository only from future Project work. It keeps the local
Git directory, managed worktrees and all Task/Review/Run/history. A removed
Repository cannot be linked again or transferred to another Project in v1. If
current engineering, recovery, Direct File or delivery authority exists, resolve
that operation and refresh; unlink never cancels it automatically. See
[the safe unlink contract](../architecture/safe-repository-unlink-closure.md).

Review Approval freezes the Task definition. **Approve only** leaves the Task
ready for a later explicit start; it does not reuse the Review's model or backend
as implementation authorization. **Approve and start**, where offered, combines
approval with an explicit runtime selection. Bridge checks the exact target and
records the chosen action before execution.

GPT decides whether a conversation should create a Feature Area, continue an explicitly
identified Feature Area, or create no Feature Area. Bridge does not decide from consultation or
implementation keywords, title similarity, or importance. A request to fix a bug or implement a design expresses
engineering intent and can create one.

Next, read [the development workflow](workflow.md).

In ChatGPT, an ordinary Bridge reply includes a compact Dev Bridge entry. Click it
or use Enter/Space to read fresh state and expand the same component. Collapse,
refresh and opening a decision target do not approve anything. Explicitly asking
to show/open the card requests the expanded state. Historical entries retain their
exact target; a resolved approval is displayed as resolved.

GPT first calls `session_initialize` and receives a real Session with no Project.
It prepares Project selection with `project_setup_prepare`, then displays the
purpose-specific confirmation with `confirmation_show`. The component confirms
the selected Project through `confirmation_commit`. Branch configuration, Task
creation, Review and implementation use their respective prepare/confirmation
flows. `session_initialize(aiSessionId)` reads the existing binding for later
steps. A missing or conflicting Session fails closed rather than creating a
replacement. Local MCP retains its direct `project_open` and other command
entries; see [the Local MCP guide](local-mcp.md).

Runtime semantic questions now use an immutable current-user decision before any
backend response. Reads, refresh and notifications never answer a question.
See the [Codex request / user decision contract](../architecture/codex-request-user-decision.md) for exact identity,
response states, canonical submission and isolated validation.

## Project Inspection

Project Inspection is the Project-scoped, read-only investigation path. It does
not create or select a FeatureArea. The client first prepares an explicit target
set, shows the repositories, per-repository read scopes, primary repository,
instruction, model/reasoning choice and backend, then confirms that exact plan.
Dev Bridge does not add every repository in the Project or infer targets from the
instruction.

Starting the confirmed Inspection creates managed worktrees for those targets.
The registered checkouts are never used as runtime roots. The result is a strict
structured report with repository-qualified evidence. Recommendations remain
recommendations; creating a FeatureArea or Task is a later explicit action.

Older ContextProbe results remain readable. If they predate the canonical
workspace, conversation, workset and App Server facts, the client displays
`LEGACY_INSPECTION_NO_CANONICAL_RUNTIME`; those rows cannot be resumed. See the
[Project Inspection runtime contract](../architecture/project-inspection-canonical-runtime-v1.md).

When a canonical Inspection Turn is terminal and idle but its formal result is
missing or cannot be reconciled, the shared Recovery Status flow can start a
new read-only Turn on the same Thread. A recovered complete result enters the
canonical Inspection reducer. A recovered partial result requires a separate,
caller-authored continuation command and keeps the same confirmed targets. A
target expansion requires a newly prepared and confirmed Inspection. The UI
continues to show an old unknown answer-consumption state separately from the
new engineering result.


## 可选的 Codex 会话连续性

固定 0.149.0 的独立 App Server 接线由 `DEVBRIDGE_CODEX_APP_SERVER_CONTINUITY=1` 显式开启新操作。Core 重启可重新连接原进程；机器重启或 App Server 退出不等价。未开启的新操作保持原 stdio 生命周期；已经持久绑定的操作仍尝试恢复原实例。

界面分别展示等待回答、回答已记录、恢复发送、接收未知和实时会话丢失。刷新只读，不会重新发送答案。原问题消失或实例更换时应走正式取消/恢复流程，不把旧答案粘贴为新 Turn。当前 consumed/resolved 丢失窗口仍不能证明消费，整体为 partial。

默认不会调用真实模型 smoke。`DEVBRIDGE_REAL_CODEX_REQUEST_RECONNECT=1` 仅用于明确授权的测试，另需显式 executable、独立 config/auth fixture 文件和操作者原始答案；详情见 [continuity 验收记录](../architecture/codex-request-cross-core-continuity.md)。
