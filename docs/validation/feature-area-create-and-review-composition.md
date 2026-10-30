# FeatureArea 创建与首次 Task-owned Review 组合验证

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-c57d1d315a11e08f)。

FEATURE_AREA_CREATE_AND_REVIEW_IMPLEMENTED

MANUAL_GPT_ORCHESTRATION_ACCEPTANCE_PENDING

## 基线与边界

Starting HEAD：`1a61382db29fd0725b84da94d697e8ce0d2af230`；分支 `dev_20260908_1913`；仓库 `/Users/phoenix/Documents/devbridge`。
本轮没有 commit/merge/push、清理或覆盖用户工作、部署、正式数据库迁移、真实 Agent 调用或 UI 验收。所有新领域/Task/Review/Run 均位于测试临时数据库。

进入本轮时已有 Pending Decisions scope 隔离修改：`docs/interfaces.md` 的相关段落、`dev_bridge_ui.html`、`dev_bridge_ui_test.js`、`dev_bridge_collapsible_test.js`、`workflowpresentation/workspace.go`，以及 pending scope 两份测试和验证文档/日志。本轮保留这些工作；它们不是组合功能的新增 UI 修改。最终工作树仍为未提交状态，文件快照见本报告同名目录的 `working-tree.txt`。

## 实际接口

完整 tools/list descriptor（包括严格 inputSchema/outputSchema、动态模型/推理强度枚举及 annotations）见 [实际 schema](archive/feature-area-create-and-review-composition.md#file-bc1a246f44a8d98a)。Task intent 和 Review plan schema 使用独立工具同一 helper；应用层也复用 Task intent、Review instruction 和 focus plan 校验。

- 无 mode 等价 CREATE_ONLY。保留 `result.id` 为 FeatureArea ID、FeatureArea 自身 status。旧调用无 requestId 不承诺幂等。
- CREATE_ONLY 拒绝 initialTask/initialReview；有 requestId 时通过 durable receipt 返回原领域。
- CREATE_AND_REVIEW 必须显式提供稳定 requestId、initialTask、initialReview。静态输入、Session/Project、Repository、模型、深度、focus、开发分支等在领域写入前校验。
- 初始 Task 是正式可见 Task。模型必须显式提供；可选 reasoningEffort 按现有默认规则解析后冻结。Review instruction 原始 bytes 保留，不拼接 GPT 编排规则。
- 两种模式保持 SILENT；工具调用不等待 Review 输出，也不响应审批。context/workflow 从实际最精确 owner 读取，post-action readback 失败保留提交身份并标记 refreshRequired。

实际 SDK 调用及完整 envelope 样例：

### 旧输入 CREATE_ONLY

[完整实际响应](archive/feature-area-create-and-review-composition.md#file-9d68b7506a7db14e)。

调用：

```json
{
  "aiSessionId": "ai_session_c65aabfc22dc0813cdcb2982f8d74186",
  "problemStatement": "Do not investigate yet.",
  "title": "Only record"
}
```

```json
{
  "createdAt": "2026-09-13T03:49:18.655435Z",
  "id": "feature_area_642aa43799895e2c49d500d46e7791ae",
  "problemStatement": "Do not investigate yet.",
  "projectId": "project_394e81a4bfccdcdb2809efc3659d9d40",
  "status": "DISCOVERING",
  "title": "Only record",
  "updatedAt": "2026-09-13T03:49:18.655435Z"
}
```

### 正常 CREATE_AND_REVIEW

[完整实际响应](archive/feature-area-create-and-review-composition.md#file-c87eba13a439a825)。

调用：

```json
{
  "aiSessionId": "ai_session_c65aabfc22dc0813cdcb2982f8d74186",
  "title": "SDK startup",
  "problemStatement": "Inspect startup without modification.",
  "mode": "CREATE_AND_REVIEW",
  "requestId": "sdk-normal",
  "initialTask": {
    "title": "Inspect startup",
    "problemStatement": "Inspect startup without modification.",
    "constraints": [
      "Read-only"
    ],
    "repositoryId": "startup-wire-repository"
  },
  "initialReview": {
    "reviewDepth": "DEEP",
    "reviewPlan": {
      "mode": "REPOSITORY"
    },
    "reviewInstruction": "  Inspect exact source.\nPreserve authored whitespace.\n",
    "model": "gpt-5.6-luna"
  }
}
```

```json
{
  "completedSteps": [
    "FEATURE_AREA_CREATED",
    "TASK_CREATED",
    "REVIEW_ADMITTED"
  ],
  "featureAreaId": "feature_area_e28804ffd9b3a00b8546a44d21faf339",
  "mode": "CREATE_AND_REVIEW",
  "refreshRequired": false,
  "requestId": "sdk-normal",
  "retryable": false,
  "reviewAdmitted": true,
  "reviewId": "review_cf3c34105f81df8c50dae2f56f84f863",
  "runId": "run_2575d9f8177db477e885103f6a7e9e6a",
  "runStatus": "CREATED",
  "taskId": "task_d084db983875f73031ed45e4b3e58ac8"
}
```

### Task 创建前故障：保留领域

[完整实际响应](archive/feature-area-create-and-review-composition.md#file-5d4e1c920d2a96aa)。

```json
{
  "completedSteps": [
    "FEATURE_AREA_CREATED"
  ],
  "error": {
    "code": "INTERNAL_ERROR",
    "message": "INTERNAL_ERROR: internal control-plane error"
  },
  "failedStep": "TASK_CREATE",
  "featureAreaId": "feature_area_e0473dd7c06ed6d4c5945ecfa4daa16a",
  "mode": "CREATE_AND_REVIEW",
  "refreshRequired": false,
  "requestId": "sdk-partial",
  "retry": {
    "action": "RETRY_SAME_REQUEST",
    "requestId": "sdk-partial"
  },
  "retryable": true,
  "reviewAdmitted": false
}
```

### 同请求恢复未完成步骤

[完整实际响应](archive/feature-area-create-and-review-composition.md#file-8f11ca2b82166e93)。

```json
{
  "completedSteps": [
    "FEATURE_AREA_CREATED",
    "TASK_CREATED",
    "REVIEW_ADMITTED"
  ],
  "featureAreaId": "feature_area_e0473dd7c06ed6d4c5945ecfa4daa16a",
  "mode": "CREATE_AND_REVIEW",
  "refreshRequired": false,
  "requestId": "sdk-partial",
  "retryable": false,
  "reviewAdmitted": true,
  "reviewId": "review_79a2a0e3dd8c23b7551ca889dc24a9b3",
  "runId": "run_80a534eacd12f75ba8761fc69de3384b",
  "runStatus": "CREATED",
  "taskId": "task_881abd07ca0a08d72369a6a306be7c7a"
}
```

### 模拟丢失客户端响应后重放

[完整实际响应](archive/feature-area-create-and-review-composition.md#file-15b6e290f23d9109)。

```json
{
  "completedSteps": [
    "FEATURE_AREA_CREATED",
    "TASK_CREATED",
    "REVIEW_ADMITTED"
  ],
  "featureAreaId": "feature_area_e28804ffd9b3a00b8546a44d21faf339",
  "mode": "CREATE_AND_REVIEW",
  "refreshRequired": false,
  "requestId": "sdk-normal",
  "retryable": false,
  "reviewAdmitted": true,
  "reviewId": "review_cf3c34105f81df8c50dae2f56f84f863",
  "runId": "run_2575d9f8177db477e885103f6a7e9e6a",
  "runStatus": "CREATED",
  "taskId": "task_d084db983875f73031ed45e4b3e58ac8"
}
```

这些样例中的 CREATED 是实际数据库观测值：fake runtime 不伪造 RUNNING。Review admission 成功并不表示外部 Agent 已确认启动、Review 完成或审批通过。

## 共享 Core 与持久化边界

`feature_area_create → TopicCreateCore → StaticService → Facade.CreateFeatureAreaWithInitialReview`；Local MCP 复用相同契约。Core 预校验并冻结输入后，调用 `FeatureAreas.CreateReceipted`，随后 canonical `CreatePlanningTaskForSession`，最后 `startAdmittedReview` / `ScopeReviews.StartAdmitted`。现有 Review admission 负责 Review/Run reservation、current pointer 和 FeatureArea claim；现有 dispatch/reconciliation 继续负责外部运行。

根键作用域是 Session + Project + command + requestId。版本化指纹保留工程文本精确值，规范化缺省 mode 与空集合。receipt 保存冻结的 Task/Review 参数、配置、模型/推理强度、分支/基线/focus，以及领域/子步骤身份。Task/Review 子请求键由根作用域、版本和步骤 SHA-256 稳定派生。

Schema 47 新增 `feature_area_creation_receipts`，必要性是关闭领域落库与根 receipt 落库间的崩溃窗口；这两项使用同一个 SQLite writer transaction。Task 与 Review 各自保留 canonical 事务边界；canonical busy admission 拒绝会固定在 child request 上，返回不可同键重试，不能自动换键绕过；根步骤记录丢失时通过 canonical child receipt / Review StartRequestID 找回原对象。receipt 身份单调写入，不允许替换已关联对象。SQLite reopen、并发相同请求、SQL trigger 故障回滚、已有领域兼容测试均覆盖。迁移只在临时测试库运行，没有升级正式数据库。

已接纳的根请求先找回历史身份，首次 Review 后续 SUCCEEDED/FAILED/CANCELED 也不会创建第二轮。未完成步骤恢复重新校验当前生命周期、模型/config、分支和 focus 安全前提；关闭领域或取消 Task 后不创建替代对象。后续新一轮 Review 仍需独立 task_review_start。

数据库事务不能提供外部 exactly-once。dispatch 前后错误保留原 Review/Run，要求 REFRESH_RESULT，不重复派发；原 Run 的既有恢复机制处理不确定状态。UNKNOWN 不被映射成“未启动，可另建 Run”；wire 只输出 canonical Run status，其他不确定性由 failedStep/error/refreshRequired 表达。GET、poll、展示和重启没有调用组合续做入口。

## 自动验证证据

- 真实临时 SQLite + 实际 SDK tools/list/tools/call + fake runtime：正常只增加 1 FeatureArea、1 Task、1 Review、1 linked Run；canonical Task current pointer、Run SCOPE_REVIEW/REVIEW ownership、FeatureArea claim 一致；没有新增 Execution 或 TaskDefinitionRevision。
- 同根并发收敛；不同 raw prompt/参数复用根键明确冲突；原始指令（含首尾空白和换行）保持 byte identity。
- 故障注入 9 个边界：领域已提交响应丢失、Task 已提交响应丢失、Task 根回执更新失败、Review 根回执更新失败、admission 响应丢失、dispatch 前失败、dispatch 后响应丢失、最终响应丢失、Run readback 失败。重新构造 Facade 并重放，不替换对象、不重复已尝试的 fake handoff；只在此前尚未到达派发步骤的 Task receipt 故障中首次派发。
- 终态后分支 HEAD 移动仍重放原 IDs；完整 receipt 在模型禁用后仍可读取，不完整 receipt 不绕过模型禁用；关闭领域和取消 Task 不继续创建。
- GPT model-visible 30、app-only 10、Local 27 工具契约测试通过。没有新增工具或旧 alias。SILENT、bootstrap diagnostics flat、runtime RawMessage/schema、独立 Task/Review、技术只读与审批边界由现有全量回归继续覆盖。

## GPT 连续编排与人工验收

修改位置：`internal/mcpserver/external_workflow.go` 的 ExternalServerInstructions、创建 descriptions；`internal/localmcp/schemas.go`；`docs/interfaces.md`。A-E 场景区分明确授权创建并调查、仅登记、现有领域、现有 Task、审批/验收门槛。机械启动步骤不重复确认；必填参数/授权不足报告阻断；nextAction 是指引而非宿主执行授权；完成或阻断后集中 workspace_show。Coding Agent 工程 prompt 不注入这些规则。

[真实 GPT 手动案例](feature-area-create-and-review-manual.md) 七项均 **NOT_RUN**。未操作浏览器/Desktop/真实现有领域。自动协议测试不能证明真实 GPT 宿主以后绝不提前停止。

## 本轮修改文件

- 共享入口与恢复：`internal/controlapi/api.go`、新增 `feature_area_startup.go`、`feature_area_startup_service.go`、`review_plan.go`；应用集成和故障测试为 `feature_area_startup_test.go`、`feature_area_startup_fault_test.go`。
- Receipt：`internal/featurearea/{featurearea.go,service.go,memory_store.go,service_test.go}`；新增 SQLite `feature_area_creation_receipt.go`、`feature_area_creation_receipt_migration.go`、`feature_area_creation_receipt_test.go`；更新 `migrate.go` 和 schema version 两份断言。
- 共享验证：`internal/task/service.go`、`internal/scopereview/service.go`。
- MCP：`external_workflow.go`、`adapter_core.go`、`service.go`、`surface_envelope.go`，对应 contract/instruction/schema/core 测试，以及放在协议层的真实 SQLite SDK `feature_area_startup_wire_test.go`。
- Local：`internal/localmcp/schemas.go`、`contract_test.go`；文档：`docs/interfaces.md`、本报告及证据目录、手动评估清单。

完整路径列表见 `working-tree.txt`（包括明确保留的上轮 dirty 工作）。

## 验证命令与结果

- `go test ./internal/controlapi -run TestFeatureAreaStartup -count=1 -v`：PASS，见 [逐项应用/故障测试日志](archive/feature-area-create-and-review-composition.md#file-80c49acd4c98cfa6)。
- `go test -race ./internal/controlapi ./internal/storage/sqlite -run 'TestFeatureAreaStartupCompositionRejectsInvalidModelBeforeAreaAndConvergesConcurrentRoot|TestFeatureAreaCreationReceiptConcurrentSameRequestReturnsOneArea' -count=1`：PASS，见 [race 日志](archive/feature-area-create-and-review-composition.md#file-01dfd47c230e407f)。
- 实际 SDK wire + `TestDomainPackagesDoNotDependOnMCP` 聚焦验证：PASS。SDK 测试归属 `internal/mcpserver`，保持应用层不依赖 MCP 的架构规则。
- `node --test internal/mcpserver/*test.js`：707 total / 703 PASS / 4 SKIP / 0 FAIL，见 [Node 日志](archive/feature-area-create-and-review-composition.md#file-8c2f102ca2fb1ca5)。四项 candidate Card 集成 fixture 未提供，因此跳过，未以此宣称 UI 验收。
- `git diff --check`：PASS。

隔离测试显式清空 `DEVBRIDGE_REAL_CODEX` / `DEVBRIDGE_REAL_REVIEW_SMOKE`；真实 Coding Agent、正式部署/迁移和真实 GPT 宿主验收均未运行。

最终全量检查：`go test ./...` **PASS**，见 [Go 全量日志](archive/feature-area-create-and-review-composition.md#file-19b33bee744eb67f)；`go build ./...`、`go vet ./...` 均 **PASS（exit 0，无输出）**，见 [build/vet 记录](archive/feature-area-create-and-review-composition.md#file-4bb24bfbca3cdb86)。

最终 HEAD 与 Starting HEAD 相同。工作树含本轮代码/测试/文档以及保留的上轮改动，没有提交或清理。自动化完成状态不包含真实 GPT 宿主验收。
