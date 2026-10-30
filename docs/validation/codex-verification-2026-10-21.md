# Codex 独立验证（2026-10-21）

实现范围：所有实际 RUN 验证由 Codex 执行。GPT 文件实施保留独立文件结果；提交时关闭写权限，创建只含 VERIFICATION Run 的 Execution。审批／启动时的显式 Codex 配置不可被 Review 配置或后续输入替换。验收仍使用 Direct File 交付证据。

## 已验证

- 单仓库 v1：批准卡片选择验证后端／模型，GPT 文件修改，真实 App Server 协议验证 Run，命令退出结果，正常验收与提交。
- 多仓库 v2：START_DIRECT_FILE 选择验证配置，逐仓文件结果和快照，验证只读工作集及逐仓命令来源，正常验收与提交。
- 没有 IMPLEMENTATION Run；重复提交不会创建第二个验证 Run；写权限关闭后不能继续写。
- 重启后保留原验证、结果和验收身份。
- 反例：模型报告 PASSED，但一个仓库缺少命令记录，仍拒绝验收。
- 必需 CUSTOM 静态阅读仍在准入前拒绝，未降级检查、未补写成功结果。
- 真实数据库隔离副本升级到 SQLite v128：quick_check=ok，foreign_key_check 无问题；8 张任务、Run、Review、报告、批准、文件结果、决策和通知业务表逐行内容摘要完全保持一致。
- 确认界面使用 v6 资源并保留 v5 入口；隔离浏览器检查验证配置说明、模型／思考等级选择和确认按钮启用。预览没有提交业务操作。
- Node 确认卡片与进度卡片测试：247 项，244 PASS、3 SKIP、0 FAIL。
- MCP Go 全包测试通过；Task、Execution 的验证相关测试通过；新单／多仓库与验证准入等定向集成测试通过。

测试通过真实服务、SQLite、工作区、App Server 协议及子进程命令观察链执行；Codex 使用隔离协议测试进程，不表示真实用户任务已经运行 Codex 验证。

## 回归限制

扩大控制面回归时，以下原有领域合并恢复测试仍失败，报错 GIT_FINALIZATION_PENDING：

- TestMultiRepositoryDirectG6AreaMergeEffectBeforeReceiptUsesFrozenPlan
- TestMultiRepositoryDirectG6PartialDirtyTargetRecoversOriginalMapping

扩大 SQLite 迁移回归时，TestDeliveryRecoveryV118MigrationPreservesKnownAndUnknown 的 APPLIED／UNKNOWN 子项失败：历史 v118 测试数据库缺少 unresolved_cancellation_interrupts。该测试明确只迁移到 v118，新 v128 不在其迁移范围。没有把这些失败报告成全量通过。

当前真实任务的旧审查报告和审批状态没有被覆盖；需要正式修订验证方案，不能把原静态阅读规格直接当作 Codex 命令验证。

## 本地更新结果

本地 Core、MCP 与 Local MCP 已更新并通过 Desktop 托管重启，diagnostics.ready=true，SQLite 已升级到 v128，quick_check=ok，无活动或状态不明 Run。

当前 Task 保持 REVIEW_APPROVAL_PENDING、current_execution_id 为空。仅准备“修订 Codex 验证方案”的 PENDING 确认意图；未消费该确认、未创建后继 Review、未批准或启动实施／验证。Core 需要通过正式 Review 修订更新不可变验证报告。
