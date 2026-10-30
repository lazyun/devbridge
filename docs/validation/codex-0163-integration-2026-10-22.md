# Codex 0.163.0 接入验证（2026-10-22）

本次按用户确认的顺序实现协议兼容、历史分页、执行中追加指令及原生运行状态展示。没有启动或修改现有业务任务，没有提交或推送 Git，也没有替换用户全局安装的 Codex。

## 实现

- 独立 App Server 版本准入增加精确版本 `0.163.0`，保留 `0.149.0`、`0.159.2`。
- 现代后端恢复指定回合时，先读元数据，再使用 `thread/turns/list(itemsView=full)` 有界分页。最多 100 页，每页 50 个回合；空、过长、重复游标均拒绝。恢复订阅使用 `excludeTurns=true`，不额外启动回合。明确选用的 0.149.0 保留其旧读取路径；现代后端的分页错误不会降级为完整历史读取。
- 原生 `thread/status/changed` 只记录已绑定会话的状态及受支持的等待标志。界面区分运行、等待授权、等待输入、空闲、未加载和错误；断线或结束后标记为最后记录。该数据不批准操作、不完成任务。
- Desktop / Web Console 的运行会话增加“执行中追加指令”。追加说明绑定精确 Run、Thread、Turn、连接、运行 epoch、操作、冻结版本及后端实例。已有授权或输入请求、取消、最终收尾、失效身份、结束回合及未知发送均禁止新发送。
- schema v129 只新增不可改写输入的 `runtime_steering_receipts`。Core 先持久化一次发送意图，再由原客户端发送一次 `turn/steer(expectedTurnId=...)`。回执区分 `DISPATCHING`、`ACCEPTED`、`REJECTED`、`UNKNOWN`；丢失响应或重启后的未完成发送不自动重试，也不能换请求 ID 绕过。
- 用户身份由现有本地操作员认证边界派生，客户端不能提供 `submittedBy`。重复请求只读原回执，变更身份、目标或正文会冲突。字面正文最多 16 KiB，每 Run 最多 100 次。追加说明不替代方案修订、文件授权、审批或验收；普通 ChatGPT 与 Work / Cloud 既有路径保留。

## 原生协议证据

通过 npm 官方包隔离安装 0.163.0，未覆盖全局版本；两个原生版本均连接本机模拟 Responses 服务，使用独立 home、工作目录、Unix socket 和子进程，未使用真实模型或凭据。

`scripts/probe_codex_continuity.py --scenario modern` 在 **0.159.2 与 0.163.0 均 PASS**：元数据读取不带完整历史、分页返回精确活动回合、断开后仍在同一实例恢复、错误回合 steering 被拒绝、正确输入被同一回合接收并记录、原回合完成、item 分页和状态通知可读。每个实例仅有两次合成 Responses 请求，进程和 socket 身份保持一致。

0.163.0 规范摘要 SHA-256：`8c4e9766471ace9cfca0243e4bffa0cb0a597aa1a84fc8d154760360584ed969`。

结果及合成 wire 日志位于 `build/codex-0163-native/`。参考 [官方 App Server 文档](https://learn.chatgpt.com/docs/app-server) 与 [官方更新记录](https://learn.chatgpt.com/docs/changelog)。

## 自动与页面验证

- 前端完整测试 **137 通过，0 失败**：`build/codex-0163-node.log`。
- App Server、Codex 客户端完整测试通过；Agent Runtime 完整测试通过。其旧测试助手曾把重复确认的活动时间推进误作处理证据，现改为等待真实投影队列排空，保持重复确认无副作用。
- 定向 Go 回归通过，覆盖版本准入、分页循环拒绝、原回合恢复、状态归属、持久化幂等、未知发送封锁、授权/取消/收尾阻止发送及实际 Go transport → SQLite → 原回合结束：`build/codex-0163-focused.log`、`build/codex-0163-steering-transport.log`。
- 数据库实际关闭、重新打开后读取原回执，不重复投递：`build/codex-0163-restart.log`。
- Console API 完整测试通过；受分页影响的生产恢复准入测试通过：`build/codex-0163-ui-api-go.log`、`build/codex-0163-control-recovery.log`。
- 相关包 `go vet`、相关文件 `git diff --check` 通过。三个服务二进制已编译。
- Chrome 隔离预览中实际填写并提交合成说明，看到原文与接收回执，始终留在当前页面；未连接正式数据库。截图：`build/codex-0163-steering-preview.png`。

## 完整回归限制

不能宣称整体 Go 测试全绿。SQLite 完整回归有 **26 个顶层测试失败**，HTTP 完整回归有 **10 个顶层测试失败**，涉及既有任务版本/运行选择、交付迁移夹具及必需检查准入。仅移除本次 v129 迁移的对照运行中，失败名称集合全部一致。它们未在此次接入中修复。

证据：`build/codex-0163-go.log`、`build/codex-0163-pre129-baseline.log`、`build/codex-0163-ui-api-go.log`、`build/codex-0163-http-pre129-baseline.log`。完整 Control API 批次中止，未作为通过证据；其相关恢复准入已单独限定时间验证。

## 本机部署状态

已生成 `build/full-flow-update-20261022-21/{devbridge,devbridge-mcp,devbridge-local-mcp}`，版本 `0.1.0-dev.full-flow-20261022.21+gd3969cf`。

用户解锁 Mac 后，已通过 Desktop 停止受管服务，备份 v128 数据库和旧版三个二进制，再原子替换并启动 v21。Desktop 确认 Core、MCP、本地 MCP 均为 MANAGED / RUNNING / ready，Core 与 MCP 显示版本 `0.1.0-dev.full-flow-20261022.21+gd3969cf`，隧道 ready。三个回环 `/readyz` 均返回 HTTP 200。

只读核对正式数据库已由 Core 正常迁移至 schema **129**，新增回执表存在，活动 Run 仍为 **0**。在 Desktop 打开 skeleton 项目和已完成的验证运行，实际会话正文、命令及退出码、验证报告和工作流事件均正常读取；结束运行没有追加指令入口。没有启动、批准、重验或修改现有业务任务。

备份位于 `build/full-flow-update-20261022-21/before-update/`，数据库备份权限为 0600；替换摘要和验证记录在同目录的 `deployment.json`。截图为 `build/codex-0163-services-deployed.png` 和 `build/codex-0163-session-deployed.png`。

本机全局 Codex 仍为 **0.159.2**，本次更新的是 Dev Bridge 的兼容能力和执行入口；0.163.0 的隔离安装只用于协议验收。
