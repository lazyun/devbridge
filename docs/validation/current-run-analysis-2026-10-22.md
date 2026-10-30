# 当前任务的 GPT 分析入口（2026-10-22）

原 ChatGPT 进度组件的默认分析入口始终指向 Review。任务进入实施/验证后，即使实施输出尚未分析，也会继续请求分析历史审查报告。

本次只调整展示和点击意图，不改变任务业务阶段、准入、冻结方案或自动续跑逻辑：

- 当前任务头部从准确 `currentExecution` 绑定的终态 Run 选择分析入口：先选择有分析合同的验证 Run，再选择实施 Run。`NOT_REQUIRED` 的中断验证不遮蔽待分析的实施输出；不根据历史 Run 的时间或数量猜目标。
- 精确核对任务、当前 Execution、定义修订、Run 引用、结束状态与分析状态。旧处理器在点击时再次核对；替换目标或投影后不能发送旧意图。
- 用户点击后才向当前 ChatGPT 对话请求读取准确 `run_analysis_get`，分析实际输出、命令和 Git 差异；按接口返回 schema/sourceHash 提交分析到 Core。已记录则读取已有分析。提交分析不批准、验收、恢复、重试或运行命令。
- 明确提示 Codex 运行成功不等于目标完成；没有文件改动时不能把门禁审查当成实施完成。
- 历史只读详情和具体 Review 的分析按钮保留原目标。普通工作区的任务头部可随业务推进显示实施入口；原 Review 的独立操作仍分析原审查。
- PiP/完整视图保留用户选择，不因请求分析返回对话；恢复时同时展示业务阶段、等待恢复和中断提示。
- 展示资源使用 `ui://devbridge/card/current-run-analysis-v12`，保留所有旧资源 URI 兼容读取。

## 原任务证据

任务“重构 Progressive Discovery 为节点驱动流程”的实施 Run 为 `run_impl_execution_approved_e9438ed7b1d342eb88baa7682fc82093`，结束状态 `SUCCEEDED`，分析状态 `OPTIONAL`。实际输出说明本轮只完成门禁审查，未修改产品代码；Git `ChangedFiles=[]`、差异为空、首尾快照相同。验证 Run 因 `bridge_shutdown_interrupted` 结束，分析状态 `NOT_REQUIRED`。默认入口因此应为“让 GPT 分析实施结果”。

## 验证

- MCP UI JavaScript：278 项，274 通过、4 跳过、0 失败。
- 新场景覆盖普通卡片、PiP、完整视图、实施待分析与验证中断、验证报告优先、逐 Run 去重、身份错误、运行未结束、过期处理器、历史只读 Review，以及从原 Review 入口推进的当前 Task。
- MCP Go 全套存在两个与本次未改文件相关的合同失败：`TestExternalWorkflowDiscoveryContract`（既有 Server instructions 与冻结副本不同）和 `TestCurrentDocumentationFreezesFeatureAreaReviewVocabulary`（既有 workflow 文档缺少 `immutable initial definition`）。其余 MCP Go 回归（跳过这两个测试）通过，用时约 40 秒；不将完整套件标为通过。

日志：`build/current-run-analysis-node-all.log`、`build/current-run-analysis-go.log`、`build/current-run-analysis-go-related.log`。

## 本机更新

已更新到 `0.1.0-dev.full-flow-20261022.28+gd3969cf`，MCP 真实诊断 ready=true，业务工具目录摘要未变。数据库 schema=130，Task、10 条 Run、原 GPT 分析、原审批意图和确认回执更新前后摘要逐项一致；活动 Run=0。未提交新的分析、审批、恢复或重试。部署与回滚备份：`build/full-flow-update-20261022-28/`。

原 ChatGPT 会话验收：刷新已连接应用的工具元数据后，历史卡片仍可能沿用缓存模板，页面重载未能证明新入口已显示。已准备在原会话只读重新展示组件的请求，发送前等待用户明确授权；不把这一项记录为验收通过。相关 go vet 通过。

用户明确授权后，已于北京时间 17:20 在原 ChatGPT 会话发送一次只读重展示请求：复用原 Session/Project/FeatureArea/Task，固定 presentationRequestId=`remount-current-run-analysis-20261022-v12`，禁止审批、分析提交、恢复、重试、运行与改文件。只读组件的回读验收单独记录。

原会话真实验收通过：ChatGPT 返回新 workspace_show 后，点击该次结果的“在标签页中打开应用”，原侧边进度组件已显示“让 GPT 分析实施结果”“验证阶段”“等待恢复”和“已中断，等待恢复。”；未点击分析或返回对话，当前展示方式保留。该次重展示后 Task/Run/分析/审批摘要仍与更新前一致。截图：`build/chatgpt-implementation-analysis-live.png`。同时观察到 ChatGPT 历史回复正文显示“无法加载此回复”，不把组件加载成功扩大为历史消息加载已修复。
