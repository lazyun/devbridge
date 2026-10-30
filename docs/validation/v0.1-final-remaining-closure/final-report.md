# V0.1 Final Acceptance Remaining Closure

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](../README.md)。 本报告整理前的原文及同期证据保存在[批次归档](../archive/v0.1-final-remaining-closure.md)中。

F1 / P1、F2–F5 / P2、F6 / P3 均为 **CLOSED**。最终组合验证通过；本轮范围内未发现新的 repository-internal P0/P1。工作区保留供人工检查。

1. **Starting branch / full HEAD**：`dev_20260908_1913` / `3d251564089935d402a444bb8265be3e134e2cf0`。实施前 fresh 核验与预期一致，初始工作区 clean；没有 HEAD 前移或 CLOSED_BY_BASELINE 项。六项均由当前源码重新确认后实施。见 [baseline.txt](../archive/v0.1-final-remaining-closure.md#file-17c08d9a46bee411)。

2. **Schema version**：起点为 v45。冻结 Task-owned workflow，本轮没有新增持久数据 contract，也没有修改 SQL schema 或 migration 实现。

3. **F1 final root cause**：GPT `decision_respond` 实际是 Review Approval alias，缺少 Acceptance GPT 注册；Card 同时过滤 Acceptance 动作，且共享 target 缺少明确 revision 字段。既有 SQLite Acceptance authority 已具备四种语义，无需复制业务 resolver。主 agent 先实施 F1，并通过 Go/Card focused gate 后才开始其余实现。

4. **GPT Acceptance adapter final contract**：增加显式 `task_acceptance_respond`，复用 `workflowprogression.TaskAcceptanceInput` 和既有 `ResolveTaskAcceptance` 链路。必填 `aiSessionId/taskId/featureAreaId/executionId/taskDefinitionRevisionId/decisionId/option/requestId`。Core 重新核验 Project 绑定、immutable observed target、当前 eligibility 和 durable receipt。`review_approval_respond` 保持 Review-only。Local generic Acceptance 分支也要求相同 exact fields。

5. **Card Acceptance final contract**：只消费 shared authoritative Task actions；四种动作必须携带 exact Task/FeatureArea/Execution/revision/gate。`REJECT` 按 decision kind 区分 Review 与 Acceptance。成功后刷新当前 Card；失败重试以完整相同 payload 生成稳定 requestId。Card 不生成 capability、不改 Task.phase，也不创建第二张 Card。

6. **Four Acceptance actions parity**：以下状态由同一个 SQLite `WorkflowProgressionStore.ResolveTaskAcceptance` 决定。实际 GPT MCP、Local MCP 和 Web HTTP 隔离测试逐一验证四种动作，并跨适配器重放 receipt；Card DOM/RPC 测试逐一验证可见性、目标、工具调用和同卡刷新，再由相同 GPT 工具契约连接上述 Core 证据。

   | Action | Authoritative result | Task / revision / Execution |
   | --- | --- | --- |
   | ACCEPT | SUCCEEDED | 原 Task；无 successor |
   | REJECT | CANCELED | 原 Task；无 successor |
   | REQUEST_CHANGES | IMPLEMENTATION_READY | 同 Task，新 immutable revision，新 Execution |
   | REQUEST_REVERIFICATION | VERIFICATION_READY | 同 Task、同 revision，新 verification-only Execution |

7. **Successor spec contract**：REQUEST_CHANGES 要求完整 `nextWorkSpec` 和 `nextVerificationSpec`，包含 caller 明确提供的 implementation / verification `codexInstruction`。Card 提供空白 JSON 输入；不补 spec、不从旧 Goal 回填、不改写 instruction。REQUEST_REVERIFICATION 依既有 Core 沿用原 revision/spec，不接受额外 implementation successor input。其余三个选项的公开 schema 禁止 successor specs。

8. **Stale / replay / response-loss evidence**：`TestFinalAcceptanceCrossAdapterParity` 覆盖 missing/wrong Task、area 缺失、Execution、revision、gate、wrong phase、缺失/无效 successor spec、成功后跨三适配器重试、changed payload conflict、旧 gate 新 requestId 和 terminal Task 拒绝。Core 对 immutable observed target 的检查位于 receipt replay 前，对当前 execution/revision/phase 的检查用于新 mutation。REQUEST_CHANGES / REQUEST_REVERIFICATION 的并发重试由 `TestFinalAcceptanceConcurrentReplayExactlyOneSuccessor` 证明只产生一个 successor；ACCEPT/REJECT 只生成 receipt。Web 绕过 presentation visibility，使响应丢失后的 replay 仍可到达 Core；fresh eligibility、exact replay、stale reject 均由同一 Core 判定。见 [acceptance-focused.log](../archive/v0.1-final-remaining-closure.md#file-f6cbdae4000a0db9)、[race-tests.log](../archive/v0.1-final-remaining-closure.md#file-4529185aceb0b2c6)、[node-tests.log](../archive/v0.1-final-remaining-closure.md#file-2d0a10247b4ee927)。

9. **F2 HTTP Task-create final disposition**：production caller 搜索为零，因此删除 `POST /v1/tasks` 路由、handler 和旧输入 DTO；`Facade.CreateTask` 返回退役错误。历史 real_e2e harness 引用分类保留，本轮没有运行。未为无 receipt 写路径保留兼容行为。

10. **Canonical CreateIntent / replay evidence**：`CreatePlanningTaskForSession` 和 `task.Service.Create` 均要求有效 requestId；Service 删除 requestless `store.Create` fallback，正式语义创建必经 `CreateIntent`。SQLite receipt 负责原子插入和 payload 冲突。新增真实临时 SQLite 测试并发四次同请求、Local response-loss retry、改变 payload 和省略 requestId，断言 exact same Task、Task/receipt count 均为 1。MemoryStore receipt 仅用于单元测试，未作为持久化证据。旧测试 fixture 已补显式 session/requestId。

11. **F3 Direct File size contract**：最终 decoded content 上限统一为 262144 bytes（256 KiB）。raw UTF-8 按 bytes 检查；base64 先按 `4 * ceil(262144 / 3) = 349528` encoded chars 限制 allocation，再 decode，最后按 decoded bytes 检查。MCP schema 按 encoding 分支并复用 Core 常量。UTF-8 schema 的字符约束之外，Core 仍是字节数 authority。

12. **Base64 boundary evidence**：191、200、255、256 KiB 的 raw/base64 均成功；256 KiB + 1 byte 均返回 FILE_TOO_LARGE；异常巨大 encoded input 在 decode 前被拒绝。无效 base64 使用当前 Core 的 structured `INVALID_FILE_TARGET` 错误，未另造错误类别。除纯边界单测外，真实 Facade file_create 也验证 exactly 256 KiB decoded base64 成功。见完整 [go-tests.log](../archive/v0.1-final-remaining-closure.md#file-43a8c53973f7b2c8) 中 Direct File service、schema、Facade 测试。

13. **F4 Card Retire confirmation behavior**：低频 destructive menu 的“关闭功能领域”首击只打开 Card-native confirmation，不计算 requestId、不发 RPC。内容明确关闭流程、拒收新开发 Task、active work canonical cancellation/convergence、关闭后不再 ACTIVE。“取消”零 mutation；“确认关闭”才生成稳定 requestId 并调用 `featureArea_retire`，成功刷新同卡。RETIRING/RETIRED/DECOMPOSED 隐藏入口；保留 authoritative capability 检查。未使用 window.confirm，未修改 Retire Core。Node 回归包含首击、取消、确认、重试和 lifecycle suppression。

14. **F5 selectedWork operation DTO**：新增 client-neutral `SelectedOperation`，字段为 `kind/operationId/ownerTaskId?/runId?/status/updatedAt` 及必要的 readOnly、backendDisposition、recoveryRequired、failureSummary。kind 仅为 REVIEW/EXECUTION/INSPECTION。Review 使用 exact current Task-owned Review，Execution 使用 exact current Execution，Project Inspection 使用其实际 operation ID、可不带 ownerTaskId；无 current operation 时为显式 null。

15. **Web / Card / Console / JSON parity**：shared workflowpresentation、controlapi、MCP fallback/schema、Console JSON 和 Card 统一使用上述 DTO。`selectedTaskId` 仅是 semantic DEVELOPMENT Task identity；不再存在读取 `selectedWork.id/work` 作为 Task 的 consumer。Project Inspection 独立保存 selection：area Task → 显式 project Inspection → empty-argument refresh 保持同一 Inspection，不恢复旧 Task/area。集成测试覆盖该序列以及 REVIEW_REQUIRED 无 operation、Review recovery、Console 输出、实体域和 updatedAt。

16. **F6 canonical docs updated**：修正 README、interfaces，以及 verification-report-and-task-acceptance、agent-runtime-safety、direct-file-capability、agent-execution-ownership、feature-area-continuation、feature-area-ownership、feature-area-retirement、unified-agent-work-and-cancellation 共 10 个 canonical 文件。明确 v45、Task.status/phase 分离、Acceptance 四语义、structured scope disposition、exact authored instruction、child recovery 与 semantic Task 解耦、SelectedOperation 和原生关闭确认。历史 closure reports 和真实 migration 历史叙述保留。

17. **Legacy executable-authority audit**：Task HTTP 创建入口已删除。旧 TaskPlan POST 可留下 inert pending proposal，但 DEVELOPMENT Apply 被 store 拒绝且无 HTTP confirm/apply 路由。旧 scope-review POST 只能接受既有 Review；正常 v45 admission 已原子绑定 Run，RunID/start-failure guard 阻止该路径重启。旧 execution-proposal POST 固定 PENDING_CONFIRMATION，v45 `approved_execution_binding` 在 approved Task 上拒绝插入；只有正式首次 materialization 或 receipt 指名 successor 可以通过。新增 HTTP 409 后继续四种 Acceptance 的回归排除了干扰 successor 的疑点。旧 decision 名称仅剩 routing 分类字符串，未注册工具。未发现第二条可推进 v45 semantic Task 的 executable authority。

18. **Prompt Neutrality preservation**：没有修改 production prompt builders/transport。REQUEST_CHANGES 跨适配器测试证明 caller 的 CRLF、空白和中文 instructions 原样进入 immutable successor revision；独立 byte/SHA suite 证明 frozen Review/Implementation/Verification/Probe instruction 与最终 sent prompt 字节一致。二者为组合证据，没有将其宣称为一条真实 host 端到端测试。没有 instruction injection、补充、summary 或旧 Goal fallback。

19. **FAST / DEEP preservation**：requiredReviewDepth、reviewDisposition、latest-only Review Approval 与 v45 depth 规则未变。byte/SHA suite 覆盖 FAST/DEEP 均不注入 prompt；fresh depth migration/admission tests 通过。

20. **Direct File preservation**：仍为既有 8 tools；Codex XOR FILE、workspace ownership、filesystem containment 和其他文件 authority 没有改动。仅闭合 encoding size contract；没有 file_patch/file_edit/append_file 注册。

21. **Task-owned v45 preservation**：保持 Review [0:N]、Review 1:1 Run、Execution [0:N]、immutable definition、same-FeatureArea serialization、runtime epoch/UNKNOWN/recovery、FeatureArea Workspace/AgentConversation ownership、Task auto commit、FeatureArea submission 和 Card recall hidden epoch contract。Acceptance 只补 adapter/target/replay 可达性，未建立第二 business authority。

22. **Schema remains v45**：最终仍为 v45；SQL schema/migration 文件无差异。fresh DB、Task-owned 升级与不兼容数据拒绝、v44 → v45 ReviewDepth preservation/fail-closed/admission 测试均通过。

23. **Modified files**：61 个 tracked 文件修改，5 个 untracked source/test 文件新增，共 66 个 source/docs/test 文件；另有本次 validation bundle。完整列表含所有证据文件，见 [modified-files.txt](../archive/v0.1-final-remaining-closure.md#file-06bee61a8b346800) 和 [git-status.txt](../archive/v0.1-final-remaining-closure.md#file-ba449914b011dbb5)。patch 本体见 [working-tree.patch](../archive/v0.1-final-remaining-closure.md#file-e1267894140b1953)；untracked source hashes 见 [validation-manifest.md](validation-manifest.md)。

24. **Go tests**：13 个受影响包全部 PASS；672 个顶层测试 pass、5 skip、0 fail。包含子测试的事件计数为 1617 pass、5 skip、0 fail。5 个 skip 为现有 opt-in real/candidate runtime 测试，名称见 [test-summary.json](../archive/v0.1-final-remaining-closure.md#file-cc5f5e1640dca626)，未把它们计作通过。Acceptance focused 单独 15 个顶层测试（含子测试 33 pass）通过。最终命令、UTC 时间、退出码保存在原始日志和 result JSON。

25. **Targeted race**：sqlite、localmcp、controlapi、workflowpresentation 四包通过；14 个顶层测试、含子测试 28 pass、0 fail/skip。覆盖 Acceptance concurrent/replay、changes/reverification exactly-one successor、Task-create duplicate request、shared selection/remembered project Inspection。

26. **Node / Card / Web tests**：所有 `internal/mcpserver/*test.js` 与 `internal/bridgehttp/*test.js` 一次最终组合运行：**370 tests，366 pass，0 fail，4 skip，0 cancelled/todo**。包括 Card 四种 Acceptance、native Retire confirmation、Task-first/selectedWork、WebUI 和 recall hidden epoch。原有 4 个外部 candidate 条件 skip 保留；没有将 fixture 宣称为真实 production recall 集成。

27. **Migration tests**：20 个顶层测试、含子测试 42 pass，0 fail/skip。覆盖历史迁移回归、fresh DB、Task-owned compatibility/rejection、v44 → v45 ReviewDepth。见 [migration-tests.log](../archive/v0.1-final-remaining-closure.md#file-e0bb925581798b2c)。

28. **Prompt byte / SHA tests**：3 个包、9 个顶层测试、含子测试 57 pass，0 fail/skip。覆盖 Review、Implementation、Verification、Probe、frozen revision、reconnect persisted prompt、空 instruction 和 substituted instruction 拒绝。见 [prompt-byte-identity.log](../archive/v0.1-final-remaining-closure.md#file-650400ce83484a1a)。

29. **Tool / catalog parity**：从 production GPT/Local registration code 启动隔离 loopback fixture，并实际 MCP tools/list：GPT **53** tools、Local **42** tools。校验 Review、Acceptance exact schema/四选项、Direct File 8 tools、FeatureArea lifecycle、Task actions 及旧工具黑名单；对比 runtime catalog 与 source schema。导出见 [GPT catalog](../archive/v0.1-final-remaining-closure.md#file-b8eed3a19aa25e06)、[Local catalog](../archive/v0.1-final-remaining-closure.md#file-9294f8b51c37cc83)、[tool-catalog.txt](../archive/v0.1-final-remaining-closure.md#file-2f67fea958c94d9f)。使用 StaticService fixture；未连接真实 Codex/live DB/外网。

30. **Static audit**：逐项保存生产命中和分类，而不是宣称 grep 全零。包括 Prompt injection identifiers、legacy pseudo-Task/decision、旧写入口及 SQL/store guard、Direct File forbidden tools、Acceptance 路由、CreateIntent、selectedWork consumer、canonical docs。独立只读复核确认结论，见 [static-audit.txt](../archive/v0.1-final-remaining-closure.md#file-fc203cb5061b172c)。

31. **go vet**：全部 13 个受影响包 PASS，退出码 0，见 [go-vet.txt](../archive/v0.1-final-remaining-closure.md#file-70b0ee196dce3d8d)。

32. **git diff --check**：最终 PASS，退出码 0，见 [diff-check.txt](../archive/v0.1-final-remaining-closure.md#file-030a4d2ae3ed8df0)。

33. **Validation patch identity**：这是 **validated working tree based on `3d251564089935d402a444bb8265be3e134e2cf0` with patch identity `05462f78985085c299b5d2d3dfb03004fdbdf16f7579125df7bebe61ce83123c`**，不是 clean-HEAD validation。最终测试前后 source snapshot 一致，source content SHA256 为 `f8e5c06c4cb084c97508a3dcaecb1cb01db84e17a4350974ff6e9ee269f27a7f`。tracked patch、全部 untracked source 和全部 evidence 的 hash 覆盖方法见 [validation-manifest.md](validation-manifest.md)；没有遗漏 untracked 文件。

34. **Final git status**：branch/HEAD 保持起点，工作区含 61 个 tracked modifications、5 个新增 source/test，以及本 validation bundle；未 staged。完整逐文件状态以 [git-status.txt](../archive/v0.1-final-remaining-closure.md#file-ba449914b011dbb5) 为准。其他 worktree 未修改。

35. **Remaining Evidence Gaps**：六项 repository-internal closure 没有剩余阻断证据缺口。真实 production recall host 集成仍未完成；保留 5 个 Go 和 4 个 Node opt-in/candidate skips。Card 证据由 DOM/RPC fixture 和实际 MCP/SQLite Core 测试组合提供，未声称真实宿主/设备/browser profile 的端到端验证。首轮组合测试曾发现 requestId fixture 迁移遗漏，已修复并对最终源码完整重跑；历史失败日志保留于 [diagnostics/first-combined](../archive/v0.1-final-remaining-closure.md)，不作为最终通过证据。

36. **SCOPE_CONFLICT**：无。没有修改 schema、扩展产品功能、重做架构或改变被冻结的语义。

37. **Production recall status**：保持 **EXTERNAL_INTEGRATION_BLOCKED / HOST_INTEGRATION_PENDING**。没有新增 fake production host，没有改变 10 秒/HIDDEN/VISIBLE/hidden epoch policy。

38. **Explicit delivery confirmation**：**no commit；no push；no tag；no release**。没有 reset/rebase、真实 Codex、外网、live DB 或用户设备/browser profile 操作；未修改其他现有 worktree，工作区留待人工审查。
