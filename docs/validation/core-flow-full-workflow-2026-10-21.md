# “统一 Core 与 pkg/flow 依赖边界”完整流程验收

状态：本任务真实业务全流程已完成。审查、审批、两文件实施、实际 Codex 验证（2项必需检查与5条标准全部通过）、用户验收、任务提交、领域合入均有正式回执。Task=SUCCEEDED，commitStatus=COMMITTED，devIntegrationStatus=MERGED；开发分支 codex/skeleton-v18-baseline HEAD=75e03db829447c7c8a93f76b2d84fab2849585ba。用户授权临时收存后，Core完成合入，原113个文件已全部恢复，并由Codex独立确认内容、类型、权限及Git状态与收存前逐项一致。保留stash备份，未推送远端。下文保留失败、修复和真实检查点历史；这不代表整个Dev Bridge测试套件全部通过。

## 准确目标

- Task：`task_0fa6e2ca0e8feb15472aca4f667d97d4`
- FeatureArea：`feature_area_881bf4ef9b1e3204bb202294bb15cdd4`
- Repository：`repository_25ce5230-5c8a-41ab-bf6f-fa323056c4bf`
- 固定基线：`fd5fccdd5c0a07c6808bdc50155a59839fdc3bcf`
- 实施渠道：GPT DIRECT_FILE；仅允许 `docs/ARCHITECTURE.md` 与 `internal/core/dependency_guard_test.go`。
- 验证渠道：独立 Codex VERIFICATION Run，保持必需命令，不以静态阅读或模型报告代替。

## 已观测检查点

1. 读取原状态为 REVIEW_APPROVAL_PENDING，原报告的必需 CUSTOM 静态检查不可执行。
2. 用户点击“确认要求修订”，原确认 `confirm_7efb547aa0d3e4faed399dd5ee843ac8` 的真实回执为 COMPLETED／CONSUMED。
3. 同一 Task 下创建后继 Review `review_rereview_9eae596d8c4669f94c33b36731890160`，Run 为 `run_rereview_9eae596d8c4669f94c33b36731890160`，Task 进入 REVIEWING。没有创建替代 Task。
4. 真实 MCP 读取／准备后继访问失败。只读核对数据库确认：后继 grant 的 `forbidden_paths_json` 被生产者写成 JSON null，读取端要求数组；原 grant 为 []。该问题阻止环境准备。

## 修复要求

- 新后继准确继承前驱禁止路径，不把未加载的字段写成 null。
- 对已经产生的 null，只允许从有正式同范围后继关系的前驱 grant 解析原边界；无法证明关系时拒绝，不默认放行，不改写历史 grant。
- 后继访问通过真实 MCP 读取、准备及重复读取；单仓库准备必须能够继续已保留的 CREATED Run。
- 后续按原 Task 顺序完成审查报告、人工批准、两文件实施、真实 Codex 命令证据、人工验收及交付核对。

后续结果将在实际发生后追加；目前没有声称流程通过。

## 后续实测及修复

5. 通过 Desktop 托管重启部署修复，使用准确 expectedUpperVersion 调用 agent_operation_recover，再独立 review_access_prepare；同一个 Review/Run 进入 ACTIVE，工作区仍为原 workspace_10b944c180dfd8d49820575255d90b73。原 grant 的 JSON null 未被改写。
6. 在正式 grant 下读取四份文件（文档、guard、architecture_flow.go、go.mod），恢复后的重新读取 digest 完全一致。
7. 报告提交遭遇连接器缓存 schema 对 `\S` 的兼容拒绝。服务改为显式整串非空白表达式并验证中文、多行、路径与纯空白；现有连接器仍缓存旧 schema。本次经相同正式回环 MCP 端点提交，保留同一 Session/Review/Run/epoch/generation；未替换身份、未改业务准入。
8. 正式报告 report_69e21a0aa6a5897d0c79997b3d676639 入库，状态 COMPLETED、APPROVAL_READY；Task 为 REVIEW_APPROVAL_PENDING。必需验证明确包含 go test ./internal/core -count=1 与 git diff --check。
9. 原 Work/Cloud 对话实际收到审查完成 MCP Events 通知，侧边任务标签页仍保持打开。
10. 准备审批发现恢复历史 UNKNOWN 记录持续覆盖工作流，返回“Review approval is no longer pending”。修复为仅识别准确 Upper 恢复回执及现有授权/正式报告完成回执；不改写历史记录，不豁免 Coding 后端边界或独立 UNKNOWN/CONFLICT。

## 回归范围与局限

- 新增禁止路径继承、单仓库后继显式准备/恢复后报告审批、真实 HTTP MCP 后继准备、文本 schema 及恢复后审批/再次重启回归。
- 相关测试使用隔离临时仓库；这些测试不替代目标 skeleton Task 的真实 Codex 执行。
- 较广回归还观察到未在本次修复中关闭的失败：TestReviewDecisionApproveReReviewAbandonSerialize 的取消后定义数量断言；TestMultiRepositoryUpperApprovalProductionClosure 的审批回执故障注入路径；TestPostOrphanReviewAcceptanceFourActionsAndRuntimeConfig/REQUEST_CHANGES 的重放后继数量；TestPostOrphanReviewReviseRejectPreserveTaskOwnedDepth/REVISE、REJECT 的定义/运行权限数量断言。不能据此宣称全套回归通过。
- 当前仍未修改目标两文件、未实际执行目标 Codex 验证、未验收或交付。下一步为恢复正确审批入口并由真实用户确认。

11. 服务 v13 部署后，真实 workspace_get 返回 REVIEW_APPROVAL_PENDING，APPROVE / REVISE / REJECT 均可用，历史 UNKNOWN 记录保持不变。
12. execution_prepare 成功：intent confirm_1b39c8d50072662188445dafa564a798，version 1，purpose EXECUTION，action APPROVE_AND_START，状态 PENDING，factsComplete/presentationReady 为 true。confirmation_options_get 当前资格 available=true，Codex 后端及模型候选可读；尚无正式用户提交。
13. 最终针对测试：controlapi 10.934s、sqlite 4.032s、mcpserver 0.751s 均通过（build/full-flow-focused-final.log）。相关构建通过；保留旧二进制和一致性数据库备份。

待下一真实用户确认后继续：两文件 Direct File 实施、实际 Codex VERIFICATION Run、用户验收、任务提交及交付。当前不可报告全流程成功。

14. 真实只读核对：后继 Run=SUCCEEDED，thread_id/turn_id 均为 NULL；grant=CLOSED 且历史 forbidden_paths_json 仍为 JSON null，历史恢复分类仍为 UNKNOWN。新工作流判定使用正式回执解释当前权威，没有改写这些历史事实。数据库 quick_check=ok。

15. 原 Work 对话的 confirmation_show 首次宿主装载失败；只读点击“重试”后真实 UI 已显示新确认。可见 GPT 文件实施、Codex 后端/模型/思考等级选择和“批准并授予文件权限”；模型尚未选择，最终确认未提交。原任务标签页保持打开且审批/修订/取消按钮可见。截图：build/full-flow-preview/approval-confirmation.png。

16. 用户在正式新审批卡片确认。原 intent 回读 COMPLETED、CONSUMED，APPROVE 与 START_IMPLEMENTATION 两步均 COMPLETED；用户显式选择 Default Codex configuration、gpt-6.1-sol、low、Fast=false。获批定义为 task_definition_task_0fa6e2ca0e8feb15472aca4f667d97d4_2_b79e2949fb178430。
17. 精确 Direct File 工作流 direct_file_event_4527cdbcf3b9d0a0926529a8f7e4b166 的 ACTIVE 授权仅允许两文件。通过正式 file_read / file_write REPLACE 实施，随后两文件读回与发送内容完全一致；未在文件实施阶段执行命令。
18. 文档补明标准库及精确 skeletonworkbench/pkg/flow 例外；守卫维持原生产 .go 扫描、排除 _test.go，以 Go 工具链 Goroot 身份及精确包名判断，拒绝 vendor/command/相对/不规范路径，并含 25 项正反例。没有改目标生产源码。
19. file_implementation_result_submit 已冻结 file_result_ef66e911d40a94cbd171e19b91168b8c，snapshot=43a813c37bfa0ef064ca79ec3c8d2e3f253bcc45，尚未 accepted。独立验证 Execution=execution_file_verify_9460a3b4128c2441944bef5e0871b40d，Run=run_verify_execution_file_verify_9460a3b4128c2441944bef5e0871b40d，状态 RUNNING / VERIFYING。
20. operation_get 实际返回 Codex 进程与 Thread=01a15edb-e1f9-7120-ba1d-855751d14c7a，Turn=01a15edb-f4e7-7e92-b90a-5a03ee86928f，CONNECTED/ACTIVE。尚无验证结论，不以进程启动当作验证通过。

21. 真实 Codex 命令证据：exec-30698737-4aad-4dad-ba9c-7e62261dd096 执行 go test ./internal/core -count=1，exitCode=0，输出 ok skeletonworkbench/internal/core 0.888s；exec-c3522f47-4feb-46ff-8a84-83e3866c5a96 执行 git diff --check，exitCode=0。前后 git status --short 均只有批准的两个文件，Git start/end snapshot 都为冻结 snapshot。
22. 验证 Run 最终为 INFRASTRUCTURE_FAILED / VERIFICATION_REPORT_INVALID，原因是模型仅报告2条合并验收标准而非5条，且 checks 使用索引1、2；Core 要求准确完整覆盖并使用零基索引。不能将命令通过宣称为正式验证通过。
23. 原因核对：BuildVerificationRunPrompt 按既有 prompt-neutrality 契约只发送已批准 CodexInstruction，不拼接 spec 中 checks/criteria；本次我编写的验证指令没有完整写入5条标准及零基报告约定。这是工程指令不完整，不能通过改写历史指令、报告或降低必需检查绕过。当前 Direct File 结果无实现 Run，现有 REQUEST_REVERIFICATION 资格要求成功的 Coding Implementation，因而该动作不可用；本次未扩充该准入规则或制造实现 Run。
24. 已按真实 REQUEST_TASK_REVIEW 可用目标准备同一任务的 REQUEST_SUPPLEMENTAL_REVIEW，仅补齐完整验证指令，保留原命令/必需性/5条验收标准、既有文件效果和历史。新 intent=confirm_7192a7da533a3c1db7860bdc0b0ce2ad，purpose=REVIEW，version=1，状态 PENDING。尚未创建新 Review 或启动重验。补审及后续新方案批准要求真实用户确认。

当前阶段：等待用户确认补充审查。两文件实施已完成，两条必需命令实际退出0；正式报告不合格，未验收、未提交或交付。项目文档补充了 exact CodexInstruction 与完整逐项报告的作者责任，以及当前 Direct File 重验入口的限制。

25. 用户确认补充审查。原补审 intent COMPLETED / CONSUMED，REQUEST_REVIEW 与 START_REVIEW 都 COMPLETED；新 Review=review_a7c9bea8876be1e764878d63613bfd53，Run=run_ab7cb1a578632b58c4defaab58a774b7，原管理工作区仍为 ACTIVE，只读快照包含已有两文件效果。
26. 通过新 Upper grant 读取四文件；文档和守卫 digest 与已实施内容完全相同，architecture_flow.go 和 go.mod 仍为原 digest。正式补审报告 report_9d95a7125367126ecae519ef8378bc7c 入库 COMPLETED / APPROVAL_READY。验证方案除 CodexInstruction 之外与此前规格逐项完全一致；指令显式列出原2个检查、原5条标准、零基索引、准确数量、完整字段及非通过状态规则。
27. 当前 connector 仍缓存旧文本 schema，报告经同一正式回环 MCP 端点提交，保留本次 grant 准确目标及读取证据，没有改写数据库。
28. 新审批 intent=confirm_edb07069b14c629ccbdb8e79cb31fda6，version=1，purpose=EXECUTION，action=APPROVE_AND_START，状态 PENDING，factsComplete/presentationReady=true。推荐运行配置来自用户此前实际确认的 gpt-6.1-sol/low/Fast=false；新方案仍须用户真实确认。尚未产生新文件授权或重验 Run。

29. 原 Work 后台已完成新审批 confirmation_show，但现有网页未同步后台消息。保持原侧边任务标签，改用当前网页发送同一 intent 的只读展示请求，没有重新 prepare、替用户选择配置或提交审批。

30. 当前网页只读展示请求已返回；首次组件装载失败，点击“重试”后加载到新审批内容。任务侧边标签保持打开，原 intent 未提交。

31. 用户批准完整修订方案。intent confirm_edb07069b14c629ccbdb8e79cb31fda6 回读 COMPLETED / CONSUMED，APPROVE 与 START_IMPLEMENTATION 均 COMPLETED；准确新定义 task_definition_task_0fa6e2ca0e8feb15472aca4f667d97d4_3_6340fef06062bc42，用户仍确认 gpt-6.1-sol/low/Fast=false。
32. 新 Direct File workflow=direct_file_event_e68bc7a4513415524b32d8bfb1323990。只读回查两文件内容、digest 均与原实现完全一致，没有再次写入。新文件结果 file_result_a221a6a0417b1aa87a97c290a1347ee3 冻结相同 snapshot=43a813c37bfa0ef064ca79ec3c8d2e3f253bcc45。
33. 新 Codex 验证 Execution=execution_file_verify_3170782f06632d99285d5f02960ea728，Run=run_verify_execution_file_verify_3170782f06632d99285d5f02960ea728 已真实启动，Task=VERIFYING。等待新 Run 独立命令证据与完整2+5报告；尚未通过验证、验收或交付。

34. 第二次真实 Codex Run=SUCCEEDED，正式 VerificationReport v2 availability=AVAILABLE。2项检查按 index 0、1 准确覆盖，均有本次 App Server observed exitCode=0；5条标准按 index 0至4 准确覆盖并逐项报告 PASSED。
35. 本次命令证据：exec-00424bb3-859e-45bb-b470-45c2026c69dd 运行 go test ./internal/core -count=1，exit=0，输出 ok skeletonworkbench/internal/core 0.933s；exec-27f9b41b-b708-4ebf-8004-d00bff04f969 运行 git diff --check，exit=0。不是借用第一次 Run 的证据。
36. Core Git 观察 startSnapshot=endSnapshot=43a813c37bfa0ef064ca79ec3c8d2e3f253bcc45；前后 Head=fd5fccdd5c0a07c6808bdc50155a59839fdc3bcf。已有两文件未被验证阶段修改。
37. Task=ACTIVE / ACCEPTANCE_PENDING；真实 workspace_get 的 ACCEPT available=true，CurrentExecutionAcceptance=WAITING_ACCEPTANCE，commitStatus=UNCOMMITTED。尚未验收或交付。

38. 任务验收意图已准备：confirm_1a6bdead3fc365c1a9899d314d7d0bb8，purpose=EXECUTION、action=ACCEPT、version=1、PENDING，factsComplete/presentationReady=true。该候选只引用当前准确 Execution / revision / Decision；不携带替换工作规格、验证规格或运行配置。正式验证证据另存 build/full-flow-verified-evidence.json。

39. 原 Work 对话只读展示同一验收意图；首次宿主组件装载失败，点击“重试”后真实显示“待确认”和已启用的“确认验收”按钮。任务标签页保持打开，显示“等待验收”，审查、审批、实施、验证均完成。截图 build/full-flow-preview/task-acceptance-confirmation.png。尚未提交用户验收，等待真实用户点击。

40. 用户点击“确认验收”。同一 intent 回读 COMPLETED / CONSUMED，正式提交请求 confirm_7bfe8be9-b716-4489-981a-978eecd63059；RESOLVE_ACCEPTANCE_DECISION 子请求 confirmation_d13c63fad225c41f27feb728ce36f853 已完成。原文件结果 accepted=true，CurrentExecutionAcceptance=ACCEPTED。
41. Core 自动 Git finalization 完成：commitStatus=COMMITTED，commitSha=75e03db829447c7c8a93f76b2d84fab2849585ba，Task=SUCCEEDED / DELIVERING（保留末阶段），pending=false。不是以验收 accepted 推定提交成功，而是独立读取 Task 和正式 task_commit_receipts 的 APPLIED / completed_at。
42. 原 Work 对话真实收到任务完成通知并只读回查，明确展示验收及提交 SHA；任务标签页仍打开，五阶段均完成。截图 build/full-flow-preview/task-accepted-committed.png。该任务 devIntegrationStatus=UNMERGED，开发分支仍为原 HEAD，不能将任务提交等同开发分支交付。
43. MERGE_FEATURE_AREA 准备请求 full-flow-final-feature-area-delivery-20261021 返回 REJECTED_NOT_PREPARED / CONFIRMATION_FACTS_UNAVAILABLE，没有生成合并 intent 或触发 Git。只读诊断确认旧 Task task_847efa4f2a3e2863504d896dc7308831 已有正式 TASK/NO_BACKEND 取消回执，只有终态 Upper 审查及 CANCELED/CLOSED 只读 grant，无 Execution、Direct File 授权或提交；现有 deliveryRelevantTaskSQL 因存在 Review/Run 而仍计作未完成交付。
44. 收窄交付就绪谓词：允许正式 TASK 无后端取消、全部 Upper 只读审查都有准确终态 grant/Run 绑定、无 Thread/Turn、无 Execution/Direct File/提交/不确定取消的旧任务退出阻塞。保留旧任务、审查及取消历史；不写生产数据库。增加真实取消、重启、冻结确认和合并回归，以及17种准入边界测试。另修正终态 Task 的标签优先显示 SUCCEEDED/CANCELED/FAILED，避免保留 DELIVERING 阶段导致成功任务仍显示“交付中”。
45. 针对回归通过：取消只读审查、重启和真实合并测试 5.536s；原无审查取消场景及部分交付取消阻断组合回归 32.894s；17种 SQL 准入边界 0.639s；UI 单文件109项测试全部通过。三服务构建成功。生产数据库通过只读连接备份至 build/full-flow-update-20261021-13/state-before-14.db，Desktop 托管停启部署 v14，Core/MCP/本地MCP 均 ready；bridge_diagnostics 确认 v14。
46. 同一领域交付准备请求重试成功，仅产生 PENDING intent=confirm_d29fcb1d1c16b8162474633e3bcbe927，purpose=FEATURE_AREA、action=MERGE_FEATURE_AREA、version=1，contentHash=e18634fc22bf92e6e01d82379d006b74827d7255d83503814f2658d1ad525af3，factsComplete/presentationReady=true。冻结计划只有一个仓库及已验收提交75e03db，sourceHead同上；目标 refs/heads/codex/skeleton-v18-baseline，expectedDevHead=fd5fccdd5c0a07c6808bdc50155a59839fdc3bcf。尚未提交合并确认或执行 Git 合并。验收/任务提交/待确认合并原始证据另存 build/full-flow-accepted-committed-evidence.json。
47. 原 Work 合并卡片实测显示“待创建领域”，并丢失项目名称。定位到 readDisplay 未把 featureArea 自身纳入准确身份校验、objectName 将所有 FEATURE_AREA 当作创建、modeLabel 对纯决策使用“创建内容”。修复为按本 intent 的准确领域身份接受只读名称，合入操作显示已有领域名称及“合入已验收提交”；不改冻结内容、资格或确认状态。通过真实 HTTP MCP 重新捕获共享卡片夹具，45项相关 Node 测试及 MCP 卡片资产/实例测试0.889s通过，三服务构建和托管部署v15成功。原待合并 intent 仍为同一 PENDING，未重新 prepare 或替用户合并。
48. v15 实测宿主仍缓存 v6 组件，未将单纯服务升级当作前端修复成功。将新确认资源升级为 ui://devbridge/confirmation/v7 / PURPOSE_CONFIRMATION_V7，保留 v6/v5 兼容资源；重新通过正式 MCP 测试捕获实例、未消费拒绝、无副作用拒绝三个夹具。90项确认 Node 测试、资产与实例 Go 测试0.885s通过。三服务部署 v16，Desktop 显示 ready；同一原合并意图保持不变，等待新组件实际装载。
49. 新资源只读展示后仍实际加载旧文案，进一步确认宿主工具元数据缓存未同步；没有把资源版本递增当作已解决。通过 ChatGPT 已连接 devBridge-test 的插件设置执行“刷新工具”，未卸载、改权限或改变 CSP。业务目录和冻结意图保持不变；等待宿主刷新完成后回到原对话核验。
50. 宿主刷新工具后回到原对话，重新只读展示同一 intent；首次装载失败后点击“重试”，最新真实 UI 显示“DEV BRIDGE skeleton / 待确认”“合入领域交付 / Core 依赖边界一致性”“执行方式：合入已验收提交”，已启用“确认逐仓合入”。现有旧消息卡片保留原缓存文案，未重写历史。重新打开同一 Task 的 workspace 展示恢复侧边任务标签页；未提交合并或推送。
51. 交接前准确 confirmation_get 再读该合并意图仍为 PENDING，无提交回执。截图 build/full-flow-preview/feature-area-delivery-confirmation.png 同时包含准确领域名称、开发分支、不推送远端及“确认逐仓合入”按钮，侧边任务标签仍打开。旧进度组件的“交付中”文案仍来自宿主缓存；权威 Task 为 SUCCEEDED，领域合入是独立未确认操作，不据旧标签推断任务未验收。
52. 用户点击“确认逐仓合入”。同一 intent 的提交请求 confirm_e2b0dfa4-1473-4a6a-b372-91e546186c7e 已 CONSUMED；子请求 confirmation_b79b825ed72d0f4ee6fd5fea5e524b92 已保留冻结计划。确认投影为 UNKNOWN / PENDING，不能以已消费推定 Git 合并成功。正式 merge_delivery_outcomes 对 operation=merge-initial/b63a2ee5d14d908ecd47b1b0ece3b1548e1927c2f8e4f733f177948e876b6a5e 记录 REJECTED_NOT_CONSUMED、before_external_effect=1、error=WORKSPACE_DIRTY，merge_oid为空；目标开发分支 HEAD仍为fd5fccdd5c0a07c6808bdc50155a59839fdc3bcf。
53. 正式 feature_area_merge_retry_candidates 读回准确拒绝，canRelease=true；没有自动换请求重试、释放计划或把 UNKNOWN 当成可派发授权。Task仍SUCCEEDED / COMMITTED，devIntegrationStatus=UNMERGED；领域合入父/成员回执尚未结清。
54. 由实际 Codex CLI gpt-6.1-sol / low 执行独立只读 Git 诊断（不是替换正式验证Run），Thread=01a15f10-68f2-7510-bb24-32ee81ff5ffa。仅5条只读命令且均设置GIT_OPTIONAL_LOCKS=0，退出码均为0；发现目标分支80个未暂存修改、33个未跟踪文件，无已暂存改动，且与本任务两文件没有路径重叠。没有运行测试、构建或Git写操作。清单 build/full-flow-target-dirty-files.txt，完整诊断 build/full-flow-merge-dirty-diagnostic-result.txt，事件 build/full-flow-merge-dirty-diagnostic.jsonl。
55. 用户明确选择“我先整理这些改动，再继续合入”。合入工作停止在此检查点；全部113个文件保持原样，不执行stash/clean/reset/commit/merge。待用户通知整理完成后，先重新读取原拒绝回执、开发分支HEAD与工作区状态，再按现有准确拒绝恢复协议处理；若HEAD或冻结计划变化，须重新准备并由用户确认，不能直接重放已拒绝的Git派发。
56. 用户随后要求“进行合并”。重新读取开发上下文确认目标分支HEAD仍为fd5fccdd5c0a07c6808bdc50155a59839fdc3bcf；原准确拒绝候选仍可读。按本次明确授权调用 feature_area_merge_reprepare，requestId=full-flow-user-authorized-merge-retry-20261021-1，未替换原计划或目标；预检查仍拒绝，服务正式日志errorCode=WORKSPACE_DIRTY。没有创建新merge_delivery_operations或改动开发分支，原拒绝回执保持不变。
57. 继续同一实际Codex只读诊断会话重新检查：仍为80个未暂存修改、33个未跟踪文件，无本任务两文件路径；目标分支HEAD未变。未收存、清理或提交这些改动。当前等待用户明确选择是否授权暂存本任务范围外的113个现有文件，或由用户自行整理。新诊断事件build/full-flow-merge-dirty-diagnostic-current.jsonl及结果build/full-flow-merge-dirty-diagnostic-current.txt。
58. 用户在明确询问临时收存与恢复后要求“你进行合并”，授权本次收存、合入与恢复。Codex重新核对113条完整状态、分支、HEAD，无适用AGENTS.md及进行中Git操作，并保存逐文件SHA256/类型/权限manifest。CLI内层将.git设为只读，未执行stash；父进程经自动审核授权后按完全相同清单和指纹重核，执行一次include-untracked收存，stashOID=8a50e7ba6a3865aa3b4b4748af175a7fe43a6bc2。stash准确覆盖80个tracked及33个untracked文件，工作区干净，HEAD和分支未变；manifest及回执在build/full-flow-pre-merge-stash-*.json。
59. 经用户本次明确授权调用正式 feature_area_merge_reprepare，requestId=full-flow-user-authorized-merge-retry-20261021-2。新准确operation=merge-retry/ea83309066d9dee36a7176504a257d558d1e24ad19fcf31c8fc6fe7fcb6aa6e8，沿用原冻结计划、目标及已验收提交；Core完成Git合入，持久化outcome=APPLIED，merge_oid=75e03db829447c7c8a93f76b2d84fab2849585ba。原父回执completed_at=2026-10-21T15:33:17.758318Z，成员COMPLETED，无last_error。原最初WORKSPACE_DIRTY拒绝历史保持不变。
60. 父进程经授权使用准确stashOID执行git stash apply --index，exit=0；没有pop/drop，保留恢复备份。113个文件恢复后SHA256、类型、权限、完整porcelain状态与收存前一致；仍为80未暂存、33未跟踪、0暂存。任务两文件相对新HEAD无差异。回执build/full-flow-restored-user-changes-receipt.json=RESTORED_AND_VERIFIED。
61. 实际Codex独立进行合入后只读核验，确认新HEAD/分支、全部113个文件SHA256/类型/权限、完整Git状态、任务两文件无差异及stash备份均与回执一致。核验通过；未额外运行测试或构建，不声称整个项目全部测试通过。完整结果build/full-flow-post-merge-codex-verification.txt，原始事件build/full-flow-post-merge-codex-verification.jsonl。
62. 独立workspace_get回读Task=SUCCEEDED、commitStatus=COMMITTED、devIntegrationStatus=MERGED、Dev已合入75e03db，无blockers和pendingDecisions，nextAction=WORKFLOW_COMPLETE。原合入confirmation_get回读COMPLETED；子步骤state=COMPLETED，保留历史effect=UNKNOWN，不以该历史字段替代真实APPLIED合入结果。最终证据build/full-flow-completed-evidence.json。全过程未推送远端。
63. 原Work对话按准确目标只读回查，实际回复“领域交付完成”，明确Task SUCCEEDED、新开发分支HEAD和无阻塞/待决定。任务侧边标签仍打开，没有返回成内联进度卡片。旧组件仍可能以保留phase显示“交付中”，旧确认详情也保留原预约不确定文案；这些历史/缓存文案不是当前Git结果，正式Core回执及当前workspace的MERGED为准。该展示局限保留记录，不宣称已在旧实例热更新。

64. 用户要求修改完成文案。任务终态优先于保留的业务phase：SUCCEEDED显示“已完成”、FAILED显示“已失败”、CANCELED显示“已取消”；活动任务DELIVERING仍为“交付中”。统一展开、收起、悬浮/标签页显示，终态不触发返回对话。工作区资源升级为read-only-terminal-status-v8并保留v7及更早别名，解决旧宿主组件缓存复用。
65. 定向界面测试119项通过；扩大只读Card回归160项中157通过、3项既有跳过、0失败。修复独立抽取renderReadOnlyFacts的测试夹具，使其加载真实taskPhaseLabel依赖。Go资源兼容测试通过，diff检查通过；三服务构建部署v17，bridge_diagnostics确认ready。已连接ChatGPT插件执行“刷新工具”，原Work对话仅只读重新展示同一已完成任务，业务状态未改。
66. 原Work对话新版workspace首次宿主加载失败，使用该展示的“重试”后成功。真实侧边标签页显示准确任务标题、“已完成”及五个COMPLETE步骤，未返回内联卡片；截图build/full-flow-preview/completed-task-wording-v8.png。旧历史消息仍保留原缓存组件，未重写历史。只读workspace_get再次确认Task SUCCEEDED、开发分支75e03db；本次没有执行任务、确认或Git操作。
