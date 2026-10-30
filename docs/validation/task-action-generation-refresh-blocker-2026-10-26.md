# 刷新后仍拒绝 taskActionGeneration 的追查

日期：2026-10-26（Asia/Shanghai）。

用户在 .82 部署及刷新后，原 GPT 对话仍返回 `Additional properties are not allowed ('taskActionGeneration' was unexpected)`。

## 新确认的证据

- 远程 `bridge_diagnostics`：`.82 / ready=true`。
- ChatGPT 插件详情中 devBridge-test 的已登记工具描述不包含 .82 新增的 `taskActionGeneration belongs at the TOP LEVEL`。登记目录确实未更新，不能仅解释为旧对话保留旧上下文。
- 本轮仅一次“刷新工具”：`POST https://chatgpt.com/backend-api/aip/connectors/mcp/refresh_actions` 返回 `500 Internal Server Error`，耗时约 28.09 秒；页面显示“无法更新应用。请重试”。没有取得 63 字节响应的具体正文，不能确定刷新失败的服务端根因。
- 原对话仍使用固定准备请求 `panel_3a5ca1b3-b01e-4688-803f-bbc6e1bbe59c`，GPT 报告未生成新确认。没有主动重发这个请求。

## 更正此前操作建议

官方元数据刷新流程包含：刷新成功、确认登记元数据已变化、在新对话中复测。单独刷新旧对话页面不足。此次连登记目录更新都失败，因此仅新开对话也不能保证解决。不能说用户没有刷新，也不能宣称部署 .82 已解决整个链路。

来源：[OpenAI 官方连接与测试文档](https://developers.openai.com/plugins/deploy/connect-chatgpt#refresh-metadata)。

## 操作边界

一次缺少必填参数的生产 `execution_prepare` 诊断尝试被自动审批审查拒绝：该工具可能创建确认，诊断请求未授权新准备且参数不完整。该尝试没有执行；随后只读核对插件目录、官方文档及刷新失败，不以本地直调或其他工具绕过拒绝。

没有发送 GPT 消息、点击原任务重试或确认按钮、修改任务历史、审批或启动运行，没有卸载、删除或新增插件连接。临时插件诊断标签页已关闭，保留原对话。向用户请求了保留旧连接、增加同隧道临时连接并只读验证的明确授权；尚未得到答复。

截图：`build/gpt-schema-refresh-blocker-20261026/plugin-refresh-failed.png`。
