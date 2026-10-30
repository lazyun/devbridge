# 运行会话工作台 V1：实施与静态验收记录

## 现场与检查边界

- 分支：`dev_20260908_1913`
- 起点完整 HEAD：`1451567fdcb6232873ce17ab8b8b6058b5d9bb15`
- 起点已跟踪工作树无未提交修改；历史基线无需切换。
- 起点文件原文和 SHA-256：`/private/tmp/devbridge-session-workbench-baseline`，按仓库相对路径保存；各写者独立 manifest，汇总文件另列本轮增量。
- 本轮只修改生产源码及文档。没有测试、编译、构建、go vet、模拟、fixture、benchmark、live SQLite、真实账户或 Turn 探测、浏览器/设备操作、服务启停、迁移应用、暂存或提交。
- 指定事故 Task / Execution / Run / Confirmation 未被业务操作、恢复、取消、重放或改写。
- 起点已有能力包括原 Console 详情与指令读取、运行时协议请求、持久审批、取消、配置冻结、原 epoch/claim/finalization；本轮复用这些权威实现。

## 统一预约与交接

```text
确认 / 合法显式启动 / 明确 continuation 授权
  → 原预约事务：准确 Task + revision + Execution + purpose
  → 创建或返回原唯一 Run；新 Run 同事务登记 runtime_handoffs
  → ResumeReserved / 原 Review handoff
  → RuntimeSupervisor.startReservedWithExecutor
  → 当前 epoch single-writer + REGISTERED→PREPARING CAS
  → 原 PrepareReservedExecution / PrepareReservedReview
  → Supervisor 持有准确 Run；持久 accepted_at
  → 原 ExecuteRun / Codex 发送 / 原终结
```

`runtime_handoffs` 是 Run 的投递记录。授权、定义和配置沿不可变 Run/Execution 外键读取，表内不另存一份业务授权。无历史回填；Core 重启不扫描 CREATED 盲目派发。

| 入口 | 预约和交接 |
| --- | --- |
| StartTaskExecutionExpected | 原期望身份事务 → ResumeReserved |
| StartTaskExecutionInstructions | 原指令物化/预约事务 → ResumeReserved |
| Service.Start | ReserveExecution → 同一 ResumeReserved 内部实现 |
| AuthorizeAndStart | 原原子授权/预约 → ResumeReserved |
| ApprovedContinuation | 原 REQUEST_CHANGES / REQUEST_REVERIFICATION 回执资格 → ReserveExecution → 同一内部实现 |
| Coding Review / 显式重放 | 原 Review 预约 → StartReservedReview → Supervisor CAS |
| Implementation 后的 Verification | 原 Verification 预约 → 同一交接表 CAS → 原受管 Session 转移准确 Run |
| Verification-only 后继 | 原预约 → StartAuthorizedVerification → Supervisor CAS |
| Upper Review / Direct File | 保留原授权合同，不制造 Codex 会话或接管证据 |

后台不从一般 CONFIRMED/PENDING 状态推导新的执行许可，不读取旧 Session owner 冒充当前 caller。明确“批准但暂不执行”不会因此变成后台启动。

| 状态 / 诊断 | 准确含义 |
| --- | --- |
| REGISTERED / RUN_RESERVED | 原工作已预约并登记投递；尚无接管证据 |
| PREPARING / HANDOFF_IN_PROGRESS | 只有一个调用持有准备资格；不等于后端受理 |
| ACCEPTED / SUPERVISOR_ACCEPTED | 准确 Run 的 Supervisor 接管已写 accepted_at |
| REJECTED | 本次交接明确失败；原 Run 和已消费授权仍保留 |
| UNKNOWN | 身份、旧 epoch、可能发送或回执结果不能证明；不自动再发 |
| TERMINAL | 原 Core Run 已有终态；是否曾接管仍以 accepted_at 区分 |
| HANDOFF_EVIDENCE_UNAVAILABLE | 历史 Run 没有此轮接管记录；不回填、不据此恢复 |
| HANDOFF_EPOCH_NOT_CURRENT | 原运行 epoch 已非当前；不能显示当前受管连接 |

确认 START 步骤需接管证据；仅预约或 PREPARING 不会记作接管完成。已消费失败保留 CONSUMED、准确 Run、前置步骤和 RESERVED 效果。UNKNOWN 回读原交接记录。历史 completed 不改写；工作台继续显示当前准确 Run 和独立交接诊断。

## 会话读取与采集合同

原 transport 拥有者向 `Session.Observe` 提交同源事件。消息/计划/命令增量进入有界展示队列，协议请求及请求解决/拒绝仍走原权威路径。普通展示事件不阻塞 RPC 读取，不另读 Events/stdout，不启动额外 Codex。

- 通知队列 64 项，每条 64 KiB；32 条或 100ms 批次。
- item 正文/输出有界；消息增量按准确 item 合并，最终正文替换，合法重复文字不去重。
- 历史最多每 Run 1024 个物化 item / 16 MiB；单项 64 KiB；单页最多 50 项。
- 历史按稳定 ordinal 倒序分页，`before` / `nextBefore` / `hasMore`；item revision 单调替换。
- 普通消息、计划与 reasoning 区分；不保存 reasoning 正文。
- 精确 Thread/Turn/connection/epoch 隔离；无法归属不挂到当前 UI 选择。
- `PARTIAL` 明确说明物化 item 不是完整事件日志；`GAP`、`TRUNCATED`、`UNAVAILABLE` 附准确原因。
- 旧历史没有采集记录时保留缺失，完成 AgentClaim 不补写成过程历史。
- 新历史写入使用独立有界 worker，不在原 runtime request 的 `Store.Save` 事务内执行。队列最多 2 个批次，每批最多 128 个已物化 item，每次历史事务 250ms deadline；仍使用原 single-writer、epoch、Run/connection/Thread/Turn 围栏。
- 原展示 Snapshot 按批次写入，使用 150ms deadline。原审批注册、Resolve / BeforeDispatch / Delivered 保持同步权威合同；它们仍可能等待原 Session 锁和 single-writer 的短事务。本轮没有宣称锁等待为零，也没有运行延迟测量。
- 历史保存 `materialized_sequence`；落后显示 `MATERIALIZATION_PENDING`，关闭、终态或旧 epoch 后仍落后显示 `HISTORY_PROJECTION_INCOMPLETE / GAP`。队列溢出或写失败保留缺口，不改变 Task 成败。
- `Close` 立即关闭新响应准入并移出当前连接注册表；随后排空展示、保存最终 Snapshot，再排空历史（最多 750ms）。调用最多等待 2 秒后取消展示 worker；后端及请求生命周期仍由原 Core/client 管理。

| 读取接口 | 合同 |
| --- | --- |
| GET /v1/runs | project_id、feature_area_id、scope=current/history/all、limit/offset；数据库有界，包含 CREATED |
| GET /ui/api/runs/{id}/session | 准确 RunDetail、RuntimeSession 快照、原取消资格、Task phase、实际 conversation boundary |
| GET /ui/api/runs/{id}/session/items | 独立授权的有界历史页 |
| GET /ui/api/runs/{id}/session/instructions | 冻结作者指令、已有实际输入记录，准确原文复制 |
| GET /ui/api/runs/{id}/session/events | generation + cursor；独立客户端 SSE，物化快照替换和 Core facts 更新提示 |

每次快照/分页/订阅读取都检查受信本机入口、Project 和准确 Run owner；可选 task_id 额外校验 Task。AI Session 原接口继续校验原 owner，不选择同项目第一 Session。SSE 每秒重新核验，2分钟重新建流，单次写超时5秒，退出清 timer/deadline。token 不在 URL 或浏览器持久状态。

流 generation 范围为服务进程 + 准确 Run + 缓存实例；Core 重启或 256个视图缓存淘汰后变化。游标为物化视图修订，不是持久事件 ID。快照后更新可以补读为最新快照；中间瞬态不宣称逐条重放。generation 错误/越界明确 reset；客户端重读。多客户端各自读取，不竞争消费原协议事件。

## 原运行时请求与取消

- CODEX_REQUEST：原 Resolution DTO，保留 connection、Thread/Turn、item、requestId 原 JSON 类型；原 Resolve / BeforeDispatch / Delivered 校验、唯一消费及 canonical cancellation。
- RUNTIME_APPROVAL：原持久 Approval 与 NormalizeDecision / Decide；新增本机操作者薄适配，仅原 allowed_decisions。授予权限从原请求集合读取，UI 不提交脱敏内容作为授权。
- Runtime Approval 本机写侧要求当前准确 AVAILABLE/CONNECTED/非零 sequence 及 Run/Thread/Turn 一致；历史、未知连接、已结束请求拒绝操作。
- 原 INTENDED / SENT / TRANSPORT_WRITTEN / CONFIRMED_RESOLVED / RESPONSE_UNCERTAIN 不改名为命令执行成功。
- 取消调用原准确 AgentExecution/owner cancellation。按钮能力来自当前原 Task 投影，必须匹配用户选定 Run；ACK 显示处理中直到原终态。
- 请求拒绝与整个 Run 取消分开。未知响应先回读，不自动再次发送；不增加强杀、清 claim 或泛用恢复按钮。

## Desktop 与共享 Web 工作台

实现和人工宿主验收要点见 `docs/user-guide/desktop-and-console.md`。工作台复用同一 Web 模块与 HTTP 合同，列表/会话/指令/文件变化/技术详情分区；消息安全 Markdown，命令输出文本，Agent diff 与 Bridge Git 证据分开。

实际嵌入路径是 `desktop/main.go` 的 Wails AssetServer Middleware → `desktop/console_embed.go` → 当前 `supervisor.Snapshot().Core` 的真实 loopback endpoint → 共享 `/ui/?workbench=sessions` iframe。代理不建立监听服务，不启动另一 Core/Codex。Wails 原生来源使用进程内随机 document capability；HTML 注入 meta，API/SSE 仅通过 header 携带，不持久化。代理丢弃调用者认证、cookie、Origin、Host 和转发头，Core 仍按原真实 loopback HTTP 准入。Core 的 CORS、Origin、Host 和防嵌入头未放宽；仅原生代理返回的文档允许自身 iframe。Desktop dist 未构建，真实宿主加载与流传输尚待运行验收。

关闭会话只清本地订阅；退出 Desktop 沿原托管 Core 生命周期。当前运行所有者为 `IN_PROCESS_SUPERVISOR`，`supportsCoreRestartReattach=false`。独立 Host daemon、跨 Core 重启运行保活、孤儿接管、聊天/steer、任意终端及自动重跑均未实施。

## 独立静态矩阵与签署

采用交叉源码复核：A 实施 M1，B 实施 M2，C 实施共享 UI/Desktop，主代理实施薄 API 并复核 M1/M2/M3；A 另行复核 B 的队列、历史与关闭路径，C 另行复核主代理的 HTTP、Console 与原审批接线。以下是源码证据，不是测试或实际运行结果。

| # | 独立静态证据 | 结论与限制 |
| --- | --- | --- |
| 1 | `executionproposal/service.go` 的 Start / ResumeReserved、`approved_continuation.go`、`sqlite/execution_proposal_store.go`、`task_execution_handoff_store.go` | 前后台先预约同一持久 Execution/purpose Run，再进入共同交接；旧 Run ID 保留。后台只使用原明确后继授权。 |
| 2 | `execution/runtime_supervisor.go`、`sqlite/runtime_handoff_store.go`、`executionproposal/handoff.go` | REGISTERED→PREPARING CAS 先于准备；空 ID、仅同 Task、PREPARING 不构成 Accepted。重复调用回读或明确 IN_PROGRESS。 |
| 3 | `controlapi/confirmation.go`、`confirmation_failure.go`、`sqlite/confirmation_recovery.go`、`consoleapi/runtime_session_state.go` | START 完成要求 accepted_at；Supervisor、实际 Turn 和原终态分别展示。历史 completed 不改写，也不代替当前事实。 |
| 4 | `execution/runtime_supervisor.go` 的 startReservedWithExecutor、`coordinator.go`、`controlapi/task_execution_expected.go` | CAS 失败者不 prepare/finalize 获胜者；未知发送/旧 epoch 保留 UNKNOWN，已消费确认保留原 Run/步骤，不盲目重派。 |
| 5 | `sqlite/run_session_list.go`、`run/session_list.go`、`consoleapi/api.go`、`product-session-workbench.js` | 列表从 Core Run 有界分页，含 CREATED/准备失败/历史；缺 Thread/Turn 保留空值。准确导航不选择最近 Run。 |
| 6 | `codex/agent_runtime.go`、`agentruntime/service.go`、`session_readmodel.go`、`sqlite/agent_session_history.go` | 原 transport 同源复制；展示非阻塞队列、独立历史 worker；客户端不读 Events。原权威请求路径保留。短锁/单写者争用有界设计，实际延迟未测。 |
| 7 | `agentruntime/service.go` 的 observe / item 合并、`sqlite/agent_session_history.go` | 准确 Thread/Turn 过滤，本地接收顺序；delta 追加、最终 item 替换。容量、物化水位与写失败显示缺口；不保存 reasoning 正文。 |
| 8 | `agentruntime/session_readmodel.go`、`bridgehttp/runtime_session.go`、`product-session-workbench.js` | Run/generation/cursor 范围明确，更新替换最新物化快照；失效 reset。客户端有独立读取和发送超时；不承诺每个瞬态事件重放。 |
| 9 | `consoleapi/runtime_session.go`、`bridgehttp/runtime_session.go`、原 localoperator 准入 | 列表/详情/指令/分页/SSE 逐次检查 Project/准确 Run owner；SSE 每秒重新准入。沿原 owner，不借用同项目 Session。 |
| 10 | `desktop/main.go`、`desktop/console_embed.go`、`desktop/frontend/src/views.js` 与 `main.js` | 真实原生 middleware 与同源 iframe 进入共享 Web 模块；代理采用实际受管/附着 Core。宿主表现未运行验证，dist 未重建。 |
| 11 | `consoleapi/runtime_session.go`、`runtime_session_state.go`、`product-session-workbench.js` | 冻结作者指令/记录输入/Agent diff/Bridge Git 来源分开；默认 ACTIVE、sequence=0 和缺失用量不充当运行或 0。 |
| 12 | `product-session-workbench.js` 的 attach/stop/断线逻辑、`desktop/console_embed.go` context | 读取与关闭只管理本地请求、订阅和 timer；缓存表单断线禁写，切 Run 隔离旧响应。不清原请求、不自动派发。 |
| 13 | `controlapi/runtime_approval_operator.go`、原 agentruntime Resolve / BeforeDispatch / Delivered、UI 原请求表单 | 两类请求分开，原 requestId 类型与 allowed responses 保留；写侧重验当前身份与响应。响应不明回读，无自动再提交。超出 JS 安全整数的数字 ID 只读。 |
| 14 | `consoleapi/runtime_session.go` 的准确 cancellation capability、UI 取消与原 execution cancel route | 取消匹配准确 AgentExecution/Run；请求 decline、原合同 cancel、整个 Run 取消分开；ACK 不当终态。 |
| 15 | M1 原预约/消费 helpers、`scopereview/service.go`、`execution/coordinator.go`、原配置引用与领域准入 | 复用冻结配置、Verification 继承、领域串行和 canonical failure/finalization。Upper/Direct File 与原确认卡合同保持；无运行回归证明。 |
| 16 | DTO owner/capability、Desktop 原生命周期、此次工具与文件记录 | 明确 IN_PROCESS_SUPERVISOR 与 restart reattach=false；没有独立 Host、启动扫描或孤儿接管。未操作事故对象、live DB、服务、浏览器或真实 Turn。 |

表中 `sqlite/` 简写对应 `internal/storage/sqlite/`，其余省略前缀的 Go 文件位于 `internal/`，共享 UI 文件位于 `internal/bridgehttp/ui/`。最终精确文件清单、起点/终点 SHA-256 与本轮 patch 见现场保护目录的汇总 manifest。

实际检查仅包括：修改 Go 的精确 gofmt、修改 JS 的 `node --check`、逐文件限定 diff whitespace 检查，以及生产源码的 DTO、SQL 事务、授权和并发先后关系分析。未执行编译或类型检查，因此这些签署不包含编译通过、竞态通过或宿主运行通过的保证。

范围取舍：历史是有界物化 item，并非完整协议日志；不支持的协议表单和无法无损表达的数字 ID 保持只读。Desktop 接入所需原生代理属于本轮实际嵌入实现；无外部 Host、dist 构建或环境部署。历史 schema 追加版本 61，交接 schema 追加版本 60，均仅源码注册、没有应用。

操作偏差：最后一轮定位通用摘要函数时，一次检索误用了 `internal/consoleapi/*.go` 通配，返回了一条测试文件匹配。这违反了本轮“不得读取或列举测试相关路径”的限制；未继续打开该文件、修改它或执行测试。其后检索恢复到逐个准确生产文件。不能将本轮描述为完全没有触及测试路径。

最终交叉复核未发现未闭合的源码阻断。复核中修正了历史写入耦合、关闭准入、历史物化落后诊断、正文二次裁剪未标记，以及 standalone 审批 CANCEL 标签含义；整改均重新交叉阅读。

- UNIFIED_RUNTIME_HANDOFF_STATIC_VERIFIED
- AGENT_SESSION_READMODEL_STATIC_VERIFIED
- DESKTOP_AGENT_SESSION_WORKBENCH_V1_STATIC_VERIFIED

以上三项仅签署本轮约定的静态源码范围；受禁止构建/测试的约束，不代表编译、宿主或运行并发已通过。

## 后续人工验收（需另行授权）

1. 在新建、明确授权的工作上执行一次正常确认；对应同一准确 Run、交接回执、实际 Turn 和原终态。
2. 前后台竞争、重复点击与响应丢失；同一准备/接管且不误终结获胜者，已消费回执不退回未消费。
3. Desktop 与 Web 同时订阅；关闭/重开、Core网络断开与重连，旧游标明确重读且断线表单禁用。
4. 长消息/命令输出、历史分页、容量溢出；显示 gap/truncation，完成正文不重复追加。
5. 两客户端回答同一准确请求；仅一个合法决定生效，另一个回读冲突，传输未知不自动重发。
6. 准确取消与迟到响应；ACK 不作终态，拒绝单项不等于取消整个 Run。
7. 旧 Run 无内容/无 Thread/Turn/无交接记录；清楚显示缺失，无读取副作用。
8. 使用实际 Desktop 宿主验收内嵌页面、流转发和退出生命周期。不得使用本轮指定事故 Run 做恢复或故障注入。

保留：

- UNIFIED_RUNTIME_HANDOFF_RACE_ACCEPTANCE_PENDING
- DESKTOP_AGENT_SESSION_HOST_ACCEPTANCE_PENDING
- AGENT_SESSION_STREAM_RECONNECT_ACCEPTANCE_PENDING
- DESKTOP_RUNTIME_REQUEST_DELIVERY_ACCEPTANCE_PENDING

原确认、文件交接、配置继承、派发与后继运行的运行验收标签未升级。

## 本轮精确文件清单

共 49 个最终有增量文件。50 个受保护记录中，`internal/storage/sqlite/agent_runtime_store.go` 已逐字节恢复起点，最终无差异，保留其原始哈希记录用于审计。

```text
desktop/console_embed.go
desktop/frontend/src/i18n.js
desktop/frontend/src/main.js
desktop/frontend/src/views.js
desktop/main.go
docs/desktop-agent-session-workbench-v1.md
docs/user-guide/desktop-and-console.md
internal/agentruntime/model.go
internal/agentruntime/service.go
internal/agentruntime/session_readmodel.go
internal/bridgehttp/console.go
internal/bridgehttp/observability.go
internal/bridgehttp/runtime_session.go
internal/bridgehttp/server.go
internal/bridgehttp/ui/index.html
internal/bridgehttp/ui/product-app.js
internal/bridgehttp/ui/product-session-workbench.js
internal/bridgehttp/ui/product-task-workbench.js
internal/bridgehttp/ui/product-workbench.js
internal/bridgehttp/ui/product.css
internal/codex/agent_runtime.go
internal/codex/client.go
internal/consoleapi/api.go
internal/consoleapi/runtime_session.go
internal/consoleapi/runtime_session_state.go
internal/controlapi/confirmation.go
internal/controlapi/confirmation_failure.go
internal/controlapi/execution_instructions.go
internal/controlapi/runtime_approval_operator.go
internal/controlapi/task_execution_expected.go
internal/controlplane/bootstrap.go
internal/execution/coordinator.go
internal/execution/runtime_supervisor.go
internal/executionproposal/approved_continuation.go
internal/executionproposal/handoff.go
internal/executionproposal/service.go
internal/run/service.go
internal/run/session_list.go
internal/scopereview/service.go
internal/storage/sqlite/agent_session_history.go
internal/storage/sqlite/confirmation_recovery.go
internal/storage/sqlite/execution_proposal_store.go
internal/storage/sqlite/migrate.go
internal/storage/sqlite/review_execution_handoff_store.go
internal/storage/sqlite/run_session_list.go
internal/storage/sqlite/run_store.go
internal/storage/sqlite/runtime_handoff_migration.go
internal/storage/sqlite/runtime_handoff_store.go
internal/storage/sqlite/task_execution_handoff_store.go
```

汇总证据位于 `/private/tmp/devbridge-session-workbench-baseline`：

- `manifest-final.json`：起点原文 SHA-256、最终 SHA-256、是否新增/是否改变、与起点 HEAD 的对应关系。
- `increment-final.patch`：本轮起点到终点的完整生产增量，含新增文件。
- `overall-head-final.patch`：准确 HEAD 到最终工作树的整体差异。本轮既有文件起点与 HEAD 相同，因此两份补丁内容相同；两种比较范围分别记录。
- `static-checks-final.json`：精确格式、JS 语法与限定差异检查结果。

代码留在工作树；没有暂存、提交、部署或应用迁移。
