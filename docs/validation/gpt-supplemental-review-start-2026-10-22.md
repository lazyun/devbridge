# GPT 确认补审后未启动的修复（2026-10-22）

真实原任务的 `REQUEST_SUPPLEMENTAL_REVIEW` 确认 `confirm_c28fa39e39b5743cf3d6ae9ad7e0d89a` 已消费，第一步 REQUEST_REVIEW 已入库，第二步 START_REVIEW 因 `REVIEW_DEPTH_TOO_SHALLOW` 未启动。卡片冻结了 FAST，而请求补审后 Core 采用的最低深度是 DEEP。原任务处于 REVIEW_REQUIRED，没有新的 Review 或 Run。

根因包含两个接口缺口：准备确认时未核对补审即将采用的最低深度；GPT 精简任务与操作资格未返回相关深度信息。另外，APPROVE 会清空任务当前 required_review_depth，补审 reducer 随后按「任务要求 → 上次 Review 深度 → FAST」确定新要求，仅检查批准后任务字段仍会漏检。

修复：

- 准备和消费前均检查实际有效最低深度，补审使用与 reducer 一致的回退规则。不足时返回来源 DOMAIN、阶段 CONFIRMATION_PREFLIGHT、动作 PREPARE_NEW_VERSION 的明确失败；不先提交补审请求。
- 精简任务公开准确 requiredReviewDepth / allowedReviewDepths，REQUEST_TASK_REVIEW 资格公开补审实际将采用的深度。保留语义深度和审查范围的区别。
- 旧部分消费确认通过只读 reviewStartPreparation 提供新启动准备指引：须原补审请求仍为当前请求、原 Task 和 revision/Review 身份一致、无当前 Execution、准确 START_TASK_REVIEW 资格仍可用。旧状态、子步骤、消费事实及 UNKNOWN 保留，不声称旧启动成功或无副作用，不重放或改写已消费确认。
- 指引明确在同 Task 准备新的 START_REVIEW，采用允许深度与完整原指令，交由用户重新确认。原 Task 推进或 Review 已启动后撤下该指引。
- GPT 工具说明与工作流指令同步该路径；已有统一任务面板展示新的准备项。

验证：

- 真实 SQLite/Facade 复现补审第一步完成、第二步浅深度被拒绝的旧状态。新准备及旧待确认的消费前检查均拒绝浅深度；无补审请求或 Run 副作用。
- 历史回执读取不写数据库；正确深度的新确认在同 Task 创建并启动一轮 GPT Review；新 Review 启动后旧回执不再提供过时准备指引。
- 确认自动启动实施、原回执重放与隔离、验证阶段三类操作及验收提交合入的相关 Control API 回归通过。MCP 回归通过，显式跳过两项既有文档契约测试 TestExternalWorkflowDiscoveryContract / TestCurrentDocumentationFreezesFeatureAreaReviewVocabulary。
- workflowpresentation、confirmation、consoleapi 回归通过；Node 446 项中 442 通过、4 项既有可选跳过。四个相关包 go vet、三程序构建与改动文件 diff 检查通过。

已部署 `0.1.0-dev.full-flow-20261022.39+gd3969cf`，schema 131。服务更新前后 Task、Run、分析、确认、暂存、完成意图、补审请求和订阅摘要一致。

随后按用户排查与继续原任务的请求，仅准备了正确深度的新确认 `confirm_0a0dc920998f6b3ff9034926db35885d`（START_REVIEW / DEEP，GPT 审查，PENDING）。沿用原选中变体的完整指令，仅修正深度和显示摘要。真实 confirmation_options_get 返回 eligibility.available=true；同 Session、准确 Task 的确认列表已读到该项。没有提交确认、创建 Task/Review/Run、启动实施或向原 ChatGPT 会话发送消息。没有取得原 ChatGPT 宿主的显示回执，因此不将“列表已可读取”表述为“宿主已展示”。

部署、原状态摘要与真实 MCP 回查元数据保存于 `build/full-flow-update-20261022-39/`。当前回滚备份位置见 `build/README.md`。
