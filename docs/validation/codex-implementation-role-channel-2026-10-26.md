# Codex 延续实施角色策略通道修复（2026-10-26）

## 生产诊断

原 Task `task_429bc7f6d36b3c3cab1eccf3027a2ad0` 最新 Implementation 的传输状态为 SUCCEEDED，但工程结果为 IMPLEMENTATION_BLOCKED，未执行实际检查。传输成功不能替代实现、验证或验收通过。Core 拒绝完成并合入符合门禁要求。

只读核对发现，本次冻结 TaskPermissions 的 purpose 为 IMPLEMENTATION，已有原托管工作区的 REPOSITORY 写授权。原生 permission_profile.file_system 同样包含准确工作区的写授权。旧的 sandbox_policy.writable_roots 摘要不能替代实际 file_system profile。

当前 turn_context 的 collaboration mode 已携带本次 IMPLEMENTATION developer_instructions；但同一原生 Thread 的 compacted.replacement_history 仍保留旧 VERIFICATION developer 消息。模型最终声称旧只读角色仍生效。证据支持修复角色策略的持久化与顺序；不能据此宣称 Core 没有授予写权限。

生产证据保存于 `build/implementation-role-channel-20261026/production-readonly-evidence.json`。没有改写生产 rollout、冻结定义、权限或原业务状态。

## 修复契约

新的 executor Turn 完成身份、冻结权限和模型校验并取得本地启动占用后，在原准确 Thread 通过原生 `thread/inject_items` 追加本次宿主 developer 策略，再调用 `turn/start`。策略文本与本次 collaboration mode 使用同一来源，明确覆盖旧阶段角色，但不扩大冻结权限、不修改工程用户输入。

注入失败即拒绝启动并释放本地占用。已运行 Turn 的恢复连接不注入、不重发工程任务。诊断运行继续源码只读；后继验证运行继续使用验证权限。原 Task、原 Thread 和历史保留。

## 验证与交付

已通过原生 Codex 0.159.2 的隔离回归：原 Thread 保留，旧验证角色真实存在于压缩历史；本次 developer 消息已持久化且在模型输入中晚于旧角色；VERIFICATION→IMPLEMENTATION→VERIFICATION 的授权路径写入依次 DENIED / ALLOWED / DENIED，范围外始终 DENIED。

另用真实 gpt-6.1-sol 在全新测试 Thread `01a17402-9d79-7760-9db8-b5afa0e3a4d9` 上运行同一阶段序列。三个阶段均观察到真实 exec 调用，实施阶段实际将临时授权文件写为 IMPLEMENTATION_PROOF，范围外文件保持不变，后继验证读到新内容。临时样例已清除。证据：`build/implementation-role-channel-20261026/real-model-probe.json`。没有启动生产工程 Run。

注入失败/取消的单元回归确认拒绝派发、释放启动占用且可重试。丢失 turn/start 回执后的恢复隔离测试通过。原生回归没有使用付费模型，真实模型测试是独立的临时样例。

使用 llmtools-delegation 技能委派了开发，父代理检查修改并加强了取消、持久化顺序和集成回归。首次外部只读审查服务返回 UPSTREAM_UNAVAILABLE；后续最终代码审查请求被自动审批审查拒绝，理由为没有向该外部目的地披露私有源码的明确授权。没有重试、改道或绕过该拒绝，采用本地父代理审查；不能记为独立外部审查通过。

Go vet、codex 包回归、execution 完整回归（89.517s）、controlapi 受影响的 12 项顶层定向测试及其子场景（76.637s）通过。候选三服务隔离冒烟通过，schema134、面板 v44、确认单 v13 保持。controlapi 完整包回归在套件累计 10 分钟后超时（600.766s），当时当前用例 TestMultiRepositoryDirectDeliveryStaysDeliveringUntilAllMembersComplete 仅运行 2 秒；没有记录为通过。该宽范围套件仍未完整验收，定向通过不能替代它。原 Skeleton 任务仍须沿 Core 当前资格准备正式实施修正并由用户确认，之后执行全部冻结检查和独立验证；本次宿主修复不能替代原任务验收。


已部署 `0.1.0-dev.full-flow-20261026.78+gc4ae1d3`。Core、GPT MCP、本地 MCP 和隧道就绪；三份已安装二进制与冻结候选 hash 一致，2051 个源码文件核验一致。schema134、面板 v44、确认单 v13 未变。部署前后 32 张业务表逐表摘要一致，完整性 ok，没有活动 Run 或 claim；原生产 native rollout SHA256 也未改变。未消费确认、未启动原任务工程 Run、未创建数据库回滚备份。部署证据：`build/full-flow-update-20261026-78/deployment.json`。

原 Task 仍为 ACCEPTANCE_PENDING。当前 Core 提供 REQUEST_CHANGES 和 REQUEST_REVERIFICATION；由于本轮实施缺少新增交付，应沿原 Task 准备 REQUEST_CHANGES 实施修正并正式确认，待实际实施完成后再执行全部冻结检查及独立 Verification。保留现有 7 项验收、8 项 required checks、工作区变更和原历史。本次 DevBridge 宿主测试不构成 Skeleton 的验收通过或可合入证明。
