# 创建任务时的原任务续接提示（2026-10-22）

创建卡片新增只读 `options.taskContinuity`，仅读取本次已授权 Project 与准确 FeatureArea 内未完成、非历史只读的开发任务。排除其他项目、领域、巡检及已结束任务，最多显示最近 20 个并保留总数。任务阶段和下一步沿用任务详情的结果感知投影；读取失败明确显示不可核对，不伪装成没有原任务。

卡片要求选择“继续原任务”“创建独立新任务”或“取消本次选择”。续接后标题显示“续接任务”及原任务名称，隐藏待创建任务的方案阅读入口，保留原提案尚未提交的说明。继续按钮重新读取准确清单，再通过用户点击发送只读对话请求；提示 GPT 核对状态、结果和续接路径，不授权创建、关闭、分析提交、审批、恢复、重试或执行。消息结果不明时展示准确指令供手动复制，不自动重发。

独立新任务明确提示原任务未完成且不会自动关闭。提交前重新读取清单，原任务身份、状态、阶段或定义版本变化时要求重新选择。取消选择只改变卡片本地显示。所有选择均不进入冻结内容、内容摘要或确认 payload，原 Task/Review/Execution/Run/analysis 的准入规则保持原样。

资源升级为 `ui://devbridge/confirmation/v9`，兼容 v8、v7、v6、v5 的读取。新展示卡片取得新资源；已经在宿主缓存的旧脚本需要重新展示确认组件。

验证：

- 确认卡片 Node 回归 97 项全部通过，包括续接准确目标、禁止消费创建、取消选择、提交前漂移、跨领域拒绝、未知清单、空清单以及消息不可用回退。
- Go 的 creation continuity、真实 SQLite confirmation/options 只读集成和 Task review 回归通过。
- `go test ./internal/mcpserver -run TestConfirmation -count=1 -timeout=180s` 通过（需要本机临时回环端口权限）。
- 三个相关包 `go vet`、`git diff --check` 通过；三个生产程序构建成功。
- 真实原创建提案返回恰好一个原任务“重构 Progressive Discovery 为节点驱动流程”，阶段为“待找回验证结果”。使用这份真实读取结果，在禁止确认和消息提交的本机预览宿主中加载原样生产卡片，实际选择续接后标题、原任务名称和只读按钮均正确。

部署 `0.1.0-dev.full-flow-20261022.34+gd3969cf`，schema 130 不变，Core/MCP/Local MCP 全部 ready。更新前活动 Run 为 0；更新后及卡片显示检查后，原任务、全部所属 Run、分析、claim 和确认意图逐项摘要均相同。原任务继续 ACTIVE / RECOVERY_REQUIRED，待创建提案仍 PENDING。没有创建、关闭、恢复或执行任务，也没有向原 ChatGPT 对话发送消息。

部署回执、前后摘要及真实卡片读取：`build/full-flow-update-20261022-34/`。最近一次更新前回滚备份仅保留该目录的 `before-update/`。显示验证截图：`build/task-continuity-card-preview.png`（只读预览，不代表原 ChatGPT 宿主已重新加载）。
