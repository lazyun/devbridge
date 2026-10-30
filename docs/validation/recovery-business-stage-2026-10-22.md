# 恢复状态保留业务阶段（2026-10-22）

原实现将 `RECOVERY_REQUIRED` 直接显示为任务阶段，并把恢复进度默认放到实施节点，导致验证中断后看起来退回实施。

修复将业务阶段与恢复状态分开：

- 从该 Task 最新的正式阶段迁移事件读取进入恢复前的阶段；不按历史 Run 数量、最新 Run 或成功状态推测。缺少、无效或已被后续迁移替代的来源保留未知，不默认实施。
- 原 canonical `phase=RECOVERY_REQUIRED`、恢复目标、审批与启动准入保持原样。只读来源元数据排除在 Task JSON 和授权载荷之外。
- 共享 TaskView 提供 `businessPhase` 与来源事件标识。验证恢复时实施节点保持完成，验证节点显示受阻；任务列表、详情、领域视图和 GPT 卡片保留验证阶段，单独提示等待恢复。
- 阶段筛选同时支持业务阶段与原恢复状态；未知来源不混入验证筛选。

## 验证

- 真实 SQLite 失败收口产生验证到恢复的事件，重读 Task 与共享展示保留验证；读取不写事件，原恢复准入仍拒绝新执行；后续不兼容事件不能复用旧来源。
- 共享 Go 展示测试覆盖审查、实施、验证、交付恢复，以及缺失/无效/过期来源、无关历史 Run 和后续阶段。
- Task JSON 在增添内部恢复来源前后逐字一致，Clone 不共享来源指针。
- Web/MCP JavaScript 412 项通过、4 项跳过、0 失败；验证阶段标签、恢复提示、受阻进度与筛选均覆盖。
- 关联 Bridge HTTP、MCP、Core、SQLite 回归通过；Task、共享展示、Console 全套通过；相关 go vet 和差异空白检查通过。

日志：`build/recovery-stage-contract.log`、`build/recovery-stage-regression.log`、`build/recovery-stage-final-go.log`、`build/recovery-stage-node.log`、`build/recovery-stage-vet.log`。

## 本机部署与原任务回读

已部署 `0.1.0-dev.full-flow-20261022.27+gd3969cf`，三个服务就绪检查均返回 200。更新候选及回滚备份位于 `build/full-flow-update-20261022-27/`。

原任务“重构 Progressive Discovery 为节点驱动流程”的真实 Web API 与 MCP 回读一致：业务阶段为 `VERIFYING`，canonical 阶段仍为 `RECOVERY_REQUIRED`，运行提示为“已中断，等待恢复。”；进度条验证节点为 `BLOCKED`，准确恢复动作仍指向原验证 Run。未提交恢复或启动重试。

部署前后 Task、10 条 Run、GPT 分析、原审批意图及三条确认提交的数据摘要一致；活动 Run 为 0，数据库版本仍为 130，业务工具目录摘要未变。页面自动化回归通过；Chrome 控制连接超时，未将浏览器目视检查记为通过。

## 原 ChatGPT 会话补验

用户反馈同一 ChatGPT 会话的进度组件仍显示“需要恢复”。通过原会话窗口复现：组件读取时间持续更新，验证节点已为 `BLOCKED`，但标题仍使用旧的恢复标签。重新加载原 ChatGPT 页面（不新建会话、不发送消息、不启动任务）后，原组件标题变为“验证阶段”，验证节点保持受阻，用户选择的侧边展示保留。

组件内“刷新”更新业务快照，不重新加载已挂载的 HTML/JavaScript；服务升级本身不能替换已运行的组件代码。[OpenAI 官方 UI 文档](https://developers.openai.com/plugins/build/chatgpt-ui)要求将资源 URI 作为缓存键，更新展示资源时同步版本化所有工具引用。`.27` 已使用新 URI 并保留旧 URI 的兼容读取；本次实际完成原会话页面重载，补齐此前只验证 WebUI/API 的遗漏。截图：`build/chatgpt-recovery-stage-live.png`。
