# FeatureArea 多 Task 与 canonical Workspace 验收

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-73b0694c3e3519d6)。

`FEATURE_AREA_MULTI_TASK_CONTINUATION_AND_WORKSPACE_OWNERSHIP_READY`

## 基线与交付边界

- Branch：`codex/remove-task-preparation-v0-1`。
- Worktree：`/private/tmp/devbridge-remove-task-preparation`。
- Starting HEAD / final HEAD：`6beb7fcbdd1363c3da36914fd5df7b53c90d7fe0`。
- 在原有 READY、未提交的 Preparation removal 基线上实施。开始时核验 branch、HEAD、working tree、final42、生产残留及 ownership/projection；保存基线文件 SHA manifest 和 patch，用于区分本次增量。
- 原始 checkout `/Users/phoenix/Documents/devbridge` 的 HEAD 相同，最终 working tree 仍干净。
- 所有修改留在上述 architecture worktree。没有 Git commit、暂存、merge、push、release、reset、checkout 或 stash 操作；测试中的 Git commit/checkout 仅发生于临时 fixture Repository。

完整生产契约见 [FeatureArea continuation](../architecture/feature-area-continuation.md)。

## 最终模型

```text
Project
└─ FeatureArea [1:N]
   ├─ canonical AgentConversation
   ├─ canonical Workspace
   └─ Task [1:N]
      └─ Execution [1:N]
         └─ Run [1:N]
```

FeatureArea lifecycle 保留 DISCOVERING、ACTIVE、RETIRING、RETIRED，以及既有显式拆分结果 DECOMPOSED。没有 COMPLETE、SUCCEEDED、FAILED 这种 Task terminal lifecycle。Task 结束后 FeatureArea 仍 ACTIVE；正常 terminal/quiesced/claim release 后为 `FEATURE_AREA_IDLE`，nextAction 为 `WAIT_FOR_USER_TASK`。

Current Task 与 Latest Task 分开。idle 时 currentTask 缺省；latestTask 保留精确 ID 和 terminal status。CREATE_TASK、VIEW_DETAILS、RETIRE 可用，RESET_AGENT_CONTEXT 遵守既有 eligibility；Project Context 和 Development Branch prerequisites 仍约束新工作。选中的历史 Task 保留原来的 terminal stages，不把它的结果当成 FeatureArea 终结。只读 Review Task 结束后，待处理的可选 Approval 也不会禁止下一意图。

`task_create` 现在是 ACTIVE FeatureArea 的正式下一意图入口，复用现有 closed Review admission input（requestId、reviewInstruction、reviewPlan、model、reasoningEffort 和可选 repositoryId），创建新的只读 REVIEW Task/Execution。DISCOVERING 仍使用 featureArea_review_start。Development/maintenance 必须由该新 Review 的 exact Approval 创建独立 DEVELOPMENT Task、immutable TaskDefinitionRevision 和 confirmed Execution；Core 自动启动 Implementation。没有复用旧 mutation authority，没有恢复 legacy free-form payload，没有引入新 workflow/stage/aggregate。

## Workspace、baseline 与 context

最终 schema 仍为 **v42**，直接修改 final schema，共 324 个 schema objects；没有 v43。Workspace 必填 Project/Repository/FeatureArea/base_commit，FeatureArea 保存 current_workspace_id。已删除 Workspace 的 Task/Review owner 列和对应 API。SQLite 约束 canonical uniqueness、owner/base immutability、pointer replacement、Run association 和 worktree exact provenance。

首次 repository-backed 工作从已选择 Development Branch 当时的 exact commit 创建 Workspace。先持久化 Workspace identity，再创建确定路径/分支的 worktree，并持久化 metadata/attachment。物理创建后、metadata attach 前发生 crash，可依据相同 Workspace identity 核验并恢复同一 worktree；不会 reset 已有成果。复用验证 Repository、路径、分支和不可变 origin，异常时 fail closed。

后续 Review、Implementation、Verification、后续 Task 均 reuse exact Workspace ID/path。先前 commit/uncommitted changes 都保留。Workspace 与 AgentConversation 同属 FeatureArea，但生命周期独立；Agent Context Reset 只换 conversation，workspace 不变。

Review provenance 使用 `FEATURE_AREA_WORKSPACE`。Review baseline/ref 是 integration authority；Run start/end snapshots 是实际工作目录事实。所选 Development Branch 与源 checkout、FeatureArea working branch 分开。workspace 内合法演进不是 drift；外部 Development Branch 前进是 integration concern。Task terminal 不自动 merge、不清理 workspace。

## Safety 与 authorization

同 FeatureArea 继续只有一个 active Coding Agent operation；不同 FeatureArea 使用独立 workspace 并发执行。terminal/quiescence/claim release 仍经过既有 atomic finalization，没有第二 finalizer。

UNKNOWN、recoveryRequired、stale callback 和 runtime epoch fencing 继续阻止未解决的操作被后续工作越过。canonical recovery/UNKNOWN_ORPHANED 可永久撤销旧 authority、隔离 exact old workspace/context，并解除旧 current pointer；后续安全操作才可创建独立替代 workspace。旧 Run/Workspace 仍可作为历史 evidence 查询。普通 Task 完成、关联取消和 context reset 不会偷偷替换 workspace。

显式 RETIRE 拒绝新 Task，使用既有 canonical cancel/recovery 收敛。共享目录不共享 mutation authority：后续 Task 的授权只来自自己的 immutable definition。Review runtime ceiling 为 read-only；Development 为其批准的 workspace-write ceiling。保持 trusted-agent 和 SCOPE_CONFLICT / REVIEW_REQUIRED stop contract，没有增加 changed-files compliance enforcement。

“提交已有修改”已经通过普通后续 Development Task 在相同 workspace 中完成测试；不等于 merge/push/release 授权。

## 验证与证据

所有 backend turn 使用 deterministic fake/stub 或受控协议 fixture；没有真实付费 Codex Turn。

| 验收项 | 结果与证据 |
| --- | --- |
| A → B → C | `TestFeatureAreaThreeTasksKeepWorkspaceAndExactAuthority`：三个 Development Tasks，各自 Review/Approval/revision；A 留下修改，B 读取同一路径并缩小 scope，C 在同一 workspace commit；每次 terminal 后 ACTIVE/idle，MCP canCreateTask=true |
| Review/Development 权限 | 上述测试逐次核验 read-only / workspace-write runtime ceiling；B 未继承 A scope 或 revision/approval |
| Context Reset | A 后 rotation conversation，workspace ID/path 不变 |
| Integration baseline/drift | source checkout 单独前进，workspace 仍从 selected baseline 建立；workspace commits 不触发 integration drift，选定 ref 前进则报告 drift |
| 不同 FeatureArea 并发 | `TestDogfoodCrossFeatureAreaReviewsAndOriginalCompletionCorrelation`：两个 Run 同时 RUNNING，workspace/worktree ID 不同 |
| terminal vs 下一 Task race | `TestFeatureAreaTerminalRacesNextTaskAdmissionOnSameWorkspace`：同一 DB 下 concurrent finalization/admission，最多一个 claim；无 phantom Task，workspace 保留 |
| 创建/重启/immutable owner | `TestFeatureAreaWorkspaceCanonicalOwnerConcurrentCreationAndReopen`：12 并发创建者只得到一个 workspace，重启保留；owner/base/错误 lifecycle/pointer mutation 被拒绝 |
| 物理 worktree crash | `TestCanonicalWorktreeCrashRecoveryKeepsCommitsAndDirtyFiles`：重建 Manager 后恢复同一 path，commit/dirty files 保留，错误 branch/owner 被拒绝 |
| UNKNOWN/recovery/cancel/retire | 既有 runtime safety、orphan、callback、cancellation、retirement suite；恢复后新增 workspace 不复用被隔离 ID/path；历史 Run 保留 exact workspace association |
| 全量 Go | `go test ./... -count=1` PASS |
| 静态检查 | `go vet ./...` PASS；本次修改 Go 文件 gofmt clean；git diff --check clean |
| Race | execution、localmcp、controlapi、workspace、worktree、featurearea、run、workflowprogression、workflowpresentation 全包 PASS；SQLite 相关 ownership/continuation/recovery/finalization 定向 race PASS |
| Web/Card | 264 tests：260 PASS，4 原有 opt-in fixture cases 未启用；0 FAIL |
| Desktop | Go 全包 PASS；frontend 43 PASS；Vite build PASS |

一次扩大范围的 SQLite 全包 race 在历史迁移矩阵 `TestMigrationsUpgradeV3ThroughV9ToV14` 触及 240 秒总超时，未报告 data race。随后独立完成本次相关 SQLite race 套件和 finalization/UNKNOWN/stale callback 套件；完整历史迁移矩阵已在非 race 全量 Go 测试通过。保留原始超时日志，未将该次全包 race 标成通过。

证据目录包含完整命令、最终 gate logs、架构场景输出、原始 race 超时记录和追加定向 race 结果。Desktop 首次受沙箱本地 bind 限制的运行，在允许临时测试端口后原样重跑通过。

## 改动与字符串审计

- [本次增量文件清单](archive/feature-area-continuation-v0.1.md#file-05cf0a54d144a1a5)：相对于保存的未提交 removal baseline。
- [完整 Git HEAD 差异文件清单](archive/feature-area-continuation-v0.1.md#file-92f3a4fe4b0bec7f)：包括原有 removal 和新增文件。
- [最终 working tree](archive/feature-area-continuation-v0.1.md#file-755686e61b745f01)。
- [Preparation 残留逐行审计](archive/feature-area-continuation-v0.1.md#file-00f4510b14126456)：生产 Go/SQL/API/enum/UI **零匹配**。其余为明确历史迁移/验收资料、negative invariant tests 和审计记录；不参与生产 ownership 或 runtime。
- [WORKFLOW_COMPLETE / fresh-workspace 审计](archive/feature-area-continuation-v0.1.md#file-de3cf9698aa36e2e)：六处剩余匹配均已分类。三处生产 helper 仅表达 Task-local terminal fact，ACTIVE area publication 前转为 idle；两处为历史记录/fixture，一处是 UI projection refresh。没有保留每 Execution fresh workspace 的生产规则。

已修正 single-current-Task、terminal Task 禁用 continuation、Review/Task owns Workspace、每 Execution fresh worktree、以 source checkout 代替 selected Development Branch baseline、context reset 隐式丢弃工作目录等旧假设。源码、closed MCP schema、Console/Card 最小 fallback、架构文档与测试一并同步；没有 WebUI/Card redesign。
