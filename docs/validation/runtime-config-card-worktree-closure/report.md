# Runtime Config + Card Approval + Worktree UX Closure

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](../README.md)。 本报告整理前的原文及同期证据保存在[批次归档](../archive/runtime-config-card-worktree-closure.md)中。

> Historical implementation evidence. [GPT surface consolidation V1](../../architecture/gpt-surface-consolidation-v1.md) supersedes the GPT/Local tool names, read envelopes, entry identity and card interaction rules below. Existing validation here is not evidence of V1 host acceptance.

任务：`V0_1_RUNTIME_CONFIG_CARD_APPROVAL_AND_WORKTREE_UX_CLOSURE`。本报告对应工作区未提交实现；验证只使用临时 SQLite、fake Codex、隔离 loopback 与离线浏览器夹具。

1. **Starting branch / HEAD**：`dev_20260908_1913` / `472dc245597711a3c093ec29489f7914c2b6c6ca`。开始时 `git status --short` 为空。先核实当前 Task-owned v44、ReviewDepth v45、Acceptance、Direct File、single-current Card 与 Desktop persistence，再实施；没有覆盖既有修改。
2. **Schema / config versions**：SQLite `45 → 46`；Desktop Coding Agent config 升为 `codingAgentConfigVersion: 2`。v46 增加 Execution 与 Review Approval materialization 的 `runtime_config_json`、不可更新触发器，并拓宽 Task/Review 已有 reasoning enum 以容纳当前 runtime 的 max/ultra。没有改 Task phase/status/ownership。历史 Execution 的 NULL runtime snapshot 保留历史语义，不伪造审批事实。
3. **Desktop previous config**：实际旧格式是 `allowedModels[]` 与全局 `allowedReasoningEfforts[]`，并非一个单独的 `global reasoningEffort` 值。
4. **Final per-model config**：`CodingAgentModelConfig{modelId, enabled, supportedReasoningEfforts[], defaultReasoningEffort}` 是唯一规范配置。四个当前产品 model 的默认值与能力见下表。每行独立修改；关闭/重新启用保留该行默认值；Core 在运行时通过已有 CAS inventory 接口更新，活跃 Run 不受影响。
5. **Capability validation**：从本机已安装 Codex App Server 的嵌入 catalog 只读提取，未启动真实 Codex。Core 严格校验 model、每模型 supported enum、default、enabled 状态，客户端无法扩充能力。`Luna + ultra` 被拒绝；`Terra/Sol/Astra + ultra` 合法。目录是产品基线能力，不声称在线账户当前可用性。
6. **Migration**：旧 enabled subset 被保留；对每个 model，从旧 effort 集合与实际能力的交集中优先取 medium，否则取稳定顺序中的首个可用值；无交集时用实际支持集的稳定默认。迁移后原子保存 v2 并删除旧字段。v2 文件夹带旧字段时也只保留 v2，保存时不再输出旧字段。新安装使用 catalog 的独立默认值。
7. **Override precedence**：显式 operation effort 优先；只有 model 时用该模型配置默认。新审批时解析并冻结；已批准 Execution 的启动、重放、延迟 materialization 不再读取 Desktop 默认。`NewAuthority` 接收仅有 `ModelConfigs` 的输入时也保留配置。
8. **GPT decision inventory**：`REVIEW_APPROVAL → review_approval_respond`；`TASK_ACCEPTANCE → task_acceptance_respond`；`RUNTIME_APPROVAL → ui_approval_decide`；`RUNTIME_REQUEST → codex_request_resolve`；`RETIRE → featureArea_retire`；`CANCEL_EXECUTION → execution_cancel`；`RECOVER → agent_operation_recover`。owner、Task/FeatureArea、semantic state、exact options/targets、requiredInput、已有时间字段均由 shared projection 输出。运行时请求没有变成 Task business Decision。
9. **Single-current consolidation**：当前 session 绑定 Project 的所有 Task/FeatureArea decisions 进入原 Card 的 Pending Decisions 区，无须选中对应 Task。审批分页读完，101 条用例证明不截断在单页；局部详情读取失败返回可见 warning 并保留其余结果。动作使用现有 presentation identity / lease / generation fence，完成或丢失响应后读取 same Card。
10. **Standalone Card removal**：生产不再注册 `ui://devbridge/decision-card`；decision tool metadata 不再带该 resource URI；旧 HTML 只由 `_test.go` 嵌入作为历史回归夹具，不进入生产 binary。生产资源目录仍只有一个 interactive Project Card；已有 FeatureArea details 是只读详情。
11. **Non-current Project**：聚合先按 session Project 过滤 Task，再核对 Run 与官方 request 的 Project。显式 foreign Project 永远拒绝；历史空 ProjectID 只允许通过当前 Project 的精确 owner 回查。UI 有第二道 request Project fence。切换 Project 沿用既有 current-card/session replacement 机制，没有新增自动弹出的审批卡。
12. **Actual Fast capability**：本机 catalog 把 `service_tiers[].id="priority"` 命名为 `Fast`，四个产品模型均具有它。源 binary SHA-256：`c147aa90d34139599711fb568102ceefc6319ca1ac5cb6f4056ca46a1834edd9`；catalog 起始 byte offset `168555365`。未运行真实 Codex，未调用外网。详见 `runtime-capability.json`。
13. **Field/protocol mapping**：`codexFastMode=true → priority`，false → 空 tier。`thread/start.config.service_tier` 设置新 thread；`turn/start.serviceTier` 覆盖新/复用会话的具体 turn，关闭时显式 JSON null。真实安装 binary 包含 `TurnStartParams.serviceTier` 字段；fake protocol tests 验证序列化与传递。没有以 prompt 模拟速度设置。
14. **Orthogonality**：ReviewDepth FAST/DEEP、model、reasoning、CodexFastMode 四者独立。测试覆盖 FAST Review + fast=false、DEEP Review + fast=true、fast=true+xhigh/ultra、fast=false+low/xhigh。没有 depth→effort/speed 推导。
15. **Next-run contract**：审批入参 `nextRunRuntimeConfig{model, reasoningEffort?, codexFastMode}`；持久化 snapshot 另有由能力推导的 `serviceTier`。三个可授权后继 Run 的动作有 UI；ACCEPT/REJECT 不显示无意义的下一次运行控件。切换 model 切换 supported efforts/default，并记住本次对各模型的显式选择。
16. **Review APPROVE binding**：既有 Review approval reducer 在成功事务内冻结配置至 approval materialization；既有 Task execution handoff 原样复制到 Execution。exact Task/gate/revision 仍由 Core 校验，TaskDefinitionRevision 仅含语义定义。
17. **REQUEST_CHANGES binding**：same Task、新 immutable revision、一个 successor Implementation Execution；Upper LLM 明确给出的 WorkSpec/VerificationSpec 与 runtime config 分开传输和保存。表单不合成语义指令。
18. **REQUEST_REVERIFICATION binding**：same Task、same revision、新 verification-only Execution；使用本次冻结配置，不产生 implementation revision。
19. **Durability/replay/restart**：测试覆盖 approval materialization、SQLite close/reopen、相同请求重放、改变 model/effort/Fast 的冲突，以及默认来源在批准后不可用的情况。UI 在 same-card refresh/response loss 时保留已选择参数与稳定 requestId；Core 才是持久化权威。
20. **Prompt Neutrality**：model/effort/Fast 位于结构化 runtime metadata；不写 WorkSpec/VerificationSpec 或 authored prompt。真实 SQLite byte/SHA 回归与四组 fake dispatcher 组合证明 `authored instruction == stored revision instruction == sent prompt`。
21. **WorktreeInventory contract**：shared service 输出 path/kind/IDs/owner/branch/head、registration/existence 及各自 known flag、workingTree/activity/activeOperation/lastActivity、integration target/unique commits、cleanupState、locked/prunable/readErrors。来源为 formal repository、持久化 worktree/workspace/run 及实时 Git registrations 的并集，不增加目录状态数据库。
22. **Registration vs directory**：Git porcelain `-z` 登记与 `os.Stat` 目录观察独立；测试覆盖登记无/目录无、登记无/目录有、登记有/目录无。包含换行路径与 `/var`/`/private/var` 规范化。未任意扫描用户 filesystem，未知于 Git 和持久记录的目录不在 inventory 发现范围内。
23. **Dirty/clean**：Core 使用只读 Git status，并核验实际 repository top-level，避免把残留目录当成父仓库；禁用 optional locks 与 fsmonitor。无法可靠获取时 UNKNOWN，不伪造 CLEAN。
24. **Integration**：将 candidate HEAD 与所选 development ref 的当前 HEAD 做 Git ancestry 判断。ancestor 为 INTEGRATED；存在不在开发线中的 candidate history 为 NOT_INTEGRATED/unique；无选定 target 或读取失败为 UNKNOWN。不会按 branch 名推断。
25. **Cleanup mapping**：活动 → ACTIVE；未登记但有目录 → CLEANUP_PENDING；已登记但无目录 → MISSING_DIRECTORY；两者都无 → REMOVED；dirty → PROTECTED_DIRTY；locked → PROTECTED_LOCKED；unique commits → PROTECTED_UNMERGED；仅 clean+integrated+已证明 idle → SAFE_TO_CLEAN；事实不足 → UNKNOWN。它只是观察分类，不是删除授权。
26. **Formal protection/activity**：formal 永不 SAFE_TO_CLEAN。活动来源同时使用 exact Run 与 FeatureArea claim，按时间及 ID 稳定选择最新操作；历史空 ProjectID 通过精确 worktree/workspace 证明归属。Conversation 依赖缺失或读取失败保持 UNKNOWN。Core 成功返回 UNAVAILABLE 且无 claim 表示已关闭的历史 area，不代表读取失败，可判定 Bridge idle。
27. **WebUI UX**：工作区页呈现 Worktrees 清单、状态、路径、展开详情和刷新；中英文本解释目录残留/登记残留/均移除。真实 1440px 与 320px iframe 的离线浏览器截图已检查；320px 保留既有可横向滚动 tab navigation，长路径/HEAD 正常换行。没有新增 cleanup 按钮。
28. **Console/JSON parity**：`controlapi.GetWorktreeInventory` 与 Console facade 共享同一 DTO；`GET /ui/api/worktree-inventory?project_id=...&format=json|text` 输出相同事实。只接受 project_id/format，不接受 path、shell 或 mutation 参数；Refresh 为 GET，返回 no-store。
29. **Modified files**：完整列表见 `modified-files.txt`；按职责集中于 Desktop/settings、codexruntime、workflowprogression/SQLite/execution、Project Card/shared presentation、worktreeinventory/Git/read endpoints/WebUI 与对应测试。文档同步更新，旧测试夹具更新为当前 Task-owned/Review contract，没有修改生产 workflow 规则来迎合测试。
30. **Go tests**：root module `go test ./...` 全量通过，73 个 package 结果（含无测试文件包）；受影响 Core/MCP/Local MCP/SQLite/Execution/Direct File/retire/workflow 回归通过。生产与 Local MCP catalog discovery/parity 契约测试通过。日志 `root-go-test.log`。
31. **Race tests**：已完成 broad `-race`：codexruntime、controlapi、storage/sqlite、execution、bridgehttp 均 PASS。最新受影响代码另跑 codexruntime/worktreeinventory，以及 SQLite model-only approval freeze/materialization 和四配置 Execution prompt/dispatch 定向 race，均 PASS（`root-go-race.log`）。一个重复 broad race 被停止以免再次重跑已通过的 SQLite 全包；不作为通过证据。
32. **Desktop tests**：Desktop module `go test ./...` 通过（含最后配置持久化与 HTTP request 变更）；frontend `npm test` 46/46，`npm run build` 通过。
33. **Node/Card/Web tests**：`node --test internal/mcpserver/*test.js internal/bridgehttp/*test.js`：393 passed、4 skipped、0 failed（397 total）。跳过项是需要显式外部 snapshot/orphan fixture 的既有测试，未启动真实 host。
34. **Migration tests**：v44/v45→v46 保留 Task/Review rows、indexes、triggers、FK 完整性；新增 enum、runtime snapshot 不可更新；Desktop legacy→v2、restart、独立 model 默认、禁用再启用与冲突 legacy 字段清除均有临时夹具测试。相关测试包含在通过的 SQLite 与 Desktop 全量测试中。
35. **Prompt byte tests**：`TestPromptNeutralityFrozenRevisionByteIdentityAndSHA` 与 `TestReservedExecutionDispatchUsesFrozenRuntimeConfigAndPrompt`（四配置组合）通过；运行时 Fast 不改 authored 文本。最新审批模型默认冻结与 runtime/prompt 分发用例亦通过定向 race。
36. **Static audit**：旧全局字段仅在一次迁移、CLI legacy ingress、derived GET/output、历史兼容测试出现；新的 HTTP PUT/保存只有 canonical per-model 配置。`decision_ui.html` 只由测试嵌入；scope-expansion 的旧卡片文案位于非生产工具，目录测试守卫其未注册。Fast 只在 DTO/storage/protocol 中，ReviewDepth 字段分离。WebUI 无 shell/Git/delete；新增 inventory 无 cleanup mutation。静态命中与说明见 `static-audit.txt`。
37. **go vet**：root 与 Desktop module 的 `go vet ./...` 均通过（exit 0）
38. **git diff --check**：通过，无空白错误。
39. **Final git status**：仍在起始分支/HEAD，保留本任务未提交变更供审查；详见 `git-status.txt` 与 `worktrees.txt`。
40. **SCOPE_CONFLICT**：本轮无新增 SCOPE_CONFLICT。Fast runtime capability 在已安装 catalog/protocol 中可见。遵守测试边界，未进行真实 Codex/在线账号速度可用性验证；这不是伪造实时 backend 成功。
41. **Task-owned / FAST-DEEP / Acceptance**：ownership、phase/status、Review 1:1 Run、same-FeatureArea serialization、exact acceptance/resolver、revision immutability、auto commit 与 FeatureArea submission 保持原契约；相关 targeted regressions 已验证；全量结果见第 30 项。
42. **Direct File**：Codex XOR FILE capability、Direct File ownership 与执行路径保持现有 Core；本轮没有新增并行写入权威或让 Card 绕过 Core。
43. **Retire**：同卡明确确认，仍调用 `featureArea_retire`；不引入恢复为 ACTIVE 的 UI，不改标准取消/收敛流程。
44. **Production recall**：保持 `EXTERNAL_INTEGRATION_BLOCKED` / `HOST_INTEGRATION_PENDING`。hidden-epoch recall 的本地回归继续覆盖；本轮不处理 production host recall。
45. **Delivery boundary**：没有 commit、push、tag、release，没有操作其他用户 worktree，没有清理用户 worktree/branch/cache。SQLite migration 只在临时测试库运行，Desktop settings 只在隔离夹具验证；实际生产数据库与用户配置未被本轮迁移。

| Model | Catalog default | Supported reasoning efforts | Codex Fast tier |
|---|---|---|---|
| gpt-5.6-luna | medium | low, medium, high, xhigh, max | priority |
| gpt-5.6-terra | medium | low, medium, high, xhigh, max, ultra | priority |
| gpt-5.6-sol | low | low, medium, high, xhigh, max, ultra | priority |
| gpt-6-astra | low | low, medium, high, xhigh, max, ultra | priority |

验证环境：`GOCACHE=/private/tmp/devbridge-runtime-card-gocache GOPROXY=off GOSUMDB=off`；真实 Codex smoke opt-ins 关闭。离线截图位于 `/private/tmp/devbridge-runtime-card-visuals/worktrees-1440.png` 和 `worktrees-narrow.png`。截图用的全新临时 Chrome profile 禁用 background networking、DNS 外连与 extensions，不访问用户 browser profile。
