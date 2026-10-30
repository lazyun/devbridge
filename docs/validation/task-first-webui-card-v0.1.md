# TASK_FIRST_WEBUI_CARD_V0_1_READY

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-a6f13531a61d89dc)。

> Historical implementation evidence. [GPT surface consolidation V1](../architecture/gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

验收日期：2026-09-11。实现、测试和截图均在独立 UI worktree 内完成；没有 merge、push、release 或真实付费 Codex Turn。

## Checkpoint 与边界

| 项目 | 结果 |
|---|---|
| 原始 checkout | `/Users/phoenix/Documents/devbridge`，HEAD `6beb7fcbdd1363c3da36914fd5df7b53c90d7fe0`，clean、未修改 |
| Architecture branch | `codex/remove-task-preparation-v0-1` |
| Architecture worktree | `/private/tmp/devbridge-remove-task-preparation` |
| Architecture checkpoint | `be752da6d46f350b7bb5aed24e4dfdb8965231a0` |
| Architecture commit message | `refactor: simplify feature area task and workspace lifecycle` |
| Architecture 状态 | fresh 核验通过后提交全部 closure；提交后和最终核验均 clean |
| WebUI/Card branch | `codex/task-first-webui-card-v0-1` |
| WebUI/Card worktree | `/private/tmp/devbridge-task-first-webui-card`，正常独立 worktree |
| Starting HEAD | `be752da6d46f350b7bb5aed24e4dfdb8965231a0` |
| Final HEAD / working tree | 本报告所在 UI checkpoint；当时的最终 Git 回执位于 `/private/tmp/devbridge-task-first-webui-card-final-receipt.md`，未包含在公开仓库中，不能据此核验当前 HEAD 或 clean 状态。 |

开始前确认 final v42、324 schema objects、Preparation production residue 0、FeatureArea workspace ownership、多 Task continuation 集成测试及两份 READY 报告。保留原始架构报告：[Preparation removal](remove-task-preparation-v0.1.md)、[FeatureArea continuation](feature-area-continuation-v0.1.md)。UI 分支没有改动 final schema、FeatureArea/Task/Execution/Workspace/AgentConversation domain contract。

## 最终用户视图

Task List 一级列只有 **Task / Status / Time**，整行按精确 Task ID 打开详情。列表保留筛选，允许同一 FeatureArea 的多个终态 Task 和当前 Task 共存，不再展示 latest Run 的 Usage。

Task Detail 以 title/goal、status、单一 semantic progress、Codex activity、Goal Progress、Usage、Result 和当前 eligible action 为主。Verification 及错误/恢复说明为次级信息。TaskDefinitionRevision、Execution/Run/Workspace、thread/turn、model/reasoning、Git evidence、raw accounting 和 runtime diagnostics 放入 Details。历史/终态 Task 不借用当前工作流按钮。

FeatureArea 保留 ACTIVE 生命周期，空闲时明确显示 **当前无运行中的 Task**。Current Task 与 Latest Task 分别读取，最新终态 Task 的摘要不会被冒充为 current。Overview 提供符合 Core eligibility 的 Create next Task、Retire、Reset agent context；终态 Task 不关闭 FeatureArea。历史 Task 可继续打开。

Development 只有 Review → Approved → Implementation → optional Verification → Done；无 Verification 时跳过。Review/Inspection 使用最小适用 progression。Task、Execution、Run、Workspace 不作用户阶段。旧精确 stage lineage 仍可作诊断，普通界面不绘制第二轨。

Codex activity 首先显示正在执行命令/修改文件/分析/等待响应/可能停滞等状态以及当前 Run elapsed time。Native Plan 单独折叠且允许不存在，structured Goal Progress 独立展示；无 native plan 不等于无进展。原生请求通过现有 resolver 回复并携带完整 Task/Run/thread/turn/connection/item/request 身份。

## Usage / Result

Go 的 `workflowpresentation.BuildTask` 是唯一聚合算法；SQLite 新增按 Task 查询 Runs 的纯读路径。聚合 exact Project + FeatureArea + Task 下所有 Execution/attempt 的 distinct Run，不把独立 Review Task 再算入 Development Task。

Duration 是累计 Run 执行时长，分别列 Implementation、Verification、Other；不是从意图产生到结束的日历跨度。**23:50 + 08:02 = 31:52**，不会仅显示最新 Verification 的 08:02。缺失时长标为不完整。

所有纳入的 Run 都有 AVAILABLE attribution 才返回完整 Input/Cached Input/Non-cached Input/Output/Reasoning Output/Total。有任一 UNAVAILABLE（包括 AVAILABLE 但缺少 usage）则 Task UNAVAILABLE、无完整 token totals，显示 **Token 用量无法完整可靠归因**，Details 指明 unavailable Runs。没有 unavailable 而有 pending 时为 PENDING。没有伪造 0、忽略缺失后声称完整 Total、或以 native cumulative counts 替代 Run attribution。Duration 与 token availability 独立。

Result 直接展示既有 Task 的 SUCCEEDED/FAILED/CANCELED/SCOPE_CONFLICT 等语义结果及简短 Run result/failure 摘要。Verification transport SUCCEEDED 不自动变成 PASSED；既有确定性 Core 结果可直接呈现，freeform/custom semantic report 保守呈现 **REPORTED**，不通过解析 prose 建立第二 reducer。原始报告与 transport status 保留在 Details。

## 共享 projection 与刷新

`TaskView` 由 client-neutral Go presentation 产生，controlapi、Console、Web、GPT MCP、Local MCP 使用同一状态。新增公开纯读 `task_presentation_get`，支持无 Card 客户端。文本优先呈现同一 Task 核心事实和 FeatureArea idle/current/latest，再附诊断信息。

Web 用 `GET /ui/api/tasks/{id}?project_id=...` 获取当前打开的 Task。Card 初次/手动导航读取 workspace + branch inventory；自动刷新只读 exact `task_presentation_get`。非终态 opened Task 每 5 秒刷新，用户等待状态仍可观测；收到终态快照后停止。关闭、切换、hidden 会停止/暂停旧计时器，并通过 abort/generation fencing 拒收旧响应。列表和 FeatureArea overview 不持续轮询。Details 只展开已读取的信息，不推进 workflow。

Web action adapter 重新核验 exact capability target 后转交 canonical Cancel/Recover/Approval；Create Task 转交原有 Core Review admission。Card 同样使用既有 canonical tools。没有机械 Start Execution 用户步骤，也没有放宽 UNKNOWN/cancellation/recovery、epoch fencing 或 same-FeatureArea serialization。

## Visual acceptance

Card 使用独立的紧凑工程 dashboard：分组表面、状态 pill、单一 progress rail、Usage/Result 并列、独立 Goal Progress 与 Native Plan、高级 monospace Details，提供 light/dark 颜色。320px 下结果纵向排列、阶段纵向展示，长标题保留完整 accessible name。没有依赖宿主颜色区分状态。

实际浏览器检查生产 Web/HTML 资源，保存以下证据：

- [Web 三列列表](task-first-webui-card-v0.1/web-task-list.png)
- [Web 精确 Task 详情](task-first-webui-card-v0.1/web-task-detail.png) / [一级 Result 与 Usage](task-first-webui-card-v0.1/web-task-result.png)
- [Card light](task-first-webui-card-v0.1/card-light.png)
- [Card dark / 320px / 长名称](task-first-webui-card-v0.1/card-dark-narrow.png)
- [FeatureArea ACTIVE / idle](task-first-webui-card-v0.1/card-idle.png)
- [权限状态 dark Card](task-first-webui-card-v0.1/card-permission-dark.png)
- [DOM/geometry 记录](archive/task-first-webui-card-v0.1.md#file-97dd3c5e1134779f)

Light 560px、dark 320px 长名称和 idle 均无横向溢出；Task 只有一轨，idle 没有伪造进度轨。Web 整行打开 Task B，直接看到 31:52、UNAVAILABLE、Result 和 REPORTED，Details 展开可见 unavailable Run。截图来自隔离只读 fixture，不是实际 ChatGPT host：**REAL_CHATGPT_HOST_NOT_RUN**。没有假称真实付费 Agent 或真实宿主 E2E。

## Tests / gates

| Gate | 结果 / 证据 |
|---|---|
| changed Go gofmt + git diff --check | PASS |
| `go vet ./...` | PASS，[log](archive/task-first-webui-card-v0.1.md#file-2d4f4858ae7d0b53) |
| `go test ./... -count=1` | PASS，[整仓 log](archive/task-first-webui-card-v0.1.md#file-0ce1a49dba196d78)；含 bridgehttp/consoleapi/controlapi/workflowpresentation/workflowprogression/mcpserver/localmcp/SQLite contract |
| Web/Card JS/DOM | PASS，241 tests / 237 pass / 4 既有 opt-in skips / 0 fail，[log](archive/task-first-webui-card-v0.1.md#file-8a30d40d9096a950) |
| 三 Task continuation | PASS，[log](archive/task-first-webui-card-v0.1.md#file-3ba875057a2ef062) |
| workspace ownership / admission / 三 Task race | PASS，[log](archive/task-first-webui-card-v0.1.md#file-47a97759c27a2a0c) |
| presentation/controlapi/consoleapi/bridgehttp/workflowprogression race | PASS，[log](archive/task-first-webui-card-v0.1.md#file-2b3916f970d071d1) |
| mcpserver race | 独立整包重跑 PASS，[log](archive/task-first-webui-card-v0.1.md#file-655a97be134749f5) |
| Desktop `go test ./... -count=1` | PASS，[log](archive/task-first-webui-card-v0.1.md#file-8bef1d99a0b108bc) |
| Desktop frontend `npm test` | PASS，43 / 43，[log](archive/task-first-webui-card-v0.1.md#file-91ce36bd03e59664) |
| frontend `npm run check` / `npm run build` | PASS，[check](archive/task-first-webui-card-v0.1.md#file-125a2ec4cec89079) / [build](archive/task-first-webui-card-v0.1.md#file-d0c7622f626ba3b0) |

新增/更新覆盖 exact row navigation、opened Task polling 生命周期、idle/current/latest、single rail、31:52 聚合、all-attempt 去重/exact ownership、UNAVAILABLE 无假 Total、semantic verification、native plan 与 Goal Progress、历史 Task actions、原生权限回复 exact identity、旧 callback fencing、Details 纯读、light/dark。

真实 Core + SQLite + fake Agent 的三 Task 测试增加跨 Web/Console、GPT MCP、Local MCP 的同一 TaskView 对比；第二次 intent 从 Console 的 Web admission 路径进入。终态 Task 拒绝 runtime action，重复 presentation read 不新增 events。原有 canonical workspace/provenance、scope ceiling、旧能力拒绝及串行化断言保留。

测试调整删除了已废弃的“双轨/Start Execution/列表全局 polling”UI 期望，未删除安全断言、未新增 skip。一次并发 gate 下 MCP 既有短 deadline 触发超时；保留首轮失败日志，独立整包 race 成功，未放宽 deadline 或断言。新集成测试曾按 MCP 包装读取原始 Go admission 返回，已纠正测试读取后通过。Desktop 缺少 ignored dependencies/dist 时正常准备依赖并构建；临时 node_modules symlink 已移除。

## Residue / changed files / final state

[完整 changed files](archive/task-first-webui-card-v0.1.md#file-3b4f4bf0d60481c8) 与 [residue/boundary audit](archive/task-first-webui-card-v0.1.md#file-da7cd3d21c705749)。主要修改为 shared TaskView、scoped Run read、control/Console/HTTP/MCP adapters、Web Task drawer/poll controller、Card renderer、对应 tests、fixtures 和文档。

Production TaskPreparation/task_preparation/task_prepare/PREPARATION/TASK_PREPARING/preparationId/taskPreparationId：**0**。生产 Web 资源与主 Card 的 Preparation / Start Execution 文案或 action：**0**。既有内部 canonical execution_start machine admission 未重构，不构成 UI 用户步骤。历史验收文件和通用 workspace/run 准备措辞与已删除 domain 对象明确区分。

原始 checkout 与 architecture worktree 最终 fresh 核验均 clean，schema 及冻结 owner/domain 文件对 architecture checkpoint 的 diff 为零。UI checkpoint 提交后保持 clean，两个分支及独立 worktree 保留等待统一 integration。最终 SHA 与工作树回执见文首链接。未执行 merge/push/release。
