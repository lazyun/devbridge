# GPT 卡片调用边界与点击续接（2026-10-23）

版本 `.55`，组件 `ui://devbridge/card/task-panel-v28`，schema 131。

## 实际断点

原 GPT v27 卡片点击后的文本为“重新授权结果待核对；请刷新读取，不能重复提交”，按钮被禁用。原 Review 的 `review_grants` 仍为 `RECOVERY_REQUIRED` / generation 1，准确 Review 的 `agent_operation_recoveries` 回执为 0 条，未完成授权。

服务的工具可见性中间件只把旧只读组件工具和确认工具开放给 app；`agent_operation_recover`、运行问题提交和恢复、`run_analysis_get` 均只对模型开放。已注册的 app-only `ui_approval_get` / `ui_approval_decide` 还被误当成退休 helper 从发现目录移除。因此组件本地 dispatcher 允许发送不代表真实宿主能调用。此前只验证展示和模拟 RPC，遗漏了实际工具发现边界。

[OpenAI UI reference](https://developers.openai.com/plugins/reference) 明确 `_meta.ui.visibility` 控制 model/app 工具调用范围；UI 使用 `tools/call`，通过 `ui/message` 或 `sendFollowUpMessage` 请求模型继续。此次仅补齐组件既有窄 dispatcher 所需的七个工具，不开放任意文件操作、分析提交或通用准备命令给 UI；Core 的原 Session、目标、授权版本和用户决策校验不变。

## 点击后的流程

- “重新授权并继续 GPT 审查”一次点击完成准确授权版本预检、原恢复接口调用、同 Review/Run 新鲜回读及一次 GPT 续接请求。不再先点一个按钮再点相同确认，也不再要求用户回对话输入“继续”。GPT 后续仍须按原范围调用 review_access_prepare、实际读取文件并正式提交报告。
- 宿主 `-32601/-32602` 明确拒绝时，展示尚未提交的原因并允许显式重试，复用原 requestId 和载荷。超时等未知结果仍禁止自动重发，不把未知当成功。错误提示不再把所有拒绝混成“授权版本已变化”。
- 正式子卡片把准确 commit 回执交给外层面板。只有当前、完整 `COMPLETED` / recorded `CONSUMED`、身份匹配的用户提交，且新鲜回读 Review/Run 或 Direct File workflow/generation 一致时，才请求 GPT 继续同一审查或获批文件实施。创建出的新任务只能按实际成功步骤的精确对象导航。历史、错误、部分、未知、跨目标和定义冲突回执均不唤起 GPT。
- Codex 实施仍由原获批 Core 流程执行；正式审批不会被自动消费。GPT 需要完成分析时仍由当前面板的具体操作点击衔接，不要求重复抄写指令。
- ChatGPT 是否显示组件发出的简短续接消息由宿主管理；本项目没有使用未文档化的隐藏消息字段。业务操作和后续确认始终在现有右侧面板，避免新会话卡片与重复手动确认。

## 验证

前端 522 项：518 通过、4 跳过、0 失败。新增覆盖单击恢复与授权版本漂移、真实宿主拒绝的显式同请求重试、超时不重发、成功 commit 后一次续接、新建任务定义变化、Direct File grant 精确匹配、导航变化、历史及部分/未知回执无续接。子组件实际 commit 响应才通知外层，读取历史不会触发。

MCP Go 全包通过，包括真实 Streamable MCP `tools/list` 的 app 可调用权限检查与两次独立目录一致性。Core `TestRecoveredUpperReviewCanReachApprovalWithoutErasingUncertainty` 通过，覆盖重启、准确恢复、激活文件授权、提交报告到审批的原合同。MCP/controlapi vet 和三程序构建通过，源码内嵌哈希一致。

真实 Chrome 内存隔离场景 `?handoff=1` 仅使用合成对象，不连接 Core、生产任务或数据库：点击一次恢复按钮，模拟恢复响应后回读，再发出一次 GPT 续接；可见计数为 1。截图 `build/full-flow-update-20261023-55/simulated-single-click-handoff.jpg`。此模拟不能替代生产授权验收。

部署通过 Desktop 托管停止/启动完成，备份 .54 程序和停止时完整数据库；生产库只读观察或备份，不直接改业务表。真实宿主与原任务验收结果以部署回执追加，不提前标为完整流程通过。

## 原 GPT 单击实测

用户明确授权载入 v28 并在原任务点击一次“重新授权并继续 GPT 审查”。最初 ChatGPT 工具刷新失败、面板刷新出现 `cloudflare_challenge`，实际脚本仍为 v27；未点击旧按钮。插件设置恢复后刷新工具，并重试同一只读展示回复，实际 iframe 验明 `2.16.0-task-panel-v28` 和续接代码后才点击。没有发送额外继续指令。

2026-10-23 15:58:44（Asia/Shanghai），Core 为同一 Review/Run 记录唯一恢复回执 `panel_62dc15f4-2bbb-4525-beeb-3537b96c05ee`，原授权 generation 1 升为 2。GPT 自动续接，随后 review_access_prepare 激活为 ACTIVE；15:59:46 已记录 generation 2 的真实文件列举成功证据。Task、Review、Run 均沿用原身份，未批准实施。正式报告仍待观察，不能把续接成功等同于整个功能已交付。

16:07:01（Asia/Shanghai），GPT 通过原 `review_result_submit` 正式提交并回读，报告 `report_f1587b4072c1ca5c0eea86bfe67b0975`，载荷 SHA256 `8809883394f2f26c33027029c7c11ecf47f1dd9a16db60ebd63bba26aa70ee0b`。第 2 代授权累计 41 次成功文件读取、16 次成功列举。原 Task 进入 `REVIEW_APPROVAL_PENDING`，currentReviewId 保持原值，currentExecutionId 仍为空。真实右侧 v28 面板显示“等待审批”，审查阶段已完成，提供“批准并开始实施 / 要求修改方案 / 取消任务”；未点击审批。截图 `build/full-flow-update-20261023-55/production-review-complete.jpg`。本次生产验收证明授权→自动 GPT 续接→真实读文件→报告提交→同面板审批入口已经走通，不代表 Skeleton 功能实施、测试、验收或合入完成。
