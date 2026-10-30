# 验证中断后提交实施分析

问题：实施 Run 已 SUCCEEDED、分析状态 OPTIONAL，`sourceHash` 始终不变，但 GPT 提交 `{summary}` 被 `RUN_ANALYSIS_TARGET_STALE` 拒绝。

根因：写事务除证据摘要外，还要求 Task 位于常规执行阶段且 FeatureArea 没有执行占用。原任务处于 RECOVERY_REQUIRED，同一 Execution 的验证 Run 中断后仍保留 claim；因此读接口的 OPTIONAL 与写入准入不一致。

修复：将 EXECUTION 类型的分析写入与执行推进权限分开。只允许当前活跃 Task、当前冻结定义、当前 Execution 指向的成功 IMPLEMENTATION Run；可保留同一 Execution 精确绑定的 VERIFICATION claim，并允许 RECOVERY_REQUIRED 阶段记录分析。仍校验调用者、sourceHash、领域状态、实施取消记录、原始请求及报告幂等性；其他操作的 claim 继续阻断。REVIEW、VERIFICATION、INSPECTION、RECOVERY 分析的既有准入不变。

分析写入只增加不可变报告、回执与分析事件，不修改 Task 阶段、Run 原生结果、验证状态、claim 或审批。不恢复、重跑或验收任务。

验证：

- 新增存储回归：验证等待及验证中断两种场景均可提交、同请求可重放、异请求冲突；写前后 Task、Run、Execution、claim、会话边界、取消与审批数据逐表一致。
- 拒绝错误 sourceHash、事务内证据变化、其他调用者、无关 Review claim、已取消 Task 及正在退役的 FeatureArea；拒绝不落库。
- SQLite、execution、controlapi、mcpserver 的分析相关测试，以及精确验证取消、Task Execution 归属测试均通过；相关四个包 `go vet` 通过。
- 真实数据库的隔离副本使用原 Session、原实施 Run 与 `a40c1cbb006b8722dba7e191f26633cabc832dda71a1e9fbddbc6335e05877b1` 成功记录分析并精确重放；Task、Run、Execution、claim、取消和原确认意图均不变。测试报告只写入副本，未写正式数据库。

已部署 `0.1.0-dev.full-flow-20261022.31+gd3969cf`，三个服务均就绪。正式任务的 Task、10 条 Run、已有分析、执行占用及确认意图更新前后摘要一致，活动 Run=0。真实 MCP 回读原实施分析仍为 OPTIONAL，同一 sourceHash，未提交新的正式分析；原会话可重试原报告。

部署元数据及更新前回滚备份：`build/full-flow-update-20261022-31/`。
