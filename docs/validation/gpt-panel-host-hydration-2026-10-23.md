# GPT 面板宿主恢复合同修复（2026-10-23）

版本 `.52`，组件 `ui://devbridge/card/task-panel-v25`，schema 131。此前 `.51` 没有完成真实宿主整页刷新验收，不能把“组件未显示”直接断定为不可修复的 OpenAI 故障。

## 官方合同与改动

依据 [OpenAI UI reference](https://developers.openai.com/plugins/reference) 和 [Add UI to your MCP server](https://developers.openai.com/plugins/build/chatgpt-ui)：结构化结果同时提供给模型和组件；ChatGPT 还提供初始 `toolOutput`、保留 canonical MCP envelope 的 `toolResponseMetadata` 及 `openai:set_globals` 更新。模板与工具的关联定义在工具 descriptor 的 `_meta.ui.resourceUri`，`openai/outputTemplate` 是 descriptor 的兼容别名。

- 原组件只等待 `ui/notifications/tool-input/tool-result`，忽略初始 globals 及其中的 canonical envelope。现支持初始和延迟 globals、标准通知并存，读取 `mcp_tool_result` / `call_tool_result` 中的准确结构化输出及隐藏 `_meta`；不会因通知不重放而丢失入口。
- 恢复仍使用同一套工具输入/输出身份校验。globals 的可选 getter 不可用时不阻断标准协议；不同输入、不同输出及 canonical envelope 的目标冲突均拒绝。保存的 UI 状态不能创建入口或提供业务授权。
- 同入口重复恢复不会重置显式导航，不提交确认、不发消息或触发业务恢复。
- 展示 lease 保留同一请求的准确身份与新鲜只读回读，不再在每次 tool result `_meta` 重新写入模板 URI；模板只由已登记的 render tool descriptor 关联，数据工具仍静默。
- v24 及之前 URI 继续提供兼容资源；保留 .51 的同入口展示快照、历史不自动抢占和只读历史控制。

## 验证

前端 505 项：501 通过、4 跳过、0 失败。新增先复现再修复的初始 globals-only、延迟 globals、重复恢复、缺 canonical 身份、输出/输入冲突、可选 getter 失败场景。Go MCP 全量测试及 MCP/controlapi vet 通过，三程序构建及内嵌源码字节一致。

真实浏览器隔离预览新增 `?globals=1`：只注入官方初始数据，不重放任何工具输入/结果通知；整页刷新后仍显示三个页签、审查提交和发起操作入口。此夹具使用合成数据、拒绝写入，不等同于原 GPT 真实验收。

通过 Desktop 托管停止/启动部署，保留停止时数据库和 .51 程序备份，业务表在停止前、停止后及重启后一致。原 GPT 会话的结果以 `build/full-flow-update-20261023-52/deployment.json` 为准，不用初次挂载或重试成功替代整页刷新通过。

## 真实宿主结论（未全部修复）

用户分别授权一次 v25 只读重展与一次纯文字对照。v25 实际脚本已经载入，DOM 确认含 `restoreHostToolResult`；原准确任务、三个页签及重新授权等入口可见。整页刷新仍出现应用加载失败，重试同一结果后可用。

在浏览器已有 DevTools 网络面板内，只读查看当前页请求：v25 组件资源请求和当前准确会话请求均为 HTTP 200；组件响应包含 HTML 和匹配的 panel_show 工具信息。未发现除遥测以外的网络失败请求，没有证据将问题归因为 Core 操作资格、CSP 拦截或资源 404。未改浏览器权限、CSP、安全设置或清理 Cookie。

进一步在原会话发送已获明确授权的纯文字对照：“不要调用任何工具……只回复：显示测试正常”。没有 Dev Bridge 工具调用，回复仍显示“无法加载此回复”。因此剩余显示故障可在没有插件调用时独立复现；不能继续把修改 Core 当作此问题的修复，也不能将原 GPT 整页刷新标记通过。

最终恢复原 v25 右侧面板，保留可用操作，未操作任何业务按钮；业务表、Run 身份均未变化。`chatgpt-text-only-control-failed.jpg` 为对照证据，`chatgpt-v25-final.jpg` 为最终面板。尚未完成的验收是 ChatGPT 自身回复与组件在整页刷新后的稳定加载。
