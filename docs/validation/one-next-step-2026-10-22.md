# 单一当前下一步（2026-10-22）

## 问题与行为

任务处于验证中断恢复状态，但同一 Execution 的实施输出尚未分析。旧页面同时突出“等待恢复”和“让 GPT 分析实施结果”，要求用户自行判断优先级。

共享 TaskView 新增只读 `nextStep`，由准确 Task、定义修订、当前 Execution 和其关联 Run 的证据派生；WebUI 与 ChatGPT 使用同一投影：

- 当前推荐先分析实施输出时，主提示统一为“待 GPT 分析实施结果”，主入口指向准确实施 Run。
- 验证中断保留在可展开的“运行背景”；Web 的恢复等操作收纳在“其他操作”。不删除事实或恢复能力，不把推荐变成强制准入。
- 分析入库后该推荐消失，再呈现仍需处理的验证恢复。必需验证分析优先于可选实施分析；已记录、未知分析、活动 Run、终态 Task、历史只读目标、错误 Execution/Run/用途/修订或缺失恢复来源不借用历史记录。
- Canonical phase、Run 状态、操作资格、冻结方案、审批、验收和恢复合同不变；展示读取不写库、不开始或恢复执行。
- 新版组件收到 `nextStep:null` 时不再用旧规则猜测分析目标；点击时重新核对目标与分析状态。

当前原任务投影为 `nextStep.code=ANALYZE_RUN_OUTPUT`，`purpose=IMPLEMENTATION`，`recommended=true`，绑定 `run_impl_execution_approved_e9438ed7b1d342eb88baa7682fc82093`。`phase=RECOVERY_REQUIRED`、`businessPhase=VERIFYING` 继续保存原真实状态。

## 验证与限制

- 共享展示、Console 测试通过；Core 只读展示定向回归通过（约 2 秒）。
- Web/MCP JavaScript：424 项，420 通过、4 跳过、0 失败。
- Bridge HTTP 全套通过；MCP Go 除两项已知文档合同失败外通过。两项排除项为 `TestExternalWorkflowDiscoveryContract` 和 `TestCurrentDocumentationFreezesFeatureAreaReviewVocabulary`，未将完整 MCP 套件记为通过。
- 广泛 Core 回归因已有原生测试子进程等待被终止并保留栈日志，不记为全套通过；仅终止本次启动的测试进程，未处理其他早先遗留进程。
- 相关 go vet、差异空白检查通过。构建因磁盘不足重试过一次，仅删除本任务临时 Go 缓存中的旧项目对象及旧候选二进制副本，生产文件和回滚备份保留。

日志：`build/one-next-step-go.log`、`build/one-next-step-control-focused.log`、`build/one-next-step-node.log`、`build/one-next-step-integration.log`、`build/one-next-step-vet.log`。

## 本机部署

已部署 `.29`（`0.1.0-dev.full-flow-20261022.29+gd3969cf`），真实 MCP 诊断 ready=true，业务工具目录摘要未变，数据库版本仍为 130。更新备份位于 `build/full-flow-update-20261022-29/`。ChatGPT 资源版本为 `ui://devbridge/card/one-next-step-v13`，旧 URI 兼容读取。

真实 Web 页面回读任务列表、详情主标题、当前下一步、任务结果状态与下一步操作，均为“待 GPT 分析实施结果”或对应分析说明；恢复操作在收起的“其他操作”内。

## GPT 摘要回执补齐

真实原会话验收发现 `workspace_show` 的模型可见紧凑回执没有 TaskView 全文，故新增 `workflow.nextStep`，与卡片私有快照的 TaskView.nextStep 同源。工具描述要求存在该字段时用其 label/summary 解释当前下一步，保留 canonical phase 与准入事实。历史、错绑任务或读取失败时不携带当前推荐；Schema、可见回执与私有数据边界测试通过。修复部署在 `.30`，回滚备份为 `build/full-flow-update-20261022-30/`，组件代码与 `.29` 相同，仍使用 v13 URI。

补齐后的回执字段为 `workflow.nextStep`，与私有组件的 `presentation.taskView.nextStep` 同源。定向 Schema/SDK 回放/资源兼容/不泄漏测试通过（0.8 秒），新增回执的 go vet 通过。`.30` 更新前后原 Task、10 条 Run、GPT 分析及审批摘要逐项一致，活动 Run 为 0。

## 原会话真实验收完成

原 ChatGPT 会话重试同一个展示 requestId 后，模型可见 `workflow.nextStep` 已回读并回复“当前下一步：待 GPT 分析实施结果”。刷新组件资源后，原侧边组件主提示与正文一致；验证中断在收起的“运行背景”内。点击组件“刷新”后主提示保持不变；没有提交分析或恢复任务。

`.30` 的 MCP 相关全套（仍排除上述两项既有文档合同失败）通过，耗时约 43 秒。重展示后 Task、10 条 Run、GPT 分析、审批意图及回执摘要仍与更新前逐项一致。截图：`build/one-next-step-chatgpt-live.png`、`build/one-next-step-web-live.png`。
