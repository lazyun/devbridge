# 不审查路径单次实施确认

修复“不审查”任务确认完整方案后，还要再次确认启动的问题。当前主入口为
execution_prepare(action=CONFIRM_AND_START_IMPLEMENTATION)，界面为 task-panel-v36。

创建任务时选择 NONE 仍只创建 Task 和持久 Review-skip 回执。随后 GPT 准备完整的单仓
Coding workSpec、可执行 verificationSpec、准确 Task/领域/原定义版本及不可变仓库基线。
用户在同一正式确认单明确选择后端、模型与运行配置，确认后 Core 冻结新定义并开始实施。

一个已消费的确认包含 CONFIRM_IMPLEMENTATION 和 START_IMPLEMENTATION 两个持久步骤。
启动必须证明同一确认的前一步已经完成，且 Review-skip 回执、当前 Task、领域、
冻结定义和启动子请求完全匹配。运行配置取自该确认的已消费原载荷，并核对内容摘要、
提交摘要及后端指纹。未确认、错误版本、未完成前一步或其他请求均不能取得启动授权。
首次确认在冻结定义的同一事务中把 DISCOVERING 领域转为 ACTIVE，沿用审查批准路径的规则。

历史 CONFIRM_IMPLEMENTATION 保持只冻结定义，不能因更新获得启动权限；
历史 START_APPROVED 继续使用其准确 expectedTarget 及独立确认的运行配置。
复用同一确认时恢复原步骤，不重建 Task、定义或 Execution。

验证：

- 新端到端测试使用真实 SQLite、Core 准备与已有独立进程 Codex 协议测试端，确认一次操作返回
  RUNTIME_HANDOFF_ACCEPTED，并保存明确选择的后端与运行配置。
- 缺少运行配置在消费确认前拒绝，定义及当前 Execution 保持不变。
- 已冻结定义但响应丢失时，从原持久回执恢复并启动；未完成定义步骤时直接请求启动被拒绝。
- 同一确认重复提交返回相同步骤，只存在一个 Execution 和一条 START_AUTHORIZED；
  改动已消费选择被拒绝。
- 原有独立方案确认、丢失响应及后选仓库测试继续通过，证明旧动作语义保留。
- 确认组件测试验证“确认并开始实施”文案、仓库基线及运行配置输入；MCP schema 验证完整方案必填，
  并拒绝当前不支持的 Direct File 合并确认。
- 相关 controlapi 回归通过（143.101 秒）；MCP 全套通过（51.222 秒）；
  JavaScript 393 项，389 通过、4 跳过；相关包 go vet 通过。
- SQLite 全套通过（281.775 秒）。.65 已部署，Core、MCP、本地 MCP 和隧道均就绪；
  schema 132，服务返回 task-panel-v36。更新前后 22 张业务表摘要、原 Task 和待确认方案均不变，
  无活动 Run。保留 .64 三个二进制和一致性数据库备份。
- 测试日志及部署只读核对记录在 build/no-review-single-confirm-20261024/ 与
  build/full-flow-update-20261024-65/ 中。旧对话没有被重新发送消息或重新挂载面板；v36 资源通过 MCP 读取验证。

范围仍为当前支持的单仓 CODING_AGENT 不审查路径。多仓及 Direct File 使用既有审查批准合同。
本次没有消费真实 Skeleton 任务的待确认方案，也没有向原 GPT 对话追加消息。
