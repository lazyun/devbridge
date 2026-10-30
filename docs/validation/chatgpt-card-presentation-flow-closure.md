# ChatGPT 卡片展示流程交付报告

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-3b0629e67528dfdf)。

`CHATGPT_CARD_PRESENTATION_FLOW_IMPLEMENTED`

`MANUAL_HOST_ACCEPTANCE_PENDING`

基线：repository root `/Users/phoenix/Documents/devbridge`；branch `dev_20260908_1913`；Starting HEAD `63ff7b458c7388442dbcd95cbc593d538f8be89d`；开始时 working tree 干净。实现与验证阶段未执行 commit；交付后用户明确授权提交本次修改并清理工作区。未执行 merge/push、发布、部署、正式配置/SQLite/服务操作或浏览器、Desktop、设备 UI 验收。提交前的文件明细与工作树快照见 [working-tree.txt](archive/chatgpt-card-presentation-flow-closure.md#file-14343416359f4f0f)。

## 保留与修改

保留现有 30 个 GPT model-visible 工具、10 个 app-only 工具、27 个 Local MCP 工具及 canonical Core；不新增展示入口、业务阶段、迁移或业务 registry。严格 Session、Decision 与管理 capability 分离、ownership/revision/epoch/cancellation/recovery、Prompt Neutrality、diagnostics flat bootstrap、Desktop identity readiness 和 runtime object payload 契约保持有效。未修改 Console/Desktop 的 Task→Runs→Run Detail 工作。

本轮完成：

- 全局 instructions 开头统一用户进度直接 show、内部读取静默、业务链末尾一次展示、卡内原位操作，以及 text/no-UI 场景；同步工具描述、Review follow-up 和 WAKE 语义。
- 唯一 `SurfacePresentationHint` 增加 required、闭合 reason、受限 tool/display、typed exact arguments、UNCONFIRMED；SDK reads/show/mutation envelope 使用同一 schema。普通 RUNNING/RETIRE/CANCEL 不产生审批要求，Task/operation 范围之外的决策不污染 hint。
- `workspace_show` 直接复用服务 reader，紧凑模型摘要与 `_meta.workspace` 来自同次读取；保留 phase、nextAction、blocker、独立分区错误和已有一致性信息。show 返回 `required=false / PRESENTATION_RETURNED`，不递归要求 show。完整方案/证据仍由 `operation_get` 按需读取。
- 展示重试保存意图和首次解析出的范围，每次重新读取业务事实；相同请求不同参数冲突，不缓存旧审批授权，不用全会话 ALREADY_SHOWN 阻止新回复入口。
- 组件复用有效的同次快照；缺失、超过 30 秒或分区失败时精确交互式回读。初始化等待有界，超时后有效晚到数据可恢复；真实身份冲突仍失败关闭。上下文传播始终保留 Session/Project，传播失败不重复 project_open。
- 历史决策使用准确 receipt/source：有效 receipt 表示已处理；同一权威 Task 显式更换 CurrentReviewID/CurrentExecutionID 表示已被替代；只有 pending 列表缺失时保持“当前不再待处理，具体结果不可用”。Runtime Approval 读取自身 durable status；Codex Request 不伪造 receipt。已处理的 runtime 入口可回读，原目标消失时不开放替代请求的控件。
- `details_show` 保持只读；准确 operation summary 可随同次结果放入 `_meta.operationDetails`，无 operation target 的 Task/FeatureArea 详情也可复用首次快照。
- 保留 background/interactive loading、draft/focus preservation（按准确 Decision/Execution/Run/运行时请求隔离）、generation fencing、超时和卸载清理、mutation success/readback failure 分离。折叠态对准确 operation 每 15 秒最多 12 次 `operation_get(include:[])`，完成后一次工作区回读；展开态只对实际运行目标使用原背景刷新，pending/terminal 停止不必要读取。
- 完成事件保留原位展开状态；观察到的折叠态完成也可请求 follow-up，复用服务端有界事件去重。runId 校验不造成旧/新组件重复去重 key。context ACK 与 follow-up 请求分离；context 失败不自动补偿长用户消息，保留手动解读。任何 ACK 都不声称 GPT 已执行。
- Local adapter 保留 TEXT 输出，移除 ChatGPT 展示 tool/arguments 映射，并同步实际 tools/list OutputSchema；共享业务 schema 仍逐字段校验。

## 场景 → 工具 → 展示

| 场景 | 工具序列 | 展示方式 |
| --- | --- | --- |
| 已绑定准确 Task，查询当前进度 | `workspace_show` | 一次 COLLAPSED；不机械先 get |
| 明确打开工作区 | `workspace_show(display=EXPANDED)` | 一次展开入口 |
| 首次项目选择 | `project_selection_show` → 用户选择 → `project_open` | 同组件进入工作区；文字已明确选择可直接 open |
| 开发需求并启动 Review | 必要静默读取 → 创建/复用准确 FeatureArea → `task_create` → `task_review_start` → `workspace_show` | 最后一次 COLLAPSED |
| Review/Verification 解读 | `operation_get` → 必要的准确当前 Decision 核验 → `workspace_show` | 准确审批/验收入口默认收起；解释后等待选择 |
| 消息中明确批准/取消 | 核验准确目标 → canonical mutation → 展示结果 | 最后一次；展示不构成授权 |
| 卡内按钮 | canonical mutation → 精确 readback | 原位更新，无 show/自然语言重猜目标 |
| 展开/刷新/导航 | 组件内准确读取 | INTERACTIVE Loading，无新入口 |
| 自动监测/刷新 | bounded operation read 或运行中 background read | 不全卡 Loading，不抢焦点 |
| 历史审批/详情 | 准确历史 identity 读取 | 原始来源固定；缺少历史证据明确未知 |
| 架构/接口/prompt 讨论 | 必要时静默读取 | 不自动插入无关审批卡 |
| 只用文字/无 UI 客户端 | canonical 静默工具与文字 | 不强制 UI |

`required=false` 不禁止用户主动展示；`required=true` 是准确交互事实及上层调用约定，不是宿主原生渲染开关。调用方为本次展示生成 `presentationRequestId`；hint 不发明宿主 turn ID。服务端不承诺宿主 exactly-once、历史卡替换或模型必定调用正确工具。

## 实际返回样例

均为隔离 fixture 的真实 SDK `tools/call` 返回，不是真实项目或用户审批：

- [pending-read-fixture-response.json](archive/chatgpt-card-presentation-flow-closure.md#file-ab9da33d9d2a8531)：静默读取产生 `PENDING_USER_DECISION`，包含真实 fixture Session/Project/Task/Decision 参数；参数加上调用方生成的 requestId 可直接 show。
- [workspace-show-fixture-response.json](archive/chatgpt-card-presentation-flow-closure.md#file-14d659c2bfada57e)：模型摘要、同次 `_meta.workspace`、同一 scope，以及 `PRESENTATION_RETURNED / required=false / UNCONFIRMED`。
- [catalog-summary.json](archive/chatgpt-card-presentation-flow-closure.md#file-d6c3e11772829430)：由最终实际 SDK tools/list 提取的目录与 UI 资源绑定。

## 自动验证实绩

| 检查 | 结果 / 证据 |
| --- | --- |
| 全仓库 Go test | PASS：59 个测试包通过、14 个包无测试；[日志](archive/chatgpt-card-presentation-flow-closure.md#file-6b39fe0884db89e5) |
| Go build / vet | 均 PASS，exit 0；[build](archive/chatgpt-card-presentation-flow-closure.md#file-c6fc538c21c27452)、[vet](archive/chatgpt-card-presentation-flow-closure.md#file-56422820f9bb68cb) |
| 实际 initialize/tools/list | PASS；全局 instructions 契约、30/10/27 分类、仅三个 show descriptor 关联一致的 resourceUri/outputTemplate；[目录测试](archive/chatgpt-card-presentation-flow-closure.md#file-1ba7924f445fe1f0) |
| 实际 tools/call 与 schema | PASS；hint→show、同次 scope、模型及组件 schema、未知 hint 枚举拒绝、runtime object payload、静默 adapters；[wire 日志](archive/chatgpt-card-presentation-flow-closure.md#file-8a09f5c50c8bed17) |
| JS 组件/wakeup | Node runner 220/220 PASS，0 skipped；[日志](archive/chatgpt-card-presentation-flow-closure.md#file-35d23db2f6e3a46a) |
| Desktop bootstrap / identity | PASS；实际临时 GPT/Local flat wire response 经 Desktop decoder 验证，严格 readiness 保留；[日志](archive/chatgpt-card-presentation-flow-closure.md#file-bd12434e6331c48f) |
| 生产组件隔离预览 | `--check` PASS，六个 fixture 场景；[日志](archive/chatgpt-card-presentation-flow-closure.md#file-2c85c25827e62418) |
| 最终组件集成 | 最终工作树的 MCP 全包测试通过，组件草稿身份及回读参数由 220 项 Node runner 和最终 build/vet 验证；[MCP 日志](archive/chatgpt-card-presentation-flow-closure.md#file-37ab188ba755a749) |
| diff hygiene | `git diff --check` PASS |

首轮全量的唯一失败是既有测试要求 Local/GPT **展示** schema 完全相等，与本轮明确的 Local TEXT 映射变化冲突。测试已改为：共享业务字段逐字段相等、Local 展示 schema 明确 TEXT、实际响应继续做 schema validation；随后全量通过。未删除测试、吞错误或关闭 validation。[首轮日志](archive/chatgpt-card-presentation-flow-closure.md#file-471f5e70ca7bc36b)。初次沙箱内 loopback 测试遇到 bind 限制，之后通过获准的隔离测试运行完成，未转用正式服务。

复现命令（从仓库根目录运行）：

```sh
DEVBRIDGE_REAL_CODEX= DEVBRIDGE_REAL_REVIEW_SMOKE= go test ./... -count=1
go build ./...
go vet ./...
node --test internal/mcpserver/dev_bridge_ui_test.js internal/mcpserver/dev_bridge_review_wakeup_test.js
node scripts/dev-bridge-card-preview.cjs --check
cd desktop
go test ./internal/runtime -run 'Test(DiagnosticBuild|CoreReadinessFailure|HTTPProber.*Readiness)' -count=1
```

真实 Coding Agent smoke 明确禁用；依赖外部 candidate binary 配置的专用测试未配置，不把它们计为实际 candidate 服务验证。未做 Desktop 全产品构建或 UI 验收。真实 ChatGPT 的工具选择、宿主挂载、follow-up 执行和实际 Session 复用尚未验证。

## 隔离预览、候选接入与回退

使用现有入口，直接服务生产 `internal/mcpserver/dev_bridge_ui.html`，不复制假 UI。预览 transport 仅提供标识明确的 fixture，业务 mutation 被拒绝：

```sh
cd /Users/phoenix/Documents/devbridge
node scripts/dev-bridge-card-preview.cjs --port 0
```

启动后只会输出随机 loopback URL；本轮未打开该 URL，也未启动浏览器。手动验收阶段由操作者打开，停止时 Ctrl+C。刷新失败场景从同次有效快照开始，首个组件回读即失败，符合新的首屏复用流程。

候选接入：保留本次未提交 diff，按正常审查/构建流程产出候选；选择临时 data root、独立端口和测试身份后接入测试 ChatGPT connector，再执行 [12 项手动评估](chatgpt-card-presentation-manual-evaluation.md)。不得替换正式服务或借用正式 Session。回退时停止候选与测试 connector，恢复先前制品/接入配置；没有本轮业务 migration 要回滚，不应通过重做批准/取消或改 SQLite 修复展示。

全部真实宿主场景为 **NOT_RUN**。逐项记录实际工具序列、展示次数、目标准确性、Session 复用、漏/误展示、宿主挂载，以及 context ACK/follow-up request/实际 GPT 响应的区别。自动测试不能保证 GPT 不漏卡，本报告不标记 GLOBAL_READY。
