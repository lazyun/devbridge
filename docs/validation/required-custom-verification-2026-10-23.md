# 必需 CUSTOM 验证与 GPT 分析衔接（2026-10-23）

目标版本 `.56`，schema 131，现有 v28 面板保持不变。

## 实际问题

原任务 `task_429bc7f6d36b3c3cab1eccf3027a2ad0` 的 GPT 审查报告已经入库，但 `execution_prepare(APPROVE_AND_START)` 被旧规则 `UNOBSERVABLE_REQUIRED_CHECK: CUSTOM must be optional` 拒绝。报告中的两项必需 CUSTOM 涵盖节点/CAS/迁移/故障恢复实测，以及真实 Codex 跨仓只读、拒写与线程恢复。它们的必需性及原指令均应保留，不能改为可选或捏造新命令。

## 合同

- CUSTOM 指令是必须逐项提交 GPT 分析的义务，Codex 按原批准指令实际验证；分析必须记录实际结果、证据或 BLOCKED/NOT_RUN/UNKNOWN。报告覆盖、索引与准确源身份仍由 Core 校验。
- CUSTOM 指令没有 Bridge `Observed` 事实及退出码，只有独立 `Reported` 评估；原始 Codex 自然输出不被解析为通过。读取、写入权限和批准范围不变。
- CUSTOM 若明确携带 command，则仍需要准确命令、仓库、工作目录、完成 item 和退出码。其他必需命令/Git 检查保持原证据门禁；缺失、错仓或未完成的执行证据不能由 GPT 声明替代。
- 必需 CUSTOM 受阻或未执行不允许“完成任务并合入”；必须逐项评估通过且其他命令证据/工作区连续性满足，才提供正式用户确认。单纯报告完整不会自动接受 Task。
- 自动恢复的权威证据规则保持严格：GPT CUSTOM 通过声明不能作为自动恢复成功的 Bridge 证据。
- 没有修改生产报告、任务定义或验证规格；没有降低 required、增加执行命令或豁免。准备只生成待确认 intent，不启动 Run。

## 验证

新增/更新测试覆盖 SDK 实际 execution_prepare、必需 CUSTOM 原文和 required 保持、缺失明确命令仍拒绝、逐项分析缺失拒绝、通过/受阻/未执行/未知评估、零伪造 observed、缺失/错仓命令证据保留阻断、CUSTOM 命令的真实执行，以及恢复不能借用分析声明。

Facade 隔离双仓流程使用临时数据库/仓库和 fake App Server：真实自然输出协议 → 独立 GPT 分析提交 → 用户正式确认 → 实施/验证 → 必需 CUSTOM 分析 → 延续/暂存 → 完成并合入；受阻 CUSTOM 无法准备 COMPLETE_TASK。该测试证明协议与状态推进，不声称真实模型已通过 Skeleton 安全验收。

部署和原任务正式准备回执记录于 `build/full-flow-update-20261023-56/`。实际结果待追加，不将未运行的生产实施/验证标为成功。

## 实际结果

Task/Run/workflowpresentation 全包通过；execution 80.587 秒、SQLite 303.392 秒、MCP 56.424 秒整包通过；Core 定向确认/分析/合入回归及相关包 vet 通过。三程序源码与产物 SHA256 一致，Desktop 托管停止/启动部署 .56，Core/MCP/Local MCP/隧道均 READY。停止后备份完整生产库且 integrity_check=ok；重启前后 22 张业务/授权/回执表内容完全一致，schema 仍为 131。

重新准备被自动审批审查拦截后，没有绕路调用。向用户说明需要明确授权；用户回复“允许，仅准备原方案确认单”后，重试同一请求。Core 返回 PREPARED，intent `confirm_3961936892650b1e33020a31ce7ef03e` / version 1 / contentHash `85e7cd30b8604bc56e2a7cbc644ec6a9455d54a8884d8aba0a45908fbdd8d1b1`，状态 PENDING。回读的 workSpec/verificationSpec 与原报告逐字段相同，8 项 required=true，含原两项必需 CUSTOM；原 Task/Review/定义版本与报告 ID 均未改变。未提交确认，原 Task 保持 REVIEW_APPROVAL_PENDING、currentExecutionId 为空且无活动 Run。

真实原 GPT v28 面板刷新后显示“后续操作 1”，正式“批准并开始实施 · 待确认”卡片在同一右侧面板内展开，等待用户选择模型与思考等级并正式批准。没有给原对话发送新消息，也没有创建补审或新 Task。截图见 `build/full-flow-update-20261023-56/production-approval-prepared.jpg`。

同步 ChatGPT 工具说明后，宿主出现短暂 `cloudflare_challenge` 和选项读取错误。未重复准备/提交；重载同一原对话、重试同一已有展示结果后，v28 原面板恢复“后续操作 1”及后端/模型选项。截图更新为恢复后的准确入口。没有声称这能解决 ChatGPT 自身全部历史回复渲染问题。

16:45（Asia/Shanghai）最终只读核对：原右侧面板恢复后连续读取保持“后续操作 1”，可见 Default Codex configuration、可用 gpt-6.1-sol 选项和待用户选择的模型/思考等级；没有代选模型或点击批准。最终截图 `production-approval-prepared.jpg` 为真实可见运行选项，暂时错误截图另保留为 `host-read-failure.jpg`。
