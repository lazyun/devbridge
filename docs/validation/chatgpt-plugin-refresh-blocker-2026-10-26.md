# ChatGPT 插件更新阻塞诊断

日期：2026-10-26（Asia/Shanghai）。

## 已确认

- 在 ChatGPT 的 devBridge-test 设置页执行“刷新工具”时，Chrome 诊断界面明确记录 `POST /backend-api/aip/connectors/mcp/refresh_actions` 返回 `500 Internal Server Error`。
- 随后重新加载插件设置页，并重试原设置读取，页面显示“无法加载插件设置”。这是加载新版组件之前的宿主插件管理路径错误。
- DevBridge 远程 `bridge_diagnostics` 返回 `0.1.0-dev.full-flow-20261026.81+gc4ae1d3`、`ready=true`。服务端已部署 v45；前一轮实际原对话 DOM 仍为 `task-panel-v44`。
- 本轮没有发送新的 GPT 消息，没有点击分析、审批、延续、恢复或启动按钮，没有重建 Session、卸载或删除插件，也没有修改生产程序和配置。

## 不能据此确定的事项

没有取得刷新请求的具体错误正文，因此不能把 HTTP 500 进一步归因为 OpenAI 全局故障、DevBridge schema 不兼容、Cloudflare 或浏览器遥测拦截。此前私有工具模板声明的冲突已在 .81 修复，但宿主刷新仍失败，不能将该冲突认定为当前 500 的唯一原因。

官方将组件 URI 定义为缓存键，并要求 UI 变更后更新对应工具的 URI：
[OpenAI UI 构建文档](https://developers.openai.com/plugins/build/chatgpt-ui)。v45 的新 URI 已部署；宿主更新目录失败尚未解决。没有使用未公开接口或修改 ChatGPT 页面内部状态来强行替换组件。

## 当前结论

原对话的完整修复仍未完成。下一步需要取得并排查宿主 `refresh_actions` 的具体失败原因，恢复插件目录更新，再实际挂载 v45 并执行此前获准的那一次只读按钮测试。不能把服务端部署成功等同于旧对话已更新。

证据：`build/gpt-plugin-refresh-repair-20261026/diagnostic.json`、`chatgpt-plugin-settings-failed.png`。未保留无关网络数据或诊断导出临时文件。
