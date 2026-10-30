# ChatGPT 单一工作面板（2026-10-23）

同一 Dev Bridge Session 使用一个持久右侧面板，涵盖项目绑定、领域和任务创建、审查、实施、验证、完成与历史。模型可见的展示入口改为 `panel_show`；`workspace_show`、`confirmation_show`、`details_show` 保留为应用内部能力，后续准备操作不再生成会话内业务卡片。旧消息中的历史组件不会被删除。

面板新增仅应用可调用的 `dev_bridge_panel_get`，读取当前调用者已有 Session 的确认引用，包括项目尚未绑定、任务尚未创建时的提案。读取不会创建 Session、绑定项目、选择任务或提交确认。各确认仍绑定原 Session、intentId、version、contentHash、purpose，独立校验审批资格；跨任务提案保留自身标题。读取失败时操作失效，后续成功读取可恢复列表。

右侧面板含「项目与任务」导航、「查看任务详情」、后续操作和历史操作。详情与确认在同一面板内部展开或切换；不提供返回会话卡片的业务控件。初始化请求宿主 PiP，一次请求不等于宿主已实际显示；普通对话宿主不支持 PiP 时保留单面板内联兼容入口。相关宿主合同见 [OpenAI 插件参考](https://developers.openai.com/plugins/reference) 和 [ChatGPT UI 文档](https://developers.openai.com/plugins/build/chatgpt-ui)。

资源升级为 `ui://devbridge/card/task-panel-v19`，保留 v18 及以前资源别名。正式确认使用原生产渲染器及受限代理，未改变批准与执行规则。

验证覆盖真实 MCP 工具目录、未绑定 Session 的生产 MCP/SQLite 读取、调用者隔离、无写入、PiP 仅请求一次、无额外 show/prepare/commit 调用、确认栈和历史标签回归。Node 共 309 项，305 通过、4 项既有可选跳过、0 失败；最终展示补丁的 10 项聚焦回归通过。相关 Go 测试、生产组件导出、go vet 与三个程序构建通过。三个正式确认协议夹具经生产协议重新捕获，未手改哈希。

部署版本 `0.1.0-dev.full-flow-20261023.43+gd3969cf`，schema 131；Desktop 托管重启后 Core、MCP、Local MCP 与隧道均就绪。最近一次更新前的程序与停止状态数据库保留于 `build/full-flow-update-20261023-43/before-update/`。

用户明确授权后，在原 ChatGPT 会话发送一次只读 `panel_show` 请求，presentationRequestId 为 `single-panel-20261023-v19`，沿用原 Session 与 `task_429bc7f6d36b3c3cab1eccf3027a2ad0`。刷新现有插件工具描述后，模型实际调用新入口。宿主首次加载失败，重试同一组件后遇到一次 `cloudflare_challenge` 上下文读取失败；只读重试后成功载入右侧面板。一次后续轮询读取失败后，列表随下一次成功读取自动恢复。本次未处理或绕过验证挑战。

原宿主实际展示「后续操作 2」「历史操作 6」，其中开始审查仍待确认，项目绑定和创建领域与任务等旧回执进入历史。点击任务详情后仍在原右侧面板内，返回当前 Task、切换历史、返回后续操作均成功；未打开其他会话业务卡片。新消息仅留下 ChatGPT 自身的工具调用条目；原有历史卡片和宿主「无法加载此回复」文字仍由 ChatGPT 保留。本次不将宿主文字加载问题等同于任务失败。

验收截图保存在 `build/full-flow-update-20261023-43/`：`original-gpt-single-panel-v19.png`、`original-gpt-panel-details-v19.png`、`original-gpt-panel-history-v19.png`。更新前后及原会话验收后，12 张业务表的完整行摘要均一致，活动 Run 为 0；未准备、消费或取消确认，未创建任务、启动执行或变更订阅。原审查确认 `confirm_0a0dc920998f6b3ff9034926db35885d` 仍在面板显示待确认。
