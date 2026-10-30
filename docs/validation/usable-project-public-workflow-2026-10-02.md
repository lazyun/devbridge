# 项目公开流程闭环验收

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-2a8779fb6a4171df)。

日期：2026-10-02。接续 [Frozen Skeleton 定义审批归档](skeleton-frozen-definition-approval-archive-2026-10-02.md)，本批目标是让用户从项目配置走到实际 Git 交付，并能在响应丢失及进程重启后读取原结果。

## 实现范围

`skeleton/**` 仅作为流程约束，保持冻结：commit `4406e717949af5bce6547af0b71525b74e206c81`，tree `942d056a9c78a7e1b28319f4e535bdc893b3509a`。它不是业务可执行模块，不运行其整包回归，也不以 Skeleton 测试替代生产验证。本批改动生产 Core、Local/GPT MCP、Console/Web、启动探针及当前使用文档。

原先 `APPROVE / NO_START` 已批准 Coding 定义，却缺少可从公开接口完整提交运行选型的启动路径。本批补齐以下接口和约束：

- Task 读取投影给出精确 revision、Execution 指针、primary member 和 approved workset hash。Local `workspace_get` 提供当前模型与后端选项，`execution_start` 明确提交本次 runtime/backend；原 Task 定义保持不可变。
- AI 启动组合既有持久 `START_APPROVED` confirmation。同一 session/request 的目标或选型变化发生冲突；读取历史回执先于实时模型策略校验。原请求完成后不会因模型列表变化而新建执行。
- GPT confirmation 区分 `EXPLICIT_SELECTION` 与 `FROZEN_EXECUTION`，卡片对新执行收集选型，对已冻结执行显示原配置；未知模式拒绝挂载。Verification 继续传递完整 v2 target。
- Web 使用实际 local operator 身份启动。v100 保存独立的不可变 operator command，验证 actor、原审批、revision、workset、请求和选型。审批来自真实 operator 时允许其 AI session 为空，不创建虚构 session。事务中同时写入 command、candidate 和 admission，失败全部回滚；已提交但未完成 preparation 的原请求可安全续做。
- 启动读回失败时，独立 `receipt` DTO 保存已经核验的原 Execution/Run ID，完整 Proposal、Run 和 handoff 仍只来自成功读取。权限拒绝、跨 owner 或身份不一致不暴露关联对象。MCP 保持 `isError`，HTTP 保持非 2xx，同时保留可证明的部分回执；错误不会成为成功。
- Web 丢失响应后保留原 request/body，只允许重放已保存的未知或部分请求。当前按钮消失不妨碍原请求读回；当前表单内容不能替换旧请求。
- 桌面 runtime probe 显式向两个 adapter 传递 Core URL，使三个进程连接到同一个随机端口实例。

这些路径使用生产持久层、作用域校验和运行器，不构造平台资格或 formal runtime binding。新的 operator command 是 Core 的独立启动来源，不冒充 Skeleton 的平台正式签发来源。

## 一键公开接口验收

在仓库根目录运行：

```sh
./scripts/verify-usable-project.sh
```

脚本从当前源码构建 Core、GPT MCP、Local MCP 三个可执行文件，在随机 loopback 端口和独立 DataRoot 启动。`TestUsableProjectCandidatePublicLifecycle` 全程使用公开 HTTP/MCP API，并创建三个临时 Git 仓库；不向 SQLite 插入预设成功状态。

覆盖链路：注册项目 → 选择项目和各仓库开发分支 → 创建 FeatureArea/Task → Upper Review 实际文件读取及报告 → 仅审批 → Local 显式选择 runtime/backend → 实施 → 验证 → GPT 验收 → 两仓 Task commit → FeatureArea merge → 关闭并重启三个进程 → 重放原请求。

独立断言验证两仓文件与 Git 历史、第三仓未改变、原步骤和冻结选型一致、原三个 Run ID 一致、修改旧 payload 被业务拒绝、每仓交付提交只出现一次且是最终 HEAD 的祖先。App Server wire 只有原实施和验证两次 turn；重放不再启动 turn 或产生 Git 副作用。

App Server 是本地确定性协议后端，实施文件写入和验证 shell 命令均真实执行。此门禁证明产品流程与持久恢复能够跑通，不是在线模型能力、供应商可用性或模型输出质量的测量。日志、协议消息、临时仓库和 DataRoot 保存在脚本打印的证据目录。

## 验证记录

本批证据汇总目录：`/private/tmp/devbridge-usable-goal-y4058s7_`。

读取故障和 operator handoff 归属检查调整后，最终一键验收通过。`one-command-final.log` 记录测试 13.45 秒（包耗时 14.340 秒），使用最终源码刚构建的三个生产二进制。该次临时项目位于 `/var/folders/kt/_smk6hpd3z994h13fxg2q18w0000gn/T/devbridge-usable.JExFjL/public-lifecycle-485158370`：

- Project：`project_08af2a8c-585f-408a-9898-053efd596397`
- Task：`task_d2da8c1d7e6a326aff778c46d1c9ba3d`
- Review：`review_c3be3e1339964a3e6b6470028dd13042`
- Execution：`execution_approved_7a6220d832348f5349f3e6bcef11eeff`

此前 `one-command-acceptance.log` 的 40.42 秒和 `one-command-final-receipt.log` 的 28.63 秒通过记录作为过程证据保留，不替代最终复测。

专项证据还包括真实 Local MCP 的首次响应丢失、跨进程原请求回放、移除实时模型/后端后零新增写入；真实 Console operator 审批和显式启动、admission 事务回滚及 preparation 失败续做；实际 MCP/SQLite 重新生成的两份卡片 fixture 和浏览器脚本断言。修复前 overlay 能重现 START_APPROVED 被错误要求旧锁定选型，以及 operator 审批来源读取失败，日志保留对应失败后再修复的过程。

| 检查 | 结果 | 日志 |
| --- | --- | --- |
| 最终三个生产二进制构建和公开全流程 | 通过；13.45 秒 | `one-command-final.log` |
| 最终 operator 两个端到端及四个读取边界测试 | 通过；21.052 秒 | `operator-final-handoff-owner.log` |
| 最终真实 Local MCP 启动、响应丢失及冷重放 | 通过；11.867 秒 | `devbridge-approved-local-start-final.log` |
| 原回执读取错误、未验证 partial、跨归属和访问拒绝 | 通过；0.740 秒 | `devbridge-approved-start-readback-final.log` |
| 最终 MCP SDK 的选型 schema、receipt 错误输出和 v2 目标 | 通过；1.201 秒 | `devbridge-approved-start-surface-final.log` |
| Web/Card JavaScript | 315 pass，4 skip，0 fail | `ui-final-receipt.log` |
| MCP 新选型 schema、v2 验证目标和卡片 fixture 专项 | 29 测试节点通过 | `mcp-final-targeted.jsonl` |
| Core API 未完成测试续跑 | 68 顶层 pass、19 skip；312.918 秒 | `regression-remaining.jsonl` |
| Local MCP 未完成测试续跑 | 12 顶层全部通过；62.791 秒 | `regression-remaining.jsonl` |
| 九个受影响 Go 包 `go vet` | 通过 | `vet-final-receipt.log` |
| 最后 operator 错误分支改动后的 Core/HTTP `go vet` | 通过 | `vet-final-operator.log` |
| 本批 Go 格式、全工作区 diff 空白检查、一键脚本语法 | 通过 | `format-check.json` |
| 470 个 Skeleton 文件逐文件 hash 与本批起点比较 | 零改动、零新增；Git tree 不变 | `freeze-proof.json` |

JavaScript 的四个跳过项依赖未提供的外部候选运行 fixture，不计为通过。上述卡片 fixture 专项使用真实本地 MCP/SQLite 重新生成的样本，固定测试时钟以免已保存的确认样本随日历过期。

早期批量回归出现过新增接口 schema 与旧卡片 fixture 失配，以及一次负载下的 discovery 超时；对应修复和独立复测日志已保留。生产 Core/Local MCP 的后续批量运行达到默认累计 10 分钟时限，因此按已通过清单去重，仅续跑未完成用例；没有重复运行 Skeleton。续跑的 99 个顶层测试全部结束，零失败，详细清单见 `regression-remaining-summary.json` 和 `regression-cumulative-accounting.json`。

Core 续跑含 106 pass / 62 skip 测试节点（包括父子节点，不能与其他专项直接相加）。其中 57 个跳过节点缺少 `DEVBRIDGE_OFFLINE_CODEX_CONTINUITY_BINARY` 配置，5 个未启用真实 provider。另有六个父测试显示 PASS，但子项全部 SKIP，不视为其业务场景已覆盖。Local 续跑 19 个节点通过、零跳过。这是分批生产回归与新入口专项的结果，不声称已执行并通过全仓 `go test ./...`。

历史 opt-in candidate 测试和在线 `real_e2e` harness 仍含已退休入口或旧 fixture 假设，未把它们列为当前流程通过证据。当前门禁位置和离线边界已写入 [workflow acceptance](../testing/real-e2e-gates.md)。本批前段曾运行 `make -C skeleton generate-check production-parity`（`frozen-parity.log`），只校核生成一致性及 Task phase 词汇映射，不是整包回归，也不作为业务流程通过证据。后续按用户要求，Skeleton 只用于约束审阅，验收落在生产实现上。

验证使用临时仓库，不操作用户项目的 Git 交付。既有未提交修改保留，本批未创建 Git commit。
