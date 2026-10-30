# 验证阶段的三类任务处理（2026-10-22）

验证检查点提供“延续任务”“完成并合入”“暂存任务”。原 Task、Review、Execution、Run 和 GPT 分析均保留。Core 记录确认回执，读取、刷新和组件挂载不写入业务决定。

- `CONTINUE_TASK`：确认同任务续接并解除暂存，不启动 Codex。验证恢复阶段可在明确确认后申请同任务补审，但旧结果保持原状，运行中的写权限、领域 claim、文件权限和 Git 交付守卫继续生效。
- `COMPLETE_TASK`：Core 准备准确版本、已验证快照、工作集及逐仓开发目标。一次用户确认覆盖验收、提交、合入。提交完成时仍为 ACTIVE / DELIVERING，合入全部成功后才 SUCCEEDED。多仓库使用逐仓回执，不把单仓库 Git 字段作为完成依据。漂移、冲突和响应丢失保留原计划与回执，不能换目标或提前完成。
- `DEFER_TASK`：以独立业务检查点保存进度、验证结果和剩余待办，阻止新 Execution、Run 及阶段推进。不执行 Git stash。确认延续前不可继续交付。

WebUI 使用本地 operator 身份；GPT 的进度组件只在用户点击后发送准备正式确认的请求，不能直接提交确认。文件实施的完成合入需由原 AI Session 的正式确认完成。确认组件显示冻结的仓库、目标分支、目标提交和验证快照，以及原确认关联的实时处理状态。旧 `ACCEPT` 保留兼容性，其验收后提交不替代新完成合入语义。

数据库升级至 schema 131，增加任务完成意图、暂存检查点和不可变选择回执，并收紧完成状态与合入回执的绑定。确认资源为 `ui://devbridge/confirmation/v10`，只读进度资源为 `ui://devbridge/card/verification-decisions-v15`；旧资源 URI 仍可读取。宿主已缓存的旧卡片需重新展示才能取得新脚本。

验证记录：

- 真实 Git 单仓库：合入前不完成、合入响应丢失后的准确重放、原验收请求不能丢弃完成计划、来源和开发目标漂移拒绝。
- 真实多仓库 Coding 流程：暂存 → 延续 → 完成合入；三个确认命令在业务回执落库后模拟确认响应丢失，均能只读找回原结果。
- 本地 operator 三类操作保留独立 actor，完成后真实合入；文件实施的 Codex 验证及合入通过。
- 暂存阻止阶段推进，解除暂存与原暂存回执重放不会再次暂停；验证恢复需明确续接确认，暂存后补审受阻。
- Node 全部前端回归：436 项，432 通过，4 个既有可选跳过，无失败。新增准确 Task/Execution/revision 匹配、组件禁止自动提交、旧恢复状态与暂存/合入结果显示优先级测试。
- Console API、Bridge HTTP、workflow presentation、confirmation 及 MCP Go 回归通过。MCP 回归显式跳过两项既有文档契约失败 `TestExternalWorkflowDiscoveryContract` 和 `TestCurrentDocumentationFreezesFeatureAreaReviewVocabulary`，不将其记为通过。
- 六个相关包 `go vet`、三个生产程序构建和 `git diff --check` 通过。实际 MCP wire 重新捕获三组确认组件夹具，保持 Go 与浏览器复用同一份生产 envelope。

部署与页面验收记录见 `build/full-flow-update-20261022-35/`。更新只交付 DevBridge 的功能，不代表已对原 Skeleton 任务作出延续、完成或暂存决定。

实际部署：`0.1.0-dev.full-flow-20261022.35+gd3969cf`，Core/MCP/Local MCP 全部 ready。原任务与所属 Run、GPT 分析、领域 claim、确认意图的前后摘要完全相同，活动 Run 为 0。真实 WebUI 和 MCP 精简 workspace 均显示三项操作，其中延续和暂存可用，完成因验证未通过禁用；没有对原任务提交任何选择，也未向原 ChatGPT 会话发送消息。页面截图：`build/verification-task-decisions-live.png`。
