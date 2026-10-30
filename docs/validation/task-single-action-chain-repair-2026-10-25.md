# Task 唯一待操作链路修复

日期：2026-10-25。基线为当前工作区（包含此前未提交修复），对应 [审计 A1—A4](task-single-action-chain-audit-2026-10-25.md)。实现与隔离验证后，已于 2026-10-25 部署 `.75` / 面板 v43 / 确认单 v13 / schema133。未消费原 GPT 的确认或向其他对话发送消息。部署证据见文末。

## 候选变更

部署版本为 schema133、面板 v43、确认单 v13。

- Core 增加 Task 操作头、不可变的 intent/代际绑定和冲突选择回执。准备和消费在 SQLite 写事务中比较 Task 全局占用；Session 不再分割同任务的准入。
- WebUI 转发读取时的代际，业务 owner 在准确历史回放后、首次新副作用前检查；同阶段延续/暂存也推进代际。旧请求只能读取原回执，不能改写当前决定。
- 创建后原子绑定新 Task 与原创建父请求。已消费未知父请求在跨事务、重启、其他 Session 请求时持续占用；准确同父请求的后续步骤通过私有 Core 上下文识别，外部 JSON 不能授予该身份。
- 正式恢复确认与后继消费间的空隙继续占用当前链路；队列按当前恢复代际和当前目的链接识别占用，旧回执保留原拒绝原因，不影响当前回执继续消费。
- GPT 和 WebUI 共享 taskView.actionNode，并关闭其他独立操作入口。跨 Session 只返回安全摘要，不泄露私有请求、原 Session 或 intentId。
- 新增仅 App 可用的 confirmation_discard / task_action_conflict_resolve。前者只封存未提交提案；后者须准确 Task/代际及原确认身份，只能淘汰未消费兄弟提案，不能抹掉另一个已消费未知结果。
- 对确定无法派发的历史浅审查补审、正式采纳后在派发前取消的原执行，以及已有准确预留回执、Core 已记录工作区准备失败且没有未决文件变更的文件授权，Core 提供有准确证据的同链路出口。释放写入不可变 task_action_releases 回执，保留原部分步骤；不把它们标成完整成功。
- schema133 不重写历史冻结 JSON；多个遗留候选进入显式 CONFLICT，不猜测最新提案。面板按钮必须来自用户点击并重新核对当前身份、代际、访问资格。确认单 v13 继续恢复 v12/v11 的未知请求快照。严格 WebUI 目标校验包含代际字段，恢复核对统一读取当前 Core epoch。

审查完成后，启动确认若丢失最终回执，使用准确 Review/Run 的正式报告和结果回执恢复其已启动事实；不要求历史 grant 仍处于 ACTIVE。原未明或部分步骤仍保留，释放当前链路并不把它改成成功。

分析、延续、暂存是运行结束后同一决策节点的合法分流；完成合入仍需全部验证门禁。完整合同见 [当前流程合同](../architecture/current-workflow-contract.md#task-当前待操作链路)。

## 验证

使用真实 Core facade、SQLite 迁移和 MCP 协议。运行进程使用现有独立协议 fixture，Git 副作用仅发生在测试临时目录。UNKNOWN 前缀由真实 BeginCommit / RecordStep 构造；不将网络失败或查无回执当成未消费证明。

新增 TaskActionNode 集成用例覆盖跨 Session 并发准备、私有身份隔离、UNKNOWN 后禁止其他写入、重启、关闭提案与迟到提交、同阶段代际变化、旧 Review selector、遗留冲突迁移与重复选择、关闭与提交竞争、WebUI 与 GPT 竞争。竞争测试同时核对业务回执数量，确保失败方没有副作用。

面板测试执行真实脚本及 Host RPC，包括显式冲突选择/关闭、后台禁止调用、旧代际/导航/只读变化、确认单关闭与未知状态保护。真实 Core 请求重新捕获三个确认单生命周期凭据；不手工编辑 wire JSON。测试宿主等待真实 WebCrypto 摘要完成，避免并行运行时提前结束 RPC 驱动。

本轮首次全包回归中，Core 后半段受临时数据库空间不足影响；失败日志未当作通过。随后按全部测试清单分批执行，限制临时占用。

| 检查 | 结果 |
| --- | --- |
| Core 全量清单分批回归 | 430 个顶层测试全部执行；批次发现的 7 项失败已逐项处理并重跑，详见下文 |
| Core 最终修复项 + TaskActionNode 全组 + 新增 operator 修订用例 | PASS，41.428s |
| 完整必需 CUSTOM 验证到合入 | 单独重跑 PASS，11.081s |
| SQLite 全包 | PASS，292.387s |
| MCP 全包 | PASS，56.281s |
| Console API / workflowpresentation / confirmation | PASS；Console 最后更新后另行重跑 PASS |
| bridgehttp 全包 | PASS，101.743s |
| GPT 面板与 WebUI 脚本 | 598 项：594 PASS、4 项按既有配置 SKIP、0 FAIL |
| 三个真实确认单 wire 凭据重新捕获 | 3 项 PASS，8.976s |
| 最后针对性存储回归 | PASS，6.954s（Confirmation、Upper Review、Direct File、恢复队列） |
| go vet / git diff --check | PASS |

分批失败项中，原候选绑定与准备竞争的旧 fixtures 已改为使用准确 canonical 回执或先显式放弃未提交提案，原安全断言保留。真实产品修复包括：文件工作区准备失败后的正式恢复、审查结束后准确启动回执的恢复，以及前一轮发现的 WebUI 严格目标字段和运行时 epoch 不一致。

批次末尾的 CUSTOM 完整闭环有一次原生 Run 的 INFRASTRUCTURE_FAILED，单独重跑通过；该次基础设施失败的具体原因未确认，不标为产品逻辑已复现或已修复。首次整包磁盘不足与这一记录分别保留。4 个脚本跳过用例不是本次为绕过失败新增的跳过。

清理后，原测试日志已归档至 `build/full-flow-update-20261025-75/verification-logs/`；Core 分批清单、JSONL 及逐批摘要位于其中的 `devbridge-task-node-core-batches/`。原失败日志保留，最终修复项重跑日志为其中的 `devbridge-task-node-all-repaired-last.log`。临时目录中的重复日志已移除。

## 边界

本报告不宣称原 GPT 中已挂载的旧卡片已重新加载。生产数据库已完成迁移；旧 UNKNOWN 不能因新版本发布而自动清除。多个独立未知副作用没有可证明的唯一结果时仍保持冲突，需核对原业务回执，不能通过强制挑选或清库解锁。

## 部署验证

2026-10-25 20:11（Asia/Shanghai）通过现有 Desktop 停止托管服务，替换三个运行程序后重新启动，部署 `0.1.0-dev.full-flow-20261025.75+gc4ae1d3`。构建源快照含当前已授权的未提交修复，共核验 2,050 个源文件哈希；安装程序与候选 SHA256 一致。未创建持久回滚备份。

- 隔离候选启动通过，空库迁移至 schema133；两个 MCP initialize、工具目录、v43 面板、v13 确认单读取通过，临时数据和进程已移除。
- 生产启动前没有活动 Run 或占用 claim；启动后 schema133、integrity_check=ok，三个 Task 均有当前操作头。停止前、停止后、启动后及只读核对后，32 张原业务表数量与内容哈希均一致。
- Desktop 中 Core、GPT MCP、本地 MCP 及隧道就绪；实际连接器 bridge_diagnostics 返回 `.75` / ready=true。本机 readiness、两个 MCP initialize 和 Console 通过。
- 实际提供的面板 v43、确认单 v13、WebUI product-app.js 与候选对应；唯一节点字段、代际 schema 和两个 app-only 恢复工具均已核验。
- 三个原任务的只读 WebUI 投影均可加载，未向操作员投影泄露原 Session 的 intentId 或候选正文。一个原任务处于历史多链路 CONFLICT，保留原请求与历史，需要在有权读取的原会话中核对后显式选择；部署不替用户选择或撤回未知请求。

证据目录：`build/full-flow-update-20261025-75/`，包括 `deployment.json`、`verification.json`、`candidate-smoke.json`、`readback.json`、四阶段业务表哈希及程序/源文件清单。部署未消费业务确认、启动工程 Run、修改全局授权策略或向其他对话发送消息；原 GPT 中已加载的旧组件需要重新加载工作台。

2026-10-25 部署后清理：当前候选目录中的三个重复程序与 `.69`—`.74` 的过期程序、源码副本已删除，原部署哈希和验收记录保留；实际运行程序仍在 `build/bin/`，当前构建源快照保留在本版本 `source/`。清理明细见 `build/cache-cleanup-20261025.json`。

后续真实历史冲突的归并缺口已在 `.76` / schema134 修复，原 GPT 可用确认入口已实测恢复，详见 [历史占用恢复](task-action-history-recovery-2026-10-25.md)。
