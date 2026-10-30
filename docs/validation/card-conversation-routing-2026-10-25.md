# 卡片消息路由修复与宿主复现

日期：2026-10-25。已部署 `.73`，面板 `v41`，确认单 `v11`，schema 132。

## 已确认的事实

旧实现的兼容消息入口在 PiP/fullscreen 中传入 `scrollToBottom=false`，发送后刻意保留展开面板。原 ChatGPT 对话的多次卡片触发回复归在「使用 devBridge-test」这一旧用户轮次下；刷新后多处显示「无法加载此回复」。这些事实不能单独证明缺少路由参数就是正文读取失败的原因。

用户选择卡片按钮直接在当前主对话发送消息，并明确允许向原对话「同步项目会话信息」发送一条无工具、无审批、无运行的测试消息。

## 已部署的代码变更

- 宿主声明 `experimental["openai/message"]` 时，使用 `ui/message` 并显式设置 `_meta["openai/message"]={target:"active",send:true}`。不创建新对话，不猜测会话 ID。
- 未声明扩展时保持标准 MCP 或 OpenAI 兼容发送入口；兼容入口使用 `scrollToBottom=true`。
- 宿主接收后请求 inline 展示，让用户回到主对话。拒绝或未知发送不切换展示；展示切换本身不改变消息送达状态。
- 在原宿主交接日志中保存 deliveryRoute。保留发送前保存、准确业务回执、未知状态不自动重发等守卫。
- 资源版本更新为 `ui://devbridge/card/task-panel-v41`，保留旧版本资源别名及旧快照兼容。

参数依据：[官方扩展文档](https://developers.openai.com/plugins/build/extensions)、[UI 参考](https://developers.openai.com/plugins/reference)；确切 schema 和能力检测对照官方发布包 `@openai/mcp-extensions@0.2.1` 的 `dist/app/message.js` 与 `dist/app/extensions.js`。SDK 的默认值本来就是 active/send=true，因此不能仅凭显式参数判断宿主一定形成独立持久轮次。

## 验证

- `node --test internal/mcpserver/*test.js`：413 项，409 通过，4 跳过，0 失败。覆盖准确目标、显式当前对话路由、兼容入口、展示切换、去重及未知发送不重试。
- `go test ./internal/mcpserver`：通过，49.922 秒。
- 三候选服务在隔离临时数据目录启动通过；生产三服务版本、面板 v41、正式确认 v11、数据库完整性均通过。
- 部署前发现原 GPT 在运行检查之后、服务停止之前准备了一个 `CONTINUE_TASK` 确认单（08:57:58 UTC）。保留这一真实业务更新，以停止后的只读校验值作为安装基线；安装、重启及测试后 32 张业务表保持一致，无活动 Run/claim，无数据库迁移或回滚备份。

证据目录：`build/card-conversation-20261025/`、`build/full-flow-update-20261025-73/`。

## 真实宿主测试与未解决项

按授权从原对话的主输入框发送了准确测试原文，GPT 回复「DevBridge 主对话消息测试通过」，未调用工具。持久会话读取确认它是独立的用户/助手轮次：用户消息 `42361234-1c6f-4f87-939d-8a5066286fd8`，助手消息 `16b1785c-c6f4-4698-9f62-39251fb100bc`。

刷新该页后，用户消息和独立轮次仍在，但这条普通助手回复也显示「无法加载此回复」。这是主输入框的对照实验，不能充当卡片新路由已通过端到端验收的证据。截图：`build/full-flow-update-20261025-73/main-conversation-refresh-failure.png`。

同页工作台首次重载也失败，点击只读重试后恢复了卡片框架，随后读取明确返回 `cloudflare_challenge`。这表明当前宿主还存在页面/资源读取障碍，但不能据此断言所有正文失败都由这一错误引起。经连接器读取 DevBridge 诊断仍为 `.73` ready。

结论：代码侧路由和展示改动已部署；当前宿主的正文刷新恢复仍失败，卡片触发后独立轮次与刷新保留的完整验收尚未通过。DevBridge 无权通过公开 UI API读取或重写 ChatGPT 的回答历史，不能伪造历史恢复。没有点击原任务的审批、延续确认或运行按钮来冒充测试。
