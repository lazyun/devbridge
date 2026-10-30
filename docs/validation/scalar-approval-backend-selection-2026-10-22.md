# 单仓库审批后端选择修复（2026-10-22）

已部署 `0.1.0-dev.full-flow-20261022.24+gd3969cf`，数据库 schema 仍为 130。

## 原因

原任务的单仓库 v1 审批需要显式冻结 Coding Agent 后端与模型配置，即使审批的启动效果是 `NO_START`。选项接口和卡片正确要求这些参数，但 `CommitConfirmation` 的预检把 `APPROVE` 一律排除，仅保留单仓库 Direct File 验证例外。因此合法的 `CODING_AGENT` 配置被误拒绝为 `BACKEND_SELECTION_NOT_APPLICABLE`。

原请求 `confirm_f24e6047-1b25-4d09-9003-2a7a2e860242` 已记录为 `REJECTED_NOT_CONSUMED`，原 Intent 仍 `PENDING`，允许 `EDIT_SELECTION_AND_RESUBMIT`。拒绝不是审批成功，也没有启动实施。

## 修复

Core 选项查询、配置资格检查和提交预检共用运行配置适用性判断：单仓库 v1 审批接受并验证显式配置；Workset v2 的仅定义审批仍不接收启动配置，后续启动独立选择。非 Codex 执行器、延后审查、配置指纹、后端连续性及模型能力检查保持约束。

仅审批仍使用 `NO_START`，不因后端选择而派发实施。未重写原拒绝回执，也不重放已拒绝的请求。

## 验证与更新

- 新增真实 Facade/SQLite 回归，覆盖单仓库审查报告、审批选项、缺失或错误配置的消费前拒绝、正确选择后的确认完成与精确重放；审批后仍只有原审查 Run，报告与 Worktree 保持不变。保留原直接审批兼容测试。日志：`build/scalar-approval-backend-focused.log`。
- 回归覆盖任务审查延后与配置隔离、拒绝未消费编辑资格、v2 定义审批后显式启动、单仓库及多仓库 Direct File Codex 验证。日志：`build/scalar-approval-backend-regression.log`。
- `go vet ./internal/controlapi` 与相关差异检查通过。日志：`build/scalar-approval-backend-vet.log`。
- 经 Desktop 停止受管服务，备份数据库（0600）及三个旧二进制，再原子更新并启动。三个服务与隧道均 ready，真实连接器诊断返回 `.24`。
- 更新前后 8 条 Run 的状态、Thread/Turn、原始结果摘要一致，分析记录仍为 1；原 Intent 和提交台账的行数与摘要一致。原任务未被重新创建，审批没有自动重提。
- 真实连接器读取原确认选项仍返回 `available=true` 与 `EDIT_SELECTION_AND_RESUBMIT`，原卡片可由用户再次点击“仅批准方案”，以新的请求标识提交同一冻结内容。该用户操作尚未作为本次修复的已完成事实。

本机备份与前后摘要：`build/full-flow-update-20261022-24/`。构建日志与备份为忽略的本机材料，不随本文提交。
