# Frozen Skeleton：取消接续与交付回执生产修复

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-1ebb7e4db7670e0c)。

日期：2026-10-01。任务类型：`IMPLEMENT_FROZEN_SKELETON`。

## 契约基线与范围

- Freeze commit：`4406e717949af5bce6547af0b71525b74e206c81`
- Skeleton tree OID：`942d056a9c78a7e1b28319f4e535bdc893b3509a`
- 契约修改已单独提交；本批生产实现期间 `skeleton/**` 只读。
- 工作区进入本批前已有大量生产修改。本批仅新增下表所述改动，不代表既有未提交实现已全部验收。

本批修复两个已复现的生产缺陷，并补充 READY 取消的真实重启窗口测试。它不宣称整个生产系统已经符合全部 Skeleton 契约。

## 修复与证据

### PREPARING 无 Run 取消后可继续接续

真实 successor command 会把 Task 置为 `IMPLEMENTATION_PREPARING`。旧投影器和启动扫描只接收 `IMPLEMENTATION_READY` / 历史 `RECOVERY_REQUIRED`，导致 successor 在创建 Run 前取消后，取消回执已完成，但 Task 永远留在 PREPARING。

`internal/storage/sqlite/no_run_successor_facts.go` 的即时投影与启动扫描均加入 PREPARING。共享事实条件保持不变：精确的当前 Execution、Task revision、已完成的 `CANCELED / NO_BACKEND / NOT_SENT` 回执，以及不存在 Run、preparation attempt、binding、start authority、RecoveryStatus 或该 predecessor 已有的 successor effect authority。

对应冻结契约：

- `execution.cancel_admitted`
- `task.reconcile_no_run_cancellation`
- `task.create_no_run_successor` / `task.replay_no_run_successor`
- `INV-ADMITTED-NO-RUN-SUCCESSOR`
- `SCN-NO-RUN-SUCCESSOR-RESTART`、`SCN-NO-RUN-SUCCESSOR-COMMIT`、`SCN-NO-RUN-SUCCESSOR-EXACT-REPLAY`

生产证据：`TestNoRunSuccessorPreparingCancellationContinues` 通过真实 command 形成 PREPARING，分别验证即时投影及关闭数据库后运行完整 recovery service。修复前两分支分别报 conflict、重启后仍为 PREPARING；修复后均可创建下一代 successor。测试比对 Task 全列仅 phase 改变、历史回执及前驱记录不变、重放不重复写入，并确认旧取消 owner 不能创建 Run。

另修复 `TestPrepareSuccessorExecutionCreatesOneSuccessor` 的空查询误通过：preparation attempt 检查使用返回的真实 RuntimeScopePlanID，增加同 Plan 下 binding / start authority 的零计数。

### READY 取消事务与 Task 投影之间的重启窗口

`TestReadyNoRunCancellationProjectsAfterRestartBeforeFirstSuccessor` 使用实际生产 admission 获得 READY。只提交 cancellation store 的 `Begin` 事务，确认 Task 未更新且尚无 eligibility，然后对同一 DataRoot 关闭并重新打开 ControlPlane。

测试证明启动补偿只改变 Task phase，保留取消回执、Execution、admission、Plan 和 preparation authority；第二次重启及取消请求重放稳定；随后首个 successor 命令只创建一次效果，保持 revision，且不产生 Run / attempt / binding / start authority / backend dispatch。

对应 `SCN-NO-RUN-READY-CANCEL-RESTART-SUCCESSOR` 的取消、恢复和接续子路径。当前 fixture 通过 `APPROVE` 加 admission 到达 READY，**没有验证该场景前置的完整 APPROVE_AND_START T1/T2/T3 协议**。

### 已完成交付优先返回持久化结果

旧 `RetryTaskCommit` 在读取完成回执前检查当前 live admission。v1 交付完成后出现新的 Direct File writer 时，重试及 ACCEPT replay 会误报 `DIRECT_FILE_CAPABILITY_ACTIVE`；v2 跳过已完成成员后再次 finalize，返回 `RECOVERY_REQUIRED`。

`internal/storage/sqlite/git_commit_store.go` 在 Project + Task 归属读取成功后，查询同 Task / FeatureArea 的 `completed_at IS NOT NULL` 回执并直接返回。会话权限、Project 归属及数据库 writer fence 保留。未完成及 UNKNOWN 记录仍走原路径。

生产测试：

- `TestCompletedTaskDeliveryReceiptPrecedesLiveAdmissionAndAcceptanceReplay`：v1、live writer 变化、workspace/member 变化、冷重启、ACCEPT replay、跨项目拒绝。
- `TestCompletedMultiTaskDeliveryReceiptReplaySurvivesRestartAndMemberDrift`：v2、冷重启、workspace/member 变化、ACCEPT replay。

两者均在完成后用 Git 探针确认零 Git 调用，并比对完成时间及持久化结果不变。旧实现两个测试均失败，修复后通过。

完整回归发现 `TestPostOrphanGitDirectFileExclusion` 原先把已完成 Task 的重放也当作新写入。现改为验证原结果、receipt 和两端 HEAD 不变，同时保留 FeatureArea 新提交受 Direct File 限制的断言。新增 `TestIncompleteTaskDeliveryReceiptStillRejectsDirectFileGrant` 使用真实 checkpoint fault 留下未完成回执，再注入 retained grant，验证未完成重试仍被拦截且零 Git 调用；释放 grant 后恢复原 commit。

关联冻结契约的 `delivery.read_unknown`、`delivery.read_unknown_merge`、`delivery.read_unknown_finish` 及 `SCN-DELIVERY-RESPONSE-LOSS` / `SCN-DELIVERY-MERGE-UNKNOWN-READBACK` / `SCN-DELIVERY-FINALIZE-UNKNOWN-READBACK` 所要求的“已知效果不重复执行”。这只是现有 completed Task receipt 的重放修复，**尚未实现这些场景要求的三阶段精确 operation receipt 读取器**。

## 本批文件

| 文件 | 本批变更 |
| --- | --- |
| `internal/storage/sqlite/no_run_successor_facts.go` | 两个阶段条件加入 PREPARING |
| `internal/storage/sqlite/no_run_successor_command_test.go` | 精确 Plan 查询及无运行时效果断言 |
| `internal/storage/sqlite/no_run_successor_preparing_test.go` | 新增 PREPARING 即时和重启回归 |
| `internal/localmcp/no_run_ready_projection_restart_test.go` | 新增 READY 真实启动补偿测试 |
| `internal/storage/sqlite/git_commit_store.go` | completed receipt 提前返回 |
| `internal/storage/sqlite/task_delivery_completed_replay_test.go` | 新增 v1 完成回执重放回归 |
| `internal/controlapi/task_delivery_completed_replay_test.go` | 新增 v2 完成回执重放回归 |
| `internal/storage/sqlite/post_orphan_git_test.go` | 更新 completed receipt 旧断言，保留新提交排他性 |
| 本文 | 冻结基线、映射、验证与剩余缺口 |

## 验证记录

所有 Go 命令使用 `GOCACHE=/private/tmp/devbridge-skeleton-audit-go-cache`。

- 生成物及实际 `Task.phase` parity：`make generate-check production-parity` 通过。
- SQLite no-Run / successor / cross-lineage 定向回归：11 个顶层测试通过。
- SQLite + Core API 交付、验收、故障恢复定向回归：28 个顶层、48 个测试节点通过。
- READY 新增重启测试通过；首次在受限沙箱中因无法监听 `127.0.0.1` 退出，获得本机监听权限后通过。
- 两个实现方向已本地交叉审查；交付审查还独立运行新增测试及既有 partial、atomicity、checkpoint loss、snapshot/member drift 负向回归，通过。
- 冻结契约完整 `make check GO_TEST_FLAGS=-json`：**通过，1532 个测试节点零失败、零跳过，主测试包耗时 1823.140 秒**；格式、source/canonical validation、coverage、`go vet` 和生成物 checksum 校验全部通过。CLI / examples 两个没有测试文件的包不计作测试节点。
- 三个受影响生产包完整回归：最终 **1504 个测试节点通过，67 个环境依赖节点跳过，零失败**。SQLite：974 通过（132.788 秒）；Core API：406 通过、62 跳过（544.355 秒）；MCP：124 通过、5 跳过（379.232 秒）。未选择的 App Server / candidate binary 及显式 opt-in 真实模型测试没有运行，不计作通过。
- 首次三包检查只有上述 post-orphan 旧断言失败；修正并加入未完成回执负向测试后，SQLite 包完整重跑通过。Core API 和 MCP 在首次完整运行中通过。
- 受影响三个包 `go vet` 通过；后续 SQLite 测试修改后的该包 `go vet` 也通过。
- Skeleton 只读证明通过：470 个文件与实现前 SHA256 基线一致；`git status --porcelain -- skeleton` 无 staged / unstaged / untracked 变化；freeze commit 是 HEAD 的祖先，HEAD 的 Skeleton tree OID 精确匹配；该 freeze commit 的变更路径全部位于 `skeleton/`。
- 与本批开始时的文件哈希清单比较，只有上表 9 个生产、测试及文档文件变化。`git diff --check`、相关 Go 文件格式及新增文件空白检查通过。生产变更保持未提交，原有其他工作区修改保留。

完整生产回归命令：

```sh
go test ./internal/storage/sqlite ./internal/controlapi ./internal/localmcp -json -count=1 -timeout=60m
# 修正 related post-orphan 旧断言并新增负向覆盖后：
go test ./internal/storage/sqlite -json -count=1 -timeout=60m
go vet ./internal/storage/sqlite ./internal/controlapi ./internal/localmcp
go vet ./internal/storage/sqlite
```

## 尚未落地的流程能力

以下是独立后续工作，不纳入本批通过范围。

1. **APPROVE_AND_START T1/T2/T3 部分进度台账。** 当前 confirmation 路径仍先 APPROVE，再 START_IMPLEMENTATION 并调运行时。READY 子路径测试不能替代三阶段 authority、部分提交、冷重启及精确回执证据。
2. **无 Run 状态的修订出口与取消 Task 验收（后续批次已处理）。** 本批结束时，guidance 暴露 `STOP_AND_REVISE`，但 v95 的 no-run phase exit trigger 仅允许有 successor authority 的 PREPARING 出口；READY / NO_RUN_SUCCESSOR_REQUIRED 的 CANCEL_TASK 也缺少专门验收。随后新增真实修订命令与 v96 迁移，并验证取消、重启、新 Review 审批和启动，见[后续验证记录](skeleton-frozen-no-run-revision-2026-10-01.md)。该后续结果不计入上面的本批测试数字。
3. **准备失败后的安全放弃。** `SCN-PREPARATION-ABANDON-*` 要求对未绑定、未派发 Run 的精确 owner settlement、不可变回执、重放冲突以及 UNKNOWN 禁止写入。需独立映射生产命令、持久化约束和故障窗口。
4. **同一 Run 的验证补证与验收决策失效。** 现有 `verification_result_json` 有不可变 trigger，验收 Decision ID 按 Execution 固定。`SCN-VERIFICATION-SUPPLEMENT-INSUFFICIENT` / `SCN-VERIFICATION-SUPPLEMENT-ACCEPTANCE` 需要追加式证据版本、决策绑定版本以及旧决策原子失效，不能覆盖旧 report。
5. **三阶段交付 operation ledger。** 生产尚无冻结契约要求的 COMMIT / MERGE / FINALIZE 十字段精确身份回执，现有 Git 恢复也可能继续执行效果。还缺预效果拒绝回执、REPREPARE_REQUIRED、按 predecessor operation + stage 唯一的新授权和 CAS。不得用当前 Task retry 的成功结果宣称这些场景通过。

三阶段 ledger 落地前存在需要单独明确的契约映射：NO_CHANGE / READ_ONLY 没有新 commit SHA；FeatureArea merge 聚合多个 Task，而冻结 operation identity 包含单一 Task / Execution；Git 效果已发生但回执未落库时，不能仅凭 HEAD 为 UNKNOWN 合成精确回执。若实现必须改变冻结契约，应按 `SKELETON_CHANGE_REQUIRED` 进入独立契约修改并重新冻结，不能在本批偷偷放宽 guard。

当前 production parity 仅证明实际 `Task.phase` 词汇与冻结契约匹配；不证明 actions、errors、所有运行时转换或上述能力已符合契约。
