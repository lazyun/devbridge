# 审批后直接执行（2026-10-22）

已部署 `0.1.0-dev.full-flow-20261022.25+gd3969cf`，数据库 schema 130。

默认 Coding 审批改为 `APPROVE_AND_START`，卡片显示“批准并开始实施”。用户一次确认完整方案和运行配置后，服务沿用该准确 Execution 完成准备及实际派发。显式 `APPROVE` 保留仅审批语义，读取、刷新、展示和历史回执不触发启动。

## 修复范围

- 进度卡片按照 Core 当前能力请求批准并启动，不再默认发出仅批准指令；不可用时不降级为另一个审批动作。保留标签页和 PiP 模式。
- 单仓库 v1 使用原审批与启动步骤，不套用 v2 Workset 的资格围栏。启动消费原命令时保留该命令冻结的 Review 决策身份与载荷摘要。
- Workset v2 保留原三事务及 `PREPARATION_READY` 回执，在实际用户确认入口组合已有准确启动消费者。启动使用原回执中的 Task、Revision、Execution、后端及模型配置，稳定请求重放不重复创建 Run。
- 卡片不把准备完成等同于运行状态；进度卡片资源升至 v10，确认卡片升至 v8，保留旧资源 URI 兼容。
- 历史仅审批不会升级为启动授权。原 Skeleton 任务仍使用原批准版本，另备 `START_APPROVED` 确认入口，不创建替代任务或重新审批。

## 验证

- 真实 Facade/SQLite 与 MCP 服务回归覆盖单仓库批准并启动、v2 批准并启动和已批准方案启动、重复确认只创建一个实施 Run、纯读取无派发、旧仅审批兼容。
- Core/MCP/SQLite/Web 关联回归均通过：`build/approval-auto-start-regression.log`（133.042 / 9.015 / 10.053 / 3.470 秒）。
- 卡片 JavaScript：269 通过、4 跳过、0 失败；新版确认资产夹具从实际 MCP 链路重新采集，未手工改写业务回执。
- 启动与重放竞争检查通过：`build/approval-auto-start-race.log`（82.784 秒）。相关 `go vet` 和差异空白检查通过。
- 经 Desktop 停止受管进程，确认无活动 Run、无 WAL 后备份数据库及旧二进制，原子替换三个服务再启动。三个 readyz 均 200，真实 ChatGPT 连接器诊断返回 `.25`。
- 更新前后 8 条 Run、1 条分析、原 Task、原确认 Intent 及其三条提交台账摘要一致。原审批未重提，未启动实施。

本机备份与状态摘要位于 `build/full-flow-update-20261022-25/`；这些本机构建材料不提交到仓库。

更新后通过真实连接器为原准确 Execution 准备启动 Intent `confirm_f2a938ddf0c47e308555ea589deaf01d`（v1，`PENDING`）。选项资格为 `available=true`，运行配置锁定为 `gpt-6.1-sol / low`。已调用 `confirmation_show` 返回展示内容，但未获得 Host 实际显示确认，故不宣称已显示卡片或启动实施。旧审批仍为完成状态；新启动需用户点击，模型未提交确认。
