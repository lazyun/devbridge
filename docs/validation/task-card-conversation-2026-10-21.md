# 任务卡片点击唤起 ChatGPT（2026-10-21）

此轮实现的是通用状态查询。用户随后明确要求审查报告分析，当前行为以[审查完成后点击 GPT 分析](review-result-analysis-click-2026-10-21.md)为准。

## 行为与边界

任务卡片新增“询问 ChatGPT”，在内联展开、收起和紧凑进度视图中提供。仅显式点击后向当前 ChatGPT 对话发送一次只读状态查询，携带准确 Session、Project、FeatureArea、Task 标识，要求重新读取正式状态并说明可选下一步。历史详情携带原只读范围与操作身份。消息不携带报告正文、动态任务标题或运行错误文本。

优先使用宿主声明的 MCP Apps `ui/message`，参数为 `role: user` 与文本 `content` 数组；未声明标准消息能力且存在 OpenAI 兼容桥接时使用 `window.openai.sendFollowUpMessage({prompt, scrollToBottom: true})`。两条路径都受 15 秒超时约束。成功只表示宿主接受消息，不把它当成模型已完成回复。紧凑视图获确认后请求返回内联。

同一展示代际只发送一次；请求中及结束后阻止重复点击。错误或超时不自动重试、不自动切换另一发送路径，提示先检查对话，并提供只读文本框供手动发送。卸载取消等待，迟到结果不能污染新选择的 Task。挂载、刷新、轮询、宿主通知均不发送此消息；通用请求分派器仍拒绝没有显式点击能力标记的 `ui/message`，既有自动唤醒代码不会因此取得发信权限。没有新增业务写工具或消耗审批。

资源升级为 `ui://devbridge/card/read-only-conversation-v4`，保留 PiP v3/v2、read-only-v1 和原始 URI。普通文本对话不依赖组件。

## 本地验证

| 检查 | 结果 |
| --- | --- |
| 新增点击消息行为用例 | 9 PASS，包含标准/兼容桥接、明确点击、精确身份、历史只读、重复点击、拒绝/超时、卸载、迟到回复和返回内联 |
| `node --test internal/mcpserver/dev_bridge*test.js` | 141 PASS，3 SKIP，0 FAIL |
| `go test -p 1 ./internal/mcpserver` | PASS，42.585s，含真实 MCP 资源读取与旧 URI 兼容 |
| `go build -p 1 ./cmd/...` | PASS |
| 修改文件空白检查 | PASS |

初次 Go 测试受沙箱本机端口限制，允许临时测试监听后通过。服务部署版本 `0.1.0-dev.conversation-20261021.6+gd3969cf`，实际 Tunnel diagnostics 返回 `ready=true`。更新前无活动 Run，已保留旧二进制供回退。

## 真实宿主验收

在既有 ChatGPT Work / Cloud 对话为同一任务返回新卡片，确认内联和宿主侧栏中的紧凑进度均出现“询问 ChatGPT”。点击侧栏中的按钮后，宿主接受消息、退出侧栏并回到内联，ChatGPT 进入处理并完成只读状态回复，说明任务仍等待审批、正式验证方案与执行准入不匹配，以及可选下一步。没有通过浏览器脚本代发消息；实际点击调用了卡片桥接。

| 真实行为 | 结果 |
| --- | --- |
| 新卡片显示“询问 ChatGPT” | PASS，内联及侧栏可见 |
| 点击后请求当前对话并返回内联 | PASS |
| ChatGPT 查询状态并完成回复 | PASS |
| 任务保持 `REVIEW_APPROVAL_PENDING`、无当前 Execution | PASS |
| Run 总数与终态、Events 订阅 | PASS，未新建 Run，订阅仍 ACTIVE |
| SQLite quick check | ok |

截图保存在忽略的 `build/conversation-evidence/chatgpt-click-response.png`，含私人对话界面，不纳入公开仓库。真实 PiP 显示能力的限制仍以[上一轮记录](task-progress-pip-2026-10-21.md)为准，侧栏不计为真正 PiP 成功。

最终主脚本 SHA-256：`d849055f4abb5be7394c36c22efd6d0e91dc6e4c626b2696bbacb5e0cf2c633a`。

## 官方依据

[Add UI to your MCP server](https://developers.openai.com/plugins/build/chatgpt-ui) 与 [Plugin UI reference](https://developers.openai.com/plugins/reference) 的共享 `ui/message` 和 OpenAI 兼容消息接口。消息目标为当前对话，不创建新会话或猜测会话 URL。
