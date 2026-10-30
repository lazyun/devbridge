# Task Events、Codex 历史与普通对话兼容验证

日期：2026-10-21。此记录区分本地测试与后续真实服务更新；最新部署状态见文末。

## 交付范围

- 普通 ChatGPT 对话继续使用原工具、确认卡片、三种审查方式与手动刷新；不要求 Work / Cloud 或订阅事件。
- 可选的 MCP `task.updated` 订阅：准确 Session / Task 归属、签名回调校验、事务 outbox、持久化重试、取消订阅及只读数量诊断。SQLite migration 127 由 Core 升级时应用。
- Bridge 会话页显示准确 Thread，并由用户主动读取原 Codex App Server 中准确 Turn 的分页历史；后端不可用时保留 Bridge 已采集记录。不恢复执行或启动替代进程。

## 本地结果

以下命令使用独立临时 `GOCACHE`，均退出 0：

```sh
go test -p 1 ./internal/workflowevents ./internal/storage/sqlite ./internal/codex ./internal/mcpserver ./internal/bridgehttp \
  -run 'Test(WorkflowEvent|ReadPersistedHistory|History|WorkflowEvents|CodexHistory)' -count=1
go test -p 1 ./internal/workflowevents ./internal/codex ./internal/consoleapi ./internal/controlapi ./internal/bridgehttp \
  -run 'Test(Event|PersistedHistory|TaskReviewMode|ConfirmationTaskReview|TaskRunHTTP|ConsoleArchitecture|ExecutionProjection|WebReviewCurrent)' -count=1
node --test internal/bridgehttp/product_session_history_test.js internal/bridgehttp/product_session_user_decision_test.js
node --test internal/mcpserver/confirmation_task_review_ui_test.js internal/mcpserver/confirmation_object_identity_test.js internal/mcpserver/dev_bridge_refresh_modes_test.js internal/mcpserver/dev_bridge_host_capability_test.js internal/mcpserver/dev_bridge_task_centric_test.js
go build -p 1 ./...
go vet -p 1 ./internal/workflowevents ./internal/codex ./internal/mcpserver ./internal/storage/sqlite ./internal/controlapi ./internal/consoleapi ./internal/bridgehttp
```

前两组是定向 Go 测试；第一组的 workflowevents、codex 没有匹配用例，由第二组实际运行其新用例。两组 JavaScript 共 40 项通过；受影响七个 Go 包的 vet 通过。

关键覆盖：MCP 2.0 Events 发现与版本限制、普通新版/旧版工具列表不依赖订阅、回调签名与校验拒绝、重试与永久拒绝、回调地址限制、事务回滚与重启、运行问题事件去重、过期与越权订阅隔离、准确 Thread / Turn 分页、未知工具参数与原始推理内容不返回、跨任务 HTTP 读取拒绝、切换 Run 时丢弃过期响应，以及审查选择和确认流程回归。

构建时 Go 尝试写入工作区外的模块版本信息缓存，出现权限提示；构建本身退出 0。仓库全量 `git diff --check` 发现既有 `internal/execution/coordinator.go` 两处空白行尾空格，本轮未修改该文件。

## 首轮交付时未完成的真实宿主验收

- 未更新或重启当前运行的 Core；未对真实任务发起订阅或调用真实模型。
- 连接中的服务此前只读确认支持 MCP `2026-07-28`，这不证明更新后 Events 已被宿主扫描。
- Work / Cloud 经 Tunnel 的回调验证、关闭卡片后的实际提醒、停止订阅、普通对话手工操作需按[用户指南](../user-guide/task-events-and-codex-history.md)完成。
- 没有声明全量 Go 测试通过，也不保证官方 Codex Desktop / CLI 默认列表自动显示 Bridge 创建的会话。

## 后续真实服务更新

用户要求继续后，已构建并安装三个运行程序，通过 Desktop 的托管重启入口载入新版。当前 Core 与 GPT MCP 的版本均为 `0.1.0-dev.events-20261021+gd3969cf`；Core、GPT MCP、本地 MCP 均 ready，原 OpenAI Tunnel 恢复 ready。Desktop 外壳未重新打包，Console 与会话页资源由新版 Core 提供。

升级前通过 SQLite backup API 保存完整数据库快照，旧程序和快照仅保存在已忽略的本地 `build/events-update-20261021/`，不进入 Git。升级前后没有活动 Run，原有 `CANCELED` 和 `SUCCEEDED` Run 各一个保持不变；数据库迁移从 126 到 127，`PRAGMA integrity_check` 返回 `ok`。

通过真实本地 GPT adapter 的只读请求确认：

- `server/discover` 已声明 `events: {}`，`events/list` 返回 `task.updated`。
- MCP `2026-07-28` 和 `2025-11-25` 均可列出原有 63 个工具，旧版初始化成功。
- 真实 Task 的通知诊断接口可读，活跃、停止、待投递、已送达、耗尽数量均为 0；没有自动订阅。
- 本对话连接的 `bridge_diagnostics` 回读到新版版本号及 ready 状态。

这些检查证明新版服务已运行及旧协议可用，仍不等同于 Work / Cloud 主动通知完成端到端验收。

随后在 ChatGPT 网页的已安装 `devBridge-test` 管理页执行“刷新工具”。刷新结束后按钮恢复可用，页面没有显示错误；该页面不列出事件目录，因此不据此声明宿主已导入 Events。刷新后的截图仅保存在上述已忽略的本地构建目录中。

当时端到端验收仍等待用户指定 Work / Cloud 对话及准确 Task。没有代替用户选择工程操作或创建真实订阅。当前两个历史 Run 均无 Thread / Turn，也不能用于验证真实 Codex 分页内容读取。

## Work 对话中的任务重建与卡片验收

用户随后提供了真实 Work 对话，并明确要求重建现有任务、关闭旧任务。通过该对话的正式 Dev Bridge 工具取消旧 Task，随后只读核对数据库确认其状态为 `CANCELED`，历史记录仍在。

首次重建受阻：宿主将复杂输入类型显示为 `unknown`，Work 代理无法正确构造必填的 `reviewPlan`。源代码的 MCP JSON Schema 已有完整定义，并不是 Core 缺失这个字段。给出准确公开形状 `{"mode":"REPOSITORY"}` 后，第二次准备成功。另在三个 prepare 工具的说明中补充两种合法计划示例和派生范围边界，Task / 初始 Task 的 reviewPlan schema 也加入说明。输入校验保持关闭未知字段；该说明后续随诊断版本一同部署，真实 `tools/list` 已回读到三个工具的参数示例。

新增示例与实际关闭式 schema 的匹配测试，以及既有三方式准备测试通过：

```sh
go test -p 1 ./internal/mcpserver \
  -run 'Test(ConfirmationReviewPlanExamplesRespectClosedInput|TaskPrepareOffersBothReviewExecutors)' -count=1
```

实际 Work 页面已显示新的“创建任务”卡片，审查下拉框包含 AI 文件审查、Codex 审查和不审查。只读核对新 pending intent 的标题、问题描述、全部五条约束、领域与仓库均与旧 Task 一致，FAST 深度与原 Review 一致；两个执行器的审查指令已准备。卡片加载首次失败，点击宿主的“重试”后恢复可见；没有修改 CSP 设置。

当时新 intent 为 `PENDING`，没有代替用户选择或提交卡片，因此尚无新的正式 Task；通知订阅未建立。卡片截图和逐项核对结果仅保存在本地已忽略的构建目录。

用户随后在卡片中选择 `UPPER_LLM` 并确认创建。只读回读确认原 intent 为 `COMPLETED`，新正式 Task 为 `ACTIVE / REVIEWING`，准确 Review / Run 已建立；旧 Task 仍为 `CANCELED`。已在用户指定的原 Work 对话中继续请求为新 Task 建立 Events 订阅，并按已确认的准确 Review 授权读取文件、提交正式报告；没有授权实施或创建 Coding Agent 工作。最终通知投递结论需另行记录。

## 真实回调诊断与 fake-IP 兼容

Work 中的 AI 文件审查随后读取四份授权文件并提交正式报告，结论 `APPROVAL_READY`；数据库回读新 Task 已为 `REVIEW_APPROVAL_PENDING`，Run 为 `SUCCEEDED`。没有修改工程文件或批准实施。

最初订阅和一次重试均返回宿主 `task service returned an unexpected error`。增加无密钥阶段日志后，实机确认 `events/subscribe` 确实到达 Core，授权及参数验证通过，但在 `callback_request` 阶段返回 JSON-RPC `-32015`；随后宿主调用 `events/unsubscribe` 清理。不能把这个宿主通用报错当成请求未到达 Bridge 的证明。

本机系统解析公开域名 `chatgpt.com` 返回 `198.18.0.12`；使用固定 IP、正常 TLS 校验的 Cloudflare DoH 查询则返回真实公网地址。已为域名的这种合成 DNS 答案补充严格公网重新解析，字面 fake-IP、私网、回环及其他非公网目标仍拒绝。解析请求不携带回调路径或签名材料，最终连接固定校验后的地址。没有修改系统或代理软件的 DNS 设置。

兼容修正及诊断已构建并部署至 `0.1.0-dev.events-20261021.2+gd3969cf`，重启前确认无活动 Run，重启后连接工具回读 ready。以下测试和受影响两包 vet 通过，三个命令行程序构建成功：

```sh
go test -p 1 ./internal/workflowevents ./internal/mcpserver \
  -run 'Test(Event|Callback|WorkflowEvents|ConfirmationReviewPlanExamplesRespectClosedInput|TaskPrepareOffersBothReviewExecutors)' -count=1
go vet -p 1 ./internal/workflowevents ./internal/mcpserver
```

覆盖普通公网解析不使用备用解析、fake-IP 重新解析、字面 IP 与私网答案拒绝、备用答案仍须公网、固定解析端点、TLS 与代理/重定向规则、错误域名/截断/过大解析响应拒绝、网络错误分类及日志不包含回调凭据。最终订阅与通知投递以实际宿主回读和持久化投递记录为准。

### 修正后的真实订阅结果

2026-10-21 19:06（Asia/Shanghai）再次在原 Work 对话中订阅后，Core 日志记录 `events/subscribe`、`workflow.subscription.established` 和 `mcp.events.succeeded`，回调签名挑战验证通过。数据库出现且仅出现一个指向新 Task 的 `ACTIVE` 订阅；宿主回读确认对应事件监控启用、没有定时规则或轮询。默认本次 Core 租约至次日 19:06，后续需由宿主按返回的 `refreshBefore` 刷新。

订阅创建发生在审查完成之后，因此当前投递记录为空。这一结果通过了宿主发现、真实订阅、回调验证与持久化链路，**尚未通过真实业务事件投递、GPT 提醒及停止订阅验收**。保留新 Task 在 `REVIEW_APPROVAL_PENDING` 等待用户确认；后续由用户正常确认引起状态变化，再检查 outbox ACK 与 Work 提醒。没有为了制造测试事件批准实施、重做审查或写入伪造业务事件。
