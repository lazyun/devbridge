# 网页延续原任务修复（2026-10-22）

原任务已经确认延续，但网页仍无法推进。实际页面将 `REQUEST_TASK_REVIEW` 禁用为 `OPERATOR_ACTION_IDENTITY_UNAVAILABLE`，而该操作已有独立、持久化的本地操作者合同。另一个独立缺陷是 Console API 的目标结构校验未包含 `CONTINUE_TASK`、`DEFER_TASK`、`COMPLETE_TASK`，导致网页展示了按钮但提交时被拒绝。

修复内容：

- 将补审请求加入已有操作者合同的允许列表，保留准确目标、项目归属、写入资格和不可变回执校验。
- 三类验证阶段操作接受准确的 Task、FeatureArea、Execution、revision 与 `TASK_ACCEPTANCE:<execution>` 目标，拒绝错配或多余字段。
- 已确认延续后展示「延续任务下一步」，将当前真正可用的后续操作直接展示，避免重复提交延续或要求切回 AI 对话。缺失操作者事实、资格失效或暂存时不能展示可提交的后续表单。
- 真实多仓库联调发现补审资格仍依赖 workspace 的单仓库字段。多仓库工作区改为核对 Run 的不可变主成员绑定及 workspace member 身份；已有单仓库检查、运行终态、运行权威和 finalization 门禁保留。

验证结果：

- 网页 Node 回归 148 项全部通过，包含已确认延续后的表单、操作者身份缺失、资格失效、暂存及重复确认处理。
- 真实 Console API / SQLite 集成：读取准确操作 → 确认延续 → 提交同 Task 补审 → 同请求回放 → 回读进入补审配置。拒绝过时 Execution 目标；回放不增加请求或 Run；记录的 actor 为 LOCAL_OPERATOR。
- 原验证阶段三类正式确认、操作者完成并合入和 Direct File/Codex 验证集成回归通过。补审、暂存及恢复延续的 SQLite 定向回归通过。
- Console API 与 bridgehttp 回归通过；最终目标结构和 Task action 定向回归通过。四个相关包 go vet、三程序构建及改动文件 diff 检查通过。

已部署 `0.1.0-dev.full-flow-20261022.37+gd3969cf`，schema 131。Core、MCP、Local MCP 与隧道均 ready。原任务的真实网页接口已返回补审 domain/channel 均可用，延续确认仍为 true。浏览器实测展示可编辑的补审目的和启用的请求按钮，截图为 `build/web-task-continuation-fixed.png`。

此次仅更新 DevBridge 产品。原任务、Run、分析、确认、claim、暂存、完成意图、补审请求和订阅的部署前后摘要一致，活动 Run 为 0。原旧验证结果仍未找回，未伪造结果或实施批准；实际业务补审由用户填写目的后提交。未发送 ChatGPT 消息或创建新任务。

部署记录位于 `build/full-flow-update-20261022-37/`。最近的更新前程序及停止后的完整数据库保留在该目录 `before-update/`。

后续文案调整部署于 `0.1.0-dev.full-flow-20261022.38+gd3969cf`：网页验证进度的 BLOCKED 展示为「未完成」（英文 Incomplete）。详细运行原因保留，业务状态与操作资格不变；其他阶段的阻塞文案不变。网页回归 148 项通过，中英文显示核对通过。当前回滚备份以 `build/README.md` 所列最新目录为准。
