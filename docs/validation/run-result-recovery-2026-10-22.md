# 按 Codex Run 的结果接收状态展示恢复需求

## 语义

找回结果只针对尚未确认最终结果的那一轮 Codex 运行。结果已收到、等待 GPT 分析、用户审批、工作区冲突和交付阻塞不能统称为“等待恢复”。

Core 从持久化的原生运行边界与终态观察计算只读 `Run.nativeResultState`：

- `RECEIVED`：准确 Thread/Turn 的终态与停止观察已持久化，成功和失败均属于结果已收到。
- `UNKNOWN`：已进入派发边界，缺少准确终态证据。
- `NOT_STARTED`：持久化边界证明没有派发 Turn。
- `UNAVAILABLE`：当前证据不足以确定结果接收状态。

本地 Run 进入 SUCCEEDED/FAILED/INFRASTRUCTURE_FAILED/CANCELED、出现“已完成”的自然语言，均不能替代原生终态证据。不会解析 Codex 文字或要求 Codex 输出新的结构化报告。

## 展示与准入

共享 `TaskView.runRecovery` 绑定当前 Task、定义修订、Execution/Review、业务阶段和准确 Run；只有 `UNKNOWN` 的 `required=true`。已收到结果显示“结果已收到，待处理”；没有派发显示“尚未开始，待处理”；证据不足显示“结果状态待核对”。其他流程阻塞显示其自身原因。

`nextStep` 将找回、处理和核对准确结果的提示同步给 WebUI、ChatGPT 组件和模型可见的紧凑回执。需要分析的已完成 Run 仍优先显示分析提示。任务列表的精简 DTO 同步携带 `runRecovery`、当前 Execution/Review 及定义身份，避免详情与列表同时显示两套状态。找回结果场景不再提示用户点击并不存在的“分析入口”。

这次调整不修改已冻结的生命周期和准入规则。内部 `RECOVERY_REQUIRED` 仍可能承担历史流程阻塞的权限围栏，但不再被展示层直接等同于“Codex 结果未知”；现有能力、审批、取消和正式恢复路径仍由原协议校验。没有自动重试、恢复、批准、验收或新建任务。

组件 URI 更新为 `ui://devbridge/card/run-result-recovery-v14`，保留 v13 等历史资源读取兼容。已缓存旧 HTML 的 ChatGPT 组件需要重新载入；普通业务数据刷新不能替换已加载的组件代码。

## 验证

- 新增 SQLite 回归证明：同一 Execution 的实施终态不能借给验证 Run；本地四种终态及自然语言不能伪造原生结果；准确原生终态可标记失败结果已收到；没有派发不要求找回结果。
- 新增共享展示回归覆盖 UNKNOWN/RECEIVED/NOT_STARTED/UNAVAILABLE、分析优先级、审查 Run、错误 Run/Execution/定义/来源阶段、历史及只读对象。
- 新增真实 Console 精简 DTO 的序列化与前端列表回归；模型回执 schema 和新旧组件资源兼容通过。
- `workflowpresentation`、`run` 全套及 SQLite、controlapi 相关恢复/分析回归通过；Console 全套通过；MCP 全套排除两项既有文档合同测试后通过，约 41 秒。相关包 `go vet` 与 `git diff --check` 通过。
- Node 共 426 项：422 通过、4 跳过；列表补齐后的相关 60 项再次通过。
- 全量 SQLite 套件达到 180 秒总时限，在历史 ReviewDepth 迁移测试期间超时，未视为全套通过；本次相关测试独立通过。
- 真实数据库隔离副本确认：原实施 Run 为 RECEIVED、分析 RECORDED；原验证 Run 为 UNKNOWN，唯一下一步为“待找回验证结果”，准确绑定原验证 Run。原分析 sourceHash 未变。

## 部署

最终部署 `0.1.0-dev.full-flow-20261022.33+gd3969cf`，schema 仍为 130。三个服务就绪，活动 Run=0。更新前后原 Task、Run、分析、claim 和确认意图逐项摘要一致。

`.32` 首次实际页面验收发现任务列表精简 DTO 遗漏 `runRecovery`，已在 `.33` 补齐。部署回执和最近一次更新前备份位于 `build/full-flow-update-20261022-33/`；隔离读取验收位于 `build/full-flow-update-20261022-32/isolated-read-check.json`。

真实 WebUI 已核对列表、详情标题、当前下一步和任务结果状态均为“待找回验证结果”，入口为“查看验证运行记录”，没有提示点击不存在的分析入口。截图：`build/run-result-recovery-web-live.png`。

原 ChatGPT 会话最初仍加载 v13 历史组件，普通数据刷新只更新了活动文字。按既有授权发送仅重新展示只读组件的请求，加载新资源后，原侧边标签页已显示“待找回验证结果”及准确的原验证 Run 提示，并保留已完成实施与分析。没有点击返回卡片、审批、恢复或创建任务。截图：`build/run-result-recovery-chatgpt-live.png`。期间 ChatGPT 历史消息正文仍有“无法加载此回复”，不将这次组件验收扩大为历史消息问题已修复。

临时构建缓存、候选程序、隔离数据库与测试日志已移除，`build/` 仅保留当前运行程序、最近一次回滚备份及轻量验收材料，约 159 MB。
