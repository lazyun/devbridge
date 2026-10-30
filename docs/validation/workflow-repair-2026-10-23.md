# 全流程审计修复

2026-10-23。对应审计：[workflow-audit-2026-10-23.md](workflow-audit-2026-10-23.md)。发布目标 `.57` / v29 / schema 131，无迁移。

| 问题 | 修复行为 | 验证 |
| --- | --- | --- |
| F1 读取失败清空待办 | 保留成功快照、组件、草稿及展开状态；禁用提交，成功同目标读取后恢复 | 实际组件 VM 注入宿主读错误及恢复 |
| F2 未决确认隐藏 | PARTIAL / UNKNOWN 留在后续操作，Core 原回执和 canResume 继续控制续接；历史只读保持 | 未决项和终态分类、历史禁止提交 |
| F3 创建提案丢失 | 保存准确请求展示关联，同 Session 独立创建提案可显式选择；聚焦 Task/请求后再截断 | 展示状态恢复、提案选择、Session/Task 隔离 |
| F4 修订审查不交接 | 识别 RESOLVE_REVIEW_DECISION 的准确后继 Review/Run，成功用户回执后续接 GPT | 创建、普通审查、REVISE、文件实施及身份变化 |
| F5 状态冲突 | 投影复用运行安全规则的准确 Upper 重新授权证明，历史 UNKNOWN 不再覆盖有效当前状态 | 活动/完成/重启一致，独立 UNKNOWN/CONFLICT 保留 |
| F6 GPT 交接丢失 | 保存未发送工作，后续准确读取后续接；恢复快照先回读确认；发送前保存、不自动重发未知消息；准备重试沿原 requestId | 失败读取、宿主恢复、发送超时、旧身份及显式重试 |
| F7 验证规则晚拒绝 | 新报告/分析发布前执行同一无副作用验证检查；历史解码/成功回放不变 | 拒绝前后数据库相同；原 Review 修正后可提交；必需 CUSTOM 保留 |
| F8 完成语义冲突 | 公开主选项为延续/完成并合入/暂存；COMPLETE_TASK 要求证据与评估通过；旧 ACCEPT 明确为兼容验收 | 通过后完整提交合入，BLOCKED 禁止完成，兼容历史 |

同时增加失败轮询退避，以及列表截断提示。当前主流程说明见 [current-workflow-contract.md](../architecture/current-workflow-contract.md)。没有合并“不审查”路径现存的两次确认；这属于另行产品合同调整。

原生产任务保持 `REVIEW_APPROVAL_PENDING`，原定义、报告、两项必需 CUSTOM 和已准备的审批单保留。本次不提交业务审批，不启动实施，不向原 GPT 会话发送消息。

构建、测试、部署及生产只读回执保存在 `build/full-flow-update-20261023-57/`。实际结果：界面 530 项（526 通过、4 跳过）；agentexecution / review / task / workflowpresentation 通过；execution 全包 123.558s、SQLite 最终全包 335.906s、MCP 全包 71.003s 通过；Core 定向用例覆盖审批仅启动一次、同 Task 延续/暂存/提交合入、必需 CUSTOM 通过及受阻、不可执行报告修正、准确重新授权及独立不确定性；相关包 vet 通过。

已由 Desktop 托管更新到 `.57`，Core/MCP/Local MCP/隧道就绪，schema 131。最终程序/源码 SHA256 与清单一致；重启前后 22 张业务表哈希一致，无活动 Run，原定义、报告及待审批确认未变化。原准确 operation_get 从 UNKNOWN / recoveryRequired=true 变为 COMPLETED / 无可操作恢复，历史 backendDisposition=UNKNOWN 仍保留。真实 WebUI 显示 GPT 审查已完成、等待用户审批。

ChatGPT 原展示结果仍缓存 v28 HTML。已刷新工具、重新加载原会话并重试同一已有展示，实际 DOM 初始化版本仍为 2.16.0-task-panel-v28。服务器旧 v28 资源别名读取 v29 已通过协议测试，但不能使宿主替换已经保存的旧 HTML。因此尚不宣称原会话已完成 v29 端到端验收；发送新的只读展示消息仍等待用户明确授权。没有发送新消息或点击任何业务确认。

## 原 GPT 会话 v29 展示验收

用户明确授权后，通过现有会话消息工具发送准确原目标的只读 panel_show 请求，presentationRequestId 为 workflow-repair-20261023-v29。消息在原网页可见；首次宿主组件加载失败，只重试该展示结果后成功。没有重复发送新消息，也没有重试业务 Run。

原页面实际 DOM 初始化标记为 `task-panel-v29` / `2.17.0-task-panel-v29`，不是只依据 GPT 回复判断。结果标签在最后读取时间更新后仍选中，Reviews 记录保持展开，完整原审查报告可见；已结束历史确认显示“历史操作仅供查看，不能重新确认或续接”，仅保留刷新，审查内容入口禁用，没有提交入口。最终返回“后续操作”，准确原审批单 PENDING 和一条 PARTIAL 未决确认均可见，未打开或消费旧创建提案。

展示前后 22 张业务表哈希一致，原 Task/Review/定义/审批版本与 contentHash 一致，无活动 Run。仅展示与只读浏览，没有选择模型、审批、提交分析、重新授权或启动任务。证据：`v29-host-acceptance.json`、`v29-before.json` / `v29-after.json`、`v29-panel-accepted.png`、`v29-reports.png`、`v29-history-readonly.png`。上节的缓存未验收状态已由本次成功回执闭合。
