# 浏览器重启后的原 Session 只读核验

日期：2026-10-26（Asia/Shanghai）。

用户在重启 Chrome 后看到「已绑定 Session 核验失败，请重试只读核验」。本次保留原对话、Session、Project、Task，只进行诊断和只读重试。

## 已确认的事实

- 生产 MCP `.81` 的 `bridge_diagnostics` 返回 `ready=true`。
- 原对话工作台仍加载 `task-panel-v44`。入口诊断中的 Session 尾号为 `b092`、Project 尾号为 `9da0`。
- 通过同一完整 Session ID 调用 `session_initialize({aiSessionId})` 的纯读取分支，返回 `PROJECT_BOUND`，Project 为原 skeleton，绑定未丢失。
- SQLite 只读查询确认原任务「重构 Progressive Discovery 为节点驱动流程」仍为 `ACTIVE / ACCEPTANCE_PENDING`。
- 原工作台的只读核验调用收到 JSON-RPC `-32000`。Chrome Network 中对应的 `POST https://chatgpt.com/backend-api/ecosystem/call_mcp` 返回 `403 Forbidden`，响应为 HTML；`widget_state` 同样返回 403。
- 工作台随后显示明确错误「工作区上下文不可用 · cloudflare_challenge」。ChatGPT 的会话读取、用户设置等请求也出现 403。
- 正常刷新原页面后，多条已保存回复显示「无法加载此回复」，插件显示「未能显示 devBridge-test」。刷新没有恢复宿主访问。

## 结论与边界

此次提示不是已证实的 Session 解绑：服务端已经成功读取同一原 Session 的持久绑定。当前浏览器内的核验请求被 ChatGPT 访问验证拦截，无法完成工作台恢复。不能据此创建替代 Session、清除原任务状态或声称插件已恢复。未确定访问验证为何触发，也不能将 Statsig 遥测的 `ERR_BLOCKED_BY_CLIENT` 当成原因。

未发送新的 GPT 消息，未点击分析按钮，未准备或消费确认，未批准、启动、恢复工程运行，未更改浏览器安全设置、代理、Cookie 或插件权限。关闭临时 DevTools，保留用户原标签页。没有进行源代码修改或重新部署。

当前页面截图：`build/gpt-session-restore-20261026/chatgpt-host-blocked.png`。
