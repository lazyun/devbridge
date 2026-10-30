# GPT Surface Consolidation and Collapsible Card V1 候选交付

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-6e5bc1d79eaf2a30)。

状态：`IMPLEMENTED_FOR_MANUAL_HOST_PREVIEW` / `MANUAL_HOST_ACCEPTANCE_PENDING`。

## 基线与修改范围

- Repository：`/Users/phoenix/Documents/devbridge`
- Branch：`dev_20260908_1913`
- Starting HEAD：`ab371137e66dac9e6dd3d8af0f66cc61230f6621`；本轮未创建提交，HEAD 保持不变。
- 开始时工作区干净。最终共 122 个修改文件（104 个既有文件、18 个新增文件），其中 36 个是 README/文档；完整路径见交付的文件清单。生产修改集中在 `internal/mcpserver`、`internal/localmcp`、`internal/controlapi`、`internal/workflowpresentation`，及 `internal/storage/sqlite` 中复用现有取消 guard 的只读 eligibility seam，以及对应测试、预览脚本与文档。
- 先冻结[共享接口及 owner](../architecture/gpt-surface-consolidation-v1.md)，再按注册/schema、生产 Card、读取投影分工并行实施。共享文件各有单一 owner，Go 构建串行使用同一临时 cache。
- 复用既有 Core/事务；无新增业务数据库迁移、Session registry 或审批/取消/recovery 状态机。
- 未修改正式 runtime/live SQLite，未启动或重启正式服务，未处理既有 Task/Decision，未运行真实 Coding Agent。未启动浏览器、Desktop、模拟器、设备或进行 UI 截图；未 commit、merge、push、发布或部署。

## 实际接口与兼容边界

实际 SDK `tools/list` 契约检查：GPT model-visible **30**，app-only **10**；Local MCP **27** 个共享业务接口，无 show 工具、UI resource 或 ChatGPT template。30 个完整名称及 old→new 映射见[冻结目录](../architecture/gpt-surface-consolidation-v1.md#old-to-new-mapping)和[当前接口文档](../interfaces.md)。Starting HEAD 的源码目录列出 46 个 GPT 模型接口；这是基线源码计数，并非本轮对旧服务进行实测。

保留的 10 个 app-only 接口为：`ui_workflow_state_get`、`ui_approval_decide`、`ui_approval_get`、`development_context_presentation_get`、`feature_area_details_get`、`dev_bridge_card_get`、`dev_bridge_review_wakeup_claim`、`feature_area_presentation_get`、`task_presentation_get`、`development_workspace_get`。这些兼容读取未泄漏为模型别名；旧无参数严格卡片展示注册已移除。

GPT/Local 消费方需要迁移到新名称、typed 参数及统一 envelope；不承诺旧模型工具返回形状兼容。内部 Core 方法及既有 app 读取仍保留。旧 ScopeExpansion 已被 Core 拒绝为 `SCOPE_EXPANSION_LEGACY_ONLY`，本轮未删除可用业务路径。OPERATION 清单保留准确语义工作；未知 legacy provenance 通过同页 typed unavailableOperations 只读诊断分支保留原 Core projection 和来源，不捏造 target 或 capability，也不使其他正常行整页失败。既有内部详情路径仍存在。

生命周期、业务决策、runtime 请求和文件写入使用闭合互斥分支。TASK 创建与 Review 启动继续独立；首次 APPROVE 进入 IMPLEMENTATION_READY；START_VERIFICATION 调用现有 `execution_start` 验证保留路径。取消复制准确 capability，并保留无 Run 的合法 pre-work 边界，不增加 Inspection 取消能力。

## 读取和返回体

`workspace_get` 默认返回当前 Project/branch 摘要、准确选择、Task phase 和当前 decisions/actions，不默认加载历史或完整 Task 规格。`MODULES`、`BRANCHES`、`TASK`、`RUNTIME_CONFIG`、`NAVIGATION` 按需读取；包括原模块 production facts 和运行配置允许的模型/推理强度。`work_list` 区分语义 TASK 与 OPERATION，先准确 scope 再有界分页。`operation_get` 的 RESULT/EVIDENCE/ACTIVITY/RUNTIME_REQUESTS 保留 Review/Execution/Run/evidence/claim/telemetry 的来源区分。

读取分区保留 generation/sourceSequence、readError、stale，并明确非原子快照。命令已经成功但回读失败时，保留准确结果/receipt 并报告 refreshRequired。执行已保留 Proposal/Run 后派发失败时仍返回这些真实身份及 dispatchError，不能引导重新创建执行。Presentation request、业务 receipt、执行保留、文件操作、runtime 响应各自保持原重试边界。

代表性 fixture（同一条含 10,000 字节需求正文的 Task，UTF-8 JSON 实测）：

| 读取形态 | 字节数 |
|---|---:|
| 保留的原详细 Task DTO | 10,532 |
| 默认 workspace_get | 1,564 |
| workspace_get include TASK | 12,189 |

默认读取比这份原详细 Task DTO 小约 85.2%；按需完整读取保留正文。数据来自 `TestSurfaceRepresentativePayloadSizes`，日志 `/private/tmp/devbridge-surface-final-detail-regression.log`。这不是六份旧返回体之和，也不是实际 ChatGPT token 或宿主耗时测量；不同项目数据会改变结果。

展示结果的模型部分使用 `result/context/workflow/presentationHint`，完整生产组件初始化位于私有 MCP metadata。数据和业务结果 SILENT，只有三个明确 show 工具附带资源。相同 presentationRequestId/参数重试返回完整身份及 metadata，参数变化冲突；新请求可生成新入口。缓存仅在当前进程有效，容量有界且满时明确失败。服务端始终报告 hostVisibility=UNCONFIRMED，不把去重当成宿主挂载证据。

## 生产 Card 行为

普通 show 默认 40px 收起入口；GPT 对用户明确“展示/打开卡片”的意图传 EXPANDED。点击展开先读取准确 Session/Project/target，再在同一组件内展开，不发 GPT 消息、不新增 show、不创建业务对象。每个入口保留自己的选择和草稿；历史 Decision 已解决时显示准确历史状态，目标不匹配直接失败。

仅 project_selection_show 可选择项目。严格入口在输入/结果尚未到达时等待，有界超时后失败，不通过 widgetState 推断 Session。首次 project_open 的真实 Session 会在传播失败时保留，重试只传播模型上下文。危险操作要求新鲜读取；全组件 loading 覆盖 Header、业务区和弹层，处理并发、超时、旧响应与卸载。设置在 Header，Task 操作区显示一个准确取消入口；普通管理能力不伪装成 pending decision。

操作弹层统一处理外部点击、Esc 和焦点恢复，关闭不提交或穿透。折叠、隐藏时停止相应刷新；既有 Review 完成通知继续使用准确事件 claim/去重，不新增后台唤醒机制。不保证未挂载时通知，也不声称模型已消费上下文传播。

## 自动验证

- `GOCACHE=/private/tmp/devbridge-surface-go-cache go test ./...`：通过，退出码 0；日志 `/private/tmp/devbridge-surface-all-go-final.log`。既有 opt-in 测试按原环境条件跳过，详情如下。
- 最后补入的 TaskView 分区读取失败撤销权限已补测；Workspace/OperationGet/schema/实际 SDK 目录/完整展示重放专项通过，日志 `/private/tmp/devbridge-surface-final-detail-regression.log`。pre-work cancellation、无历史扫描、legacy 混合分页等新契约专项通过，日志 `/private/tmp/devbridge-surface-new-contract-final.log`。
- `GOCACHE=/private/tmp/devbridge-surface-go-cache go vet ./...`：通过，退出码 0；日志 `/private/tmp/devbridge-surface-vet.log`。
- `git diff --check`：通过。
- `node --test internal/mcpserver/*test.js`：436 次测试执行，432 通过、0 失败、4 跳过。测试文件之间复用 suite，执行数不是独立用例数。跳过项是原有 orphan transport / candidate snapshot / candidate Approval / candidate Review completion 外部 fixture 场景。日志：`/private/tmp/devbridge-card-ui-complete.log`。
- `node scripts/dev-bridge-card-preview.cjs --check`：六场景及同一生产组件 transport 检查通过，没有启动服务器。
- `node scripts/build-chatgpt-ui.mjs /private/tmp/devbridge-surface-ui-final`：2 个 self-contained 生产 UI asset 构建检查通过。
- 展示专项的真实 SDK 测试覆盖同 request 完整重放、参数冲突、跨回复新入口、历史 Decision owner、私有初始化数据与模型 envelope 分离；并发相同请求只执行一次读取。
- typed 组合分支、实际目录和 app visibility、Direct File 原约束、Task→Review→Execution→Verification→Acceptance、并发 receipt、准确 owner/revision、取消和 recovery 均复用真实适配器/隔离存储及 stub backend 的契约测试。
- 未执行真实 ChatGPT 宿主、浏览器、Desktop 或截图验收；未运行真实 Coding Agent 或 live E2E。
- 基线已有的三份 opt-in live-candidate Go 测试保持原样及默认跳过条件，未作为通过证据：`internal/localmcp/review_approval_v2_candidate_test.go`（DEVBRIDGE_REVIEW_APPROVAL_V2_BIN）、`web_observability_candidate_test.go`（DEVBRIDGE_WEB_OBSERVABILITY_BIN）、`unified_candidate_acceptance_test.go`（DEVBRIDGE_UNIFIED_CANDIDATE_BIN / DEVBRIDGE_RETIRE_CANDIDATE_BIN）。这些旧脚本仍依赖已过时的生命周期和读取接口；复用前需要另行迁移。本轮未为了通过测试删除它们或更改 skip 条件，新隔离预览使用本文的 fixture transport。

自动检查仅证明相应代码、SDK 契约和 fixture 交互逻辑；不证明真实 ChatGPT 挂载、消息位置、宿主尺寸处理、模型上下文投递或后续 GPT Session 复用。

## 手动预览与候选启动

### 只体验 Card 交互

以下命令由用户手动运行。本轮只执行了 `--check`，没有启动预览服务器或浏览器。

```sh
cd /Users/phoenix/Documents/devbridge
node scripts/dev-bridge-card-preview.cjs --port 0
```

手动打开终端打印的随机 loopback URL。页面明确标识 FIXTURE TRANSPORT，直接读取同一生产 `internal/mcpserver/dev_bridge_ui.html`，没有复制另一份 UI，也不连接 Core/数据库/Agent。可切换六个场景：收起空闲、收起待审批、展开运行中、展开待审批、刷新失败、Session 无效。刷新失败场景首次读取成功，随后刷新返回错误；业务按钮只记录请求并返回 fixture 错误。`Ctrl+C` 关闭。

### 手动连接隔离真实候选 Core

候选二进制已构建成功（退出码 0），存放于 `/private/tmp/devbridge-surface-candidate/`：`devbridge`、`devbridge-mcp`、`devbridge-local-mcp`。它们没有被启动；构建日志 `/private/tmp/devbridge-surface-build.log`。

若需要重新构建：

```sh
cd /Users/phoenix/Documents/devbridge
GOCACHE=/private/tmp/devbridge-surface-go-cache go build -o /private/tmp/devbridge-surface-candidate/ ./cmd/...
```

手动启动一个全新临时 data root 和随机端口，必须明确 `-mode server`：

```sh
candidate_data="$(mktemp -d /private/tmp/devbridge-surface-data.XXXXXX)"
/private/tmp/devbridge-surface-candidate/devbridge -mode server -data-root "$candidate_data" -listen 127.0.0.1:0 -timeout 1h
```

终端会打印 `DEVBRIDGE_READY http://127.0.0.1:<实际端口> ... dataRoot=...`。该隔离 Core 的 GPT MCP 路径为 `/_adapters/gpt/mcp`，Local MCP 路径为 `/_adapters/local/mcp`。用户在自己的宿主连接配置中使用这个候选 endpoint；若宿主需要可达地址，由用户单独配置，本轮未建立隧道或发布服务。不要替换正式 endpoint，也不要指向正式 data root。新 data root 为空，真实流程测试需用户明确建立独立测试 Project/repository 和新 Task，不借用或批准旧 Review。

如需要单独 proxy，在新终端手动运行对应候选二进制，`-core-url` 使用刚才打印的实际 Core URL，`-data-root` 使用同一个临时目录，`-listen 127.0.0.1:0`。`devbridge-mcp` / `devbridge-local-mcp` 会打印各自随机端口的 `/mcp` 地址。此步骤不是 fixture 预览的前置条件。

### 回退

停止本次手动启动的 preview/Core/proxy：各自终端 `Ctrl+C`。正式服务和正式数据本轮没有改变，宿主切回原先的连接配置即可。

完整未提交补丁位于 `/private/tmp/devbridge-gpt-surface-v1.patch`，文件清单位于 `/private/tmp/devbridge-gpt-surface-v1-files.txt`。回退预检查已通过，本轮未执行实际回退。若用户要撤销这次源代码候选，先检查，再执行逆向应用：

```sh
cd /Users/phoenix/Documents/devbridge
git apply --reverse --check /private/tmp/devbridge-gpt-surface-v1.patch
git apply --reverse /private/tmp/devbridge-gpt-surface-v1.patch
```

只在第一条检查成功时执行第二条。若之后有新修改导致检查失败，应先人工检查差异，不使用 reset/stash/clean。补丁和候选二进制在临时目录，长期保留前请自行复制到合适位置。

## 真实宿主待验收

- 首次项目选择只调用一次 project_open；故意让模型上下文传播失败后重试，验证 Session 未重建，后续 GPT 实际沿用返回的 aiSessionId。
- 普通回复是轻量收起入口；明确要求打开时展开；同轮多次读只请求一次 show，不同回复可有不同入口。
- 同 request 重试保持完整初始化，宿主是否重复挂载需实测。服务端去重不等于宿主原位替换。
- Enter/Space、焦点、展开 fresh read、收起后的实际 iframe 高度和宿主外部留白；不通过修改 parent DOM 模拟成功。
- 历史 Review/Acceptance/Operation 精确定位，已解决状态正确；另一个入口切换目标不使它漂移。错 Session/Project/target 不进入 picker、不展示可操作的旧授权。
- 当前 Task 只有一个准确取消入口，合法无 Run 取消与 Review/Execution 区分；ACK/UNKNOWN_ORPHANED 文案不宣称进程死亡。准确审批仍需用户明确决定。
- Header 设置、弹层外部点击/Esc/焦点恢复、内部输入、全卡 loading、并发/超时/失败后的重试与危险操作禁用；草稿保留。
- 展开/可见状态的读取次数、折叠历史入口不增加全量轮询；Review 完成通知准确去重且不会自动批准或启动执行。

以上宿主条目均为待验收；fixture 和单元测试不能替代它们。当前已运行的测试和构建无未处理失败。真实宿主行为及上面列明的旧 opt-in 脚本仍无验收证据。
