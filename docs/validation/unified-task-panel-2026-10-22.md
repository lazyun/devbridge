# 统一任务进度与操作列表（2026-10-22）

悬浮窗口固定展示当前 Task 的名称、阶段、总进度和最后读取时间，下方为同一任务的可滚动操作列表。未处理项优先，历史回执默认折叠；已过期和已替换的版本由原资格检查拒绝提交。确认命令完成与业务任务完成继续分别展示。

Core 增加同 Session、同 Project、准确 Task 的只读确认引用列表。包括尚未提交的提案，排除其他任务及只有领域相同的未创建任务提案。最多 20 项并保留截断标志；读取失败明确标记不可核对。该列表独立于原项目未闭合创建回执列表，不会阻塞项目导航或产生审批、执行与订阅。

列表中的每个正式确认项复用未改写的 `confirmation_ui.html` 主脚本。资源构建时静态组合，无 `eval`、动态函数和嵌套 iframe。各项以完整的 Session/intent/version/contentHash 为身份，拥有独立 Shadow DOM、选择、未知提交标记及 RPC 通道。只允许原 confirmation_get/options_get/commit 三个接口，而且参数必须匹配该项冻结身份。进度组件的通用 dispatcher 仍拒绝写操作。挂载和轮询仅只读；提交仍由正式表单的用户点击触发，沿用 Core 全部准入与回执规则。

后台刷新在用户编辑正式表单或阅读内容时延后绘制，表单 DOM、选择和操作列表滚动位置复用。确认后的结果回读更新原列表项。任务切换或上下文失效销毁旧子实例；新确认不会重绑旧实例。宿主发来的其他正式确认 tool-input/result 不会替换总进度入口。

资源升级为 `ui://devbridge/card/task-panel-v16`，保留 v15 及既有 URI 的读取兼容。已有悬浮组件需重新展示以加载新脚本；在已打开统一面板的流程中，GPT 准备后续 intent 后不再另发 standalone confirmation_show。无统一面板的客户端继续使用独立正式卡片。

官方依据：[Plugin UI reference](https://developers.openai.com/plugins/reference)、[Add UI to your MCP server](https://developers.openai.com/plugins/build/chatgpt-ui)。采用同一个组件内部的布局和正式子表单，不依赖搬动 ChatGPT 已有消息卡片。

验证结果：

- Node 前端回归 444 项，440 通过，4 项既有可选跳过，无失败。新增操作列表身份拒绝、旧记录折叠、确认通道隔离、旧延续确认完成后可准备下一步等覆盖。
- MCP、Console API、workflow presentation 回归通过；MCP 显式跳过此前已知的两项文档契约检查 `TestExternalWorkflowDiscoveryContract` 与 `TestCurrentDocumentationFreezesFeatureAreaReviewVocabulary`，未将它们计为通过。
- 真实 MCP HTTP/SQLite 集成验证未提交确认可读取、跨 Session/Project/Task 拒绝、读取不改变数据库。原独立卡片实例隔离、确认响应丢失回读、验证阶段三类操作回归通过。
- 四个相关包 go vet、三程序构建与 git diff --check 通过。正式确认主脚本仍与独立确认资源的源代码完全一致。
- 浏览器隔离预览验证固定总进度、滚动操作列表及原正式按钮。预览禁止全部写入；点击确认后走原请求回读并显示结果未知，没有假报成功。截图 `build/unified-task-panel-preview.png` 是隔离夹具预览，不表示原 ChatGPT 宿主已经加载新组件。

部署 `0.1.0-dev.full-flow-20261022.36+gd3969cf`，schema 131 不变，Core/MCP/Local MCP 全部 ready。原任务与所属 Run、分析、确认、claim、暂存与完成意图、订阅的更新前后摘要一致，活动 Run 为 0。真实 MCP 回读操作列表 AVAILABLE，延续确认 COMPLETED，旧启动确认因阶段已变显示 STALE；没有替用户新增、提交、继续或完成任何业务操作。未向原 ChatGPT 会话发送消息。

部署回执和只读回查：`build/full-flow-update-20261022-36/`，最近的 `.35` 程序与数据库备份位于该目录 `before-update/`。原 ChatGPT 中缓存的旧组件需刷新插件工具并重新展示一次新版面板；后续同任务确认进入面板列表。正式确认脚本或容器脚本发生变化时，都应检查并升级容器资源 URI，避免嵌入旧版本。
