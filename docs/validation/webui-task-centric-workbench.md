# WEBUI_TASK_CENTRIC_WORKBENCH_REDESIGN_V1

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-46de21b46843ce9b)。

交付状态：

```text
WEBUI_TASK_CENTRIC_WORKBENCH_IMPLEMENTED
MANUAL_WEBUI_DESKTOP_ACCEPTANCE_PENDING
```

未标记 GLOBAL_READY。没有实际启动浏览器、Desktop、模拟器或设备；没有操作真实 Task、Review、Decision、Execution、权限请求、cancellation 或 recovery。本报告中的 DOM/VM 检查不是视觉或实际窗口布局验收。

## 基线与候选身份

- Repository root：`/Users/phoenix/Documents/devbridge`
- Branch：`dev_20260908_1913`
- Starting HEAD：`7896c5c5d36a1f5f981ef04c1be840aec6d70af7`
- 开始时 `git status --short` 与 `git diff --stat` 均为空；没有既有 dirty source/build diff。
- 最终保持同一分支、同一 HEAD；本轮代码、测试和文档为未提交工作树修改。无 commit、merge、push、部署、stash、reset、clean。
- 候选 Core：`/tmp/devbridge-workbench-candidate/devbridge`。显式版本 `0.1.0-workbench-v1-dirty+g7896c5c`；不是 Starting HEAD 的纯净构建。
- [候选源文件与二进制身份](archive/webui-task-centric-workbench.md#file-d187b5e772793904)记录修改文件 SHA-256、Git 状态、候选 binary SHA-256 与 Go build metadata。临时目录可由系统清理；下面提供可重建命令。

## 最终领域对象、页面与接口

| 领域对象 | 页面 | 准确读取 | 唯一操作入口 |
| --- | --- | --- | --- |
| Project / Repository | 顶部项目、按仓库分支上下文；项目总览 | `/v1/project-overview`、`/v1/repositories`、`/v1/development-context`、`/ui/api/project-workspace` | 既有显式开发分支选择命令；顶部设置说明各自作用域 |
| FeatureArea | 领域列表、领域详情、任务过滤、按需工作区/Conversation、交付区域 | `/ui/api/featureAreas`、`/ui/api/featureAreas/{id}/presentation`；任务列表按 `feature_area_id` 读取 | 现有 CREATE_TASK / RESET_AGENT_CONTEXT 能力；领域菜单 canonical retirement 确认；交付区集成命令 |
| Task | 进度、横向审批操作与 Run 列表；无任务标签栏 | `/ui/api/tasks/{id}?project_id=…`；任务列表 `/ui/api/work-tabs/tasks` | Task 内 capability/target 绑定的既有 `/ui/api/tasks/{id}/action`；运行请求在 Task 操作区处理 |
| Review | 审查与方案：ordinal、FAST/DEEP、当前标记、历史指令/结果、Run | Task shared Review history；已有 `/ui/api/reviews/{id}/result` | 当前有效 Decision 的 Task 操作区；历史无审批控件 |
| TaskDefinitionRevision | 只读版本索引与展开后的冻结规格 | `/ui/api/tasks/{task}/definition-revisions/{revision}?project_id=…` | 无原地保存；REQUEST_CHANGES 仍通过原 Task 命令形成后继规格 |
| Execution | 执行批次：current、revision、实施/验证 Run、结果与验收 | Task shared executionHistory；Run 详情按需加载交付与验证事实 | 当前 Task 的同一 Acceptance Decision；历史只读 |
| Run | Task 概览紧凑列表、运行记录、同容器 Run Detail；诊断全局入口 | `/ui/api/tasks/{task}/runs`；`/v1/runs/{run}?project_id=…&task_id=…`；同一 `/prompt` | Run Detail 只读；待处理请求返回 Task |
| AgentExecution / AI Session / Inspection / 日志 / worktree | 诊断二级入口 | 既有全局 inventory、`/v1/runs`、`/v1/ai-sessions`、`/v1/context-probes`、`/v1/logs`、`/ui/api/worktree-inventory` | 只读观察，不新增审批/取消/恢复捷径 |

WebUI 仍走 HTTP → consoleapi/controlapi → shared workflowpresentation/Core。Desktop 未复制业务 UI，仍 iframe 同一 `/ui/`。ChatGPT Card、GPT 30-tool surface、app-only/Local MCP、flat diagnostics/bootstrap、receipt/revision/epoch 与 prompt transport 均保留原有边界。

## 新旧结构与职责

| 原结构 | 本轮结构 |
| --- | --- |
| 主入口为概览、领域、任务、文档、模块、工作区，诊断散列 | 项目选择器＋项目总览、领域、任务、项目资料、诊断 |
| 总览以少量领域/任务摘要为主 | 需要处理（业务 Decision / runtime request 分组）→异常与阻断→进行中任务→近期交付 |
| Task Current Work 嵌套整张 Review/Execution 内容 | 固定标题与阶段事实；横向审批与 Run 列表直接展示，移除当前事项及任务标签栏 |
| Runs 藏在技术诊断；独立全局 operation 入口 | Task 概览和完整运行记录共享摘要；Run 五个标签使用同一个 Drawer |
| 功能菜单混有集成操作 | 生命周期菜单保留关闭确认；集成在交付区域 |
| 文档/模块/工作区为并列工作流 | 项目资料＝文档/模块地图/仓库；诊断＝运行记录/运行身份与上下文/日志/worktree |

保存导航迁移：documents/modules/repositories → resources 对应二级入口；workspaces → diagnostics/workspaces；executions/runs → diagnostics/runs；logs/diagnostics 旧 view → 项目诊断；其他未知值 → overview。没有保留幽灵 HTTP endpoint 或第二套业务工作流。

Task.status/phase 与 Core current pointers 不因查看历史、Run 终态、计数或重连而改变。审查 Run 基础设施失败时仍可呈现 ACTIVE / REVIEWING，同时显示“审查未完成／本次运行已中断”。只有 Task SUCCEEDED 的 Done 为完成。验收、transport 完成、Verification Report、Git commit、开发分支 integration 分开表达。

Execution 来源仅由同项目、同 Task 前序 Execution 的实际 Acceptance receipt 提供 `sourceAction`。REQUEST_REVERIFICATION 表示相同 revision 的后继仅验证候选；尚无任何 Run 且无该证据时显示尚未生成/类型未知，不伪造 Implementation Run。

## 身份、分页和输出约束

`TaskRunDetail.executionId` 的旧值是 AgentExecution ID。本轮不改其含义；添加 `agentExecutionId`、`taskExecutionId`、`reviewId`、`operationKind`、`operationId`、精确 `runPurpose`。生产新 caller 不拿旧字段加载 Task Execution。Run ID 是行/导航身份，attempt/ordinal 不参与身份。历史冻结配置来自 durable Review/Execution；缺失为 UNKNOWN，不回退到 Desktop 默认。

Task 上下文的 Run detail、prompt、列表和 revision 读取由服务端验证 Project、Task、FeatureArea 与 Review/Execution 双向关联。错误 owner / 不存在对象返回明确错误；全局 Inspection 没有语义 Task 时保留诊断来源。

- 工作区总览最多100个 Task，增加 `tasks_total` / `tasks_truncated`。Task/领域列表在服务端过滤后分页，UI每页25，服务端上限100。
- 默认 `TaskView.details.runs` 最多20条。Task 概览显示前5条；完整记录每页20、上限100。purpose 在分页前过滤。
- 摘要按 createdAt 升序、相同时间按 Run ID 稳定排序；elapsed/status 更新不改变排序。
- Review/Execution history 与任务定义索引继续复用共享投影；完整 revision、Run runtime、输出、证据和持久化 prompt 按需读取。
- HTTP fixture 实测：23个Run的默认 Task **35,964 bytes**；20条 Run 摘要页约 **15,440 bytes**；单条 Run Detail **3,174 bytes**。这是该测试数据的观测大小，不是所有 Task 的总字节硬上限。[原始大小日志](archive/webui-task-centric-workbench.md#file-4d4a6a8fbe2b8a69)
- Run detail沿用 changed files≤256、events≤100、approvals≤100、证据artifact≤64KiB及summary截断；runtime复制后按现有item/output限制裁剪和脱敏，RawMessage payload仍为合法 object。Verification嵌套正文也有界。
- 持久化 prompt沿用原读取路径和既有16MiB上限/真实字节；未改变Prompt Neutrality、传输或用当前规格重建历史prompt。
- token AVAILABLE 的0、PENDING与UNAVAILABLE分开；cached input/reasoning output不重新计入total。非法/逆序时间显示未知；活动耗时带未完成标记。
- 内容经escape；不执行任意HTML或脚本。不存在、不适用、未生成和读取失败有明确显示。

存储侧沿用现有 `ListByTask` / product inventory 聚合读取，没有引入第二套registry或数据库迁移。HTTP响应与按需正文有界；极大历史量的聚合计算成本仍取决于现有存储查询，不能将上述观测字节数理解成全域硬上限。

## 导航、刷新与草稿

Typed detail route包含 Project、rootTask、TASK/RUN、准确target、source、activeTab、generation。深链接使用 `#project=…&task=…&run=…&tab=…&source=tasks`；首次恢复重新读取owner。全局Run可省略task；仅有真实Task关联才给Task跳转。

Task→Run→Back保留Task root、标签、Execution/Review稳定section key、Runs筛选、真实`#detail`滚动位置、精确Decision/revision草稿。失效草稿以只读保留文本呈现，不套用到新Decision。

后台读取保留last good snapshot，不加阻断遮罩。交互读取使用局部inert/aria-busy，Core长时间运行不作为Loading。导航/关闭/隐藏/离开中止本地请求并用generation隔离迟到响应。AbortController不调用后端取消。

Task mutation成功和readback失败分别记录；后者显示“操作已提交，状态刷新失败”，只重试读取。响应不确定时保留原request identity。读取失败不会重新开放旧Task或领域capability。返回、展开、轮询不补偿写入。

Esc优先处理操作弹层/确认层，其后Run→Task，再关闭根Drawer。关闭恢复焦点；弹层外点击阻止穿透。普通details不是操作弹层。设置在顶部，项目开发分支仍按Repository选择；Desktop/Core全局设置仍由Desktop宿主管理。

## 实际验证结果

| 检查 | 结果与边界 |
| --- | --- |
| 开始时受影响Go包 | workflowpresentation、controlapi、consoleapi、bridgehttp通过 |
| 开始时Node | 79/79通过，作为改动前基线 |
| 最终受影响Go包 | 4包通过；含Run owner/purpose分页、默认摘要同源、revision read-only/fence、source receipt测试 |
| 最终Node | 101/101通过；包含生产renderer、刷新协调、mutation/readback、整个index脚本启动与fixture handler集成 |
| Desktop前端Node | 46/46通过；Desktop业务UI未复制 |
| GPT/RawMessage定向 | 无socket的严格schema、实际SDK runtime payload、实际共享catalog、closed read branches、Execution identity测试通过 |
| `go build -p 4 ./...` | 通过；候选Core单独构建到/tmp |
| `go vet -p 4 ./...` | 通过 |
| `git diff --check` | 通过 |
| `go test -p 4 ./...` | 未全部通过：cmd/devbridge、adapterproxy、localmcp、mcpserver中的既有socket集成测试被沙箱拒绝监听；其余包通过。未禁用或降低schema/owner断言 |
| 真实浏览器/Desktop视觉、窄屏布局、键盘宿主行为 | 未执行，按任务约束留待手动验收 |

[受影响Go日志](archive/webui-task-centric-workbench.md#file-20ee1f6ea4b7070f)、[Node日志](archive/webui-task-centric-workbench.md#file-277dc795ef58b18a)、[Desktop日志](archive/webui-task-centric-workbench.md#file-e4f17e454e662f9d)、[schema日志](archive/webui-task-centric-workbench.md#file-df6503dff40dc3b7)、[全量Go日志](archive/webui-task-centric-workbench.md#file-15f858096494c728)、[build](archive/webui-task-centric-workbench.md#file-ced94295afa8771e)、[vet](archive/webui-task-centric-workbench.md#file-9fa055d572af5daa)。

真实阻断证据为 `listen tcp 127.0.0.1:0: bind: operation not permitted` / `httptest: failed to listen on a port`。预览transport的自动测试改用同一个HTTP handler和最小DOM契约double，不需要监听socket；没有把这种测试表述为浏览器测试，也没有扩大权限启动监听。Go构建可能输出module stat cache只读写入警告，但build/vet退出码均为0。

自动场景覆盖范围：

1. Task五段轨道及等待方案/实施/验证/验收/终态，由共享阶段测试和renderer capability测试覆盖。
2. FAST→DEEP、历史不可批准、Review基础设施失败但Task未终结，由shared/fixture/renderer测试覆盖。
3. 多Execution/revision、准确current、仅验证且无Implementation，由shared owner/source测试与fixture覆盖。
4. Run成功但验证FAIL、Task未验收、已验收但Git失败，由fixture/renderer和既有Git tests覆盖。
5. cancellation ACK、UNKNOWN、finalization/recovery采用现有投影与回归测试；没有执行真实取消/恢复。
6. Inspection无Task、错误Project/Task/owner、不存在/缺失历史，由HTTP fences、fixture及诊断renderer覆盖。
7. zero/PENDING/UNAVAILABLE、异常duration、object payload/schema、运行列表分页稳定性，由Go/Node契约覆盖。
8. Task→Run→Back的tab/filter/expanded/scroll/draft以及Project切换、late response，由完整生产脚本VM集成覆盖。
9. background与interactive区别、mutation成功/readback失败、旧导航、空态/部分失败、Esc优先级，由协调器和完整脚本/handler tests覆盖。
10. Desktop仍复用/ui/与GPT/Local接口保持，由既有Desktop检查及不依赖socket的schema/catalog测试覆盖；涉及真实宿主/socket的剩余验证明确待办。

## 手动启动与回退

推荐先启动纯fixture预览，不需要数据库、配置、Core或Coding Agent：

```sh
cd /Users/phoenix/Documents/devbridge
node internal/bridgehttp/testdata/task_owned_visual_host.mjs --scenario workbench --port 18187
```

在用户自己的浏览器中打开 `http://127.0.0.1:18187/ui/`。页面明确标识 `FIXTURE TRANSPORT`，直接加载当前生产HTML/JS/CSS，所有POST均返回405，不是复制的演示UI或正式fixture API。

可复用命令将scenario替换为 `empty`、`read-error`、`partial-history`，或使用 `--port 0` 获取临时端口。建议深链接：

```text
http://127.0.0.1:18187/ui/#project=project&task=task-2&run=run-verification-only&tab=overview&source=tasks
```

Core候选用于用户侧独立空数据验证；本轮只构建，没有启动该服务：

```sh
cd /Users/phoenix/Documents/devbridge
GOCACHE=/tmp/devbridge-workbench-go-cache go build -o /tmp/devbridge-workbench-candidate/devbridge -ldflags '-X github.com/lazyun/devbridge/internal/buildinfo.ProductVersion=0.1.0-workbench-v1-dirty -X github.com/lazyun/devbridge/internal/buildinfo.GitCommit=7896c5c5d36a1f5f981ef04c1be840aec6d70af7' ./cmd/devbridge
workbench_data=$(mktemp -d /tmp/devbridge-workbench-data.XXXXXX)
/tmp/devbridge-workbench-candidate/devbridge -mode server -data-root "$workbench_data" -listen 127.0.0.1:18188
```

用户打开 `http://127.0.0.1:18188/ui/` 可验证真实空Core协议；fixture场景使用上面的Node host。没有把fixture transport挂到正式Core或Desktop。Desktop验收应在单独的用户测试实例/配置中完成；本轮不更换正式binary、配置、live SQLite或重启正式服务。

回退预览：各自终端Ctrl+C，关闭预览窗口。正式服务没有改变，无需数据库回滚。工作树改动仍可通过Git diff逐项审阅；本轮未自动丢弃或回退任何文件。

## 真实WebUI/Desktop手动验收清单

- [ ] 五个主入口、资料/诊断二级入口；旧保存导航迁移后没有幽灵主入口。
- [ ] 每个Repository分别显示开发分支、current ref HEAD、selectedRevision、checkout；设置scope清楚。
- [ ] 总览待审批/待验收/runtime请求各自准确进入Task；Inspection进入诊断。
- [ ] 领域含多个Task，进行中/全部/已完成与分页有效；关闭只在领域菜单；集成只在交付区。
- [ ] Task固定头部/阶段在实际滚动中可读；无当前事项重复区块，横向审批与 Run 列表在主视图中可见。
- [ ] 当前Review审批、FAST继续DEEP、历史Review只读、基础设施失败不中断Task语义。
- [ ] 多revision/Execution、REQUEST_CHANGES、verification-only、Run成功但验证失败均不误报验收完成。
- [ ] 已验收但提交失败/未集成单独展示；真实运行停止状态不由transport终态推断。
- [ ] Run五标签，冻结模型/推理、token0/不可用、历史prompt、证据和诊断身份一致。
- [ ] Task→Run→Back保持tab/filter/expanded/scroll/draft；深链接重载验证owner。
- [ ] 快速切Task/Run/Project、Back、X/backdrop、隐藏窗口后无迟到覆盖/永久Loading。
- [ ] background不抢焦点/清草稿；interactive局部阻断；stale能力不可提交。
- [ ] mutation已提交但回读失败仅重试读取；失效草稿可读但不可提交到新Decision。
- [ ] Esc先关闭操作弹层，随后Run→Task→关闭；外部点击不穿透；焦点可见且关闭后返回。
- [ ] 明/暗主题、中/英文、长标题/长ID/大输出、窄窗口及Desktop iframe实际布局。
- [ ] Desktop使用同一Core `/ui/`，没有第二套Task/Run业务页面；正式环境能力由用户另行验收。

## 旧Runs专项复用与未执行业务

复用了已有shared Task progress/usage、Review/Execution/current指针、Task-owned drawer、work-refresh owner协调、RunSummary/RunDetail、persisted prompt、bounded evidence/runtime和只读诊断inventory。原Task详情只把运行痕迹列在diagnostics，尚没有本轮四/五标签和完整Task Run分页/返回；本轮直接扩展这些生产实现，没有复制另一套Runs系统。

仓库中此前提出的真实“Task Runs列表与Run Detail”业务Task未被推进：没有读取其业务状态来猜审批目标、没有批准Review/权限、没有启动Execution/Agent，也没有取消或恢复它。本轮源码完成状态不代表那个真实Task已验收。


## 追加调整：结论单行预览

按用户最新要求，结论正文最多显示 1 行，旁边点击“查看全部”打开既有只读结果弹窗。覆盖 Review 结果与阻塞原因、Task 当前结论及验证报告、Execution 记录、Run 结果和证据、Agent message，以及总览待处理结论。长内容预览最多 80 个 Unicode 字符，并按可用宽度省略；弹窗保留原文换行、特殊字符和完整返回内容。Review 继续通过精确 Review ID 按需读取完整结果。

验证：105/105 前端测试通过，bridgehttp Go 测试通过，候选 Core 已重新构建。测试包含生产脚本启动、页面/Task 内打开弹窗、Esc/遮罩关闭、焦点返回、重绘保留正文、过期 Review 读取不能覆盖新弹窗。未启动浏览器或正式服务，实际视觉验收仍待用户完成。

证据：[追加前端测试](archive/webui-task-centric-workbench.md#file-83a1ce326b128a59)、[追加 Go 测试](archive/webui-task-centric-workbench.md#file-423f3893518a8835)、[追加候选构建](archive/webui-task-centric-workbench.md#file-03e496687af165bb)。候选二进制及源文件身份已更新至本次调整。


## 追加调整：领域与 Task / Execution 详情层级

- 领域详情移除 Current Task 区块；创建任务等现有能力保留。领域内的任务表不再重复领域列，项目任务表仍显示领域列。
- Task 详情移除 Current Work，按任务进度、审批与任务操作、唯一 Execution 列表排列；资料、审查和 Run 清单为补充标签。旧 executions 深链接仍能打开主视图中的 Execution 列表。
- 顶部黄色空条来自读取完成后未隐藏的状态容器。成功状态隐藏容器，加载使用中性样式，真实读取失败保留明确告警及原快照。
- 每个 Execution 的实施、验证 Run 分别提供“查看 Prompt”“查看 Result”“查看 Token 消耗”。入口绑定精确 Run ID，Prompt 读取持久化内容；Token 按实际状态和计数展示，返回保留 Task 展开状态。结论继续使用单行预览与完整结果弹窗。

验证：108/108 前端测试、bridgehttp Go 测试和候选构建通过。没有启动浏览器或操作正式服务。证据：[前端测试](archive/webui-task-centric-workbench.md#file-6b9895ebdd511635)、[Go 测试](archive/webui-task-centric-workbench.md#file-20aea96be53ad287)、[候选构建](archive/webui-task-centric-workbench.md#file-2c9c80b253c9cfa0)。


## 最新调整：Task 直接展示 Run 列表

按最新要求，Task 保留进度与审批操作；操作横向排列并允许窄屏换行。移除 Execution 区块和概览、审查与方案、运行记录标签栏，直接展示唯一的运行记录列表。每条 Run 提供“查看 Prompt”和“查看结果”按钮，跳转精确 Run 的持久化 Prompt 或结果/证据；Token 详情仍可在 Run 中读取。Task 和当前 Run 分页一起刷新，保留页码、筛选以及返回时的草稿和滚动状态。旧 Task 标签深链接仍可进入同一 Task 的运行列表。

验证：109/109 前端测试、bridgehttp Go 回归及候选构建通过。没有启动浏览器或正式服务。证据：[前端](archive/webui-task-centric-workbench.md#file-ff05200a1febfbe9)、[Go](archive/webui-task-centric-workbench.md#file-619995f55ead39a3)、[候选构建](archive/webui-task-centric-workbench.md#file-325067da77c2350e)。本节覆盖此前关于 Task 的 Execution 列表和补充标签的布局说明。


## 提交说明

用户在实现及追加调整完成后明确要求提交本次修改并清理工作区。本报告此前的 Git 状态、源文件身份及“未提交”描述是验证时的提交前快照。生产代码、测试、隔离预览 fixture 与本报告引用的验证证据一并保留提交；未执行推送或部署。
