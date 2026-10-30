# Frozen Skeleton：无 Run 修订出口与 Task 取消

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-ce6551ad3d866f4e)。

日期：2026-10-01。任务类型：`IMPLEMENT_FROZEN_SKELETON`。

## 基线与范围

- Freeze commit：`4406e717949af5bce6547af0b71525b74e206c81`。
- Skeleton tree OID：`942d056a9c78a7e1b28319f4e535bdc893b3509a`；`skeleton/**` 保持只读。
- 本批接续[上一批验证记录](skeleton-frozen-production-recovery-2026-10-01.md)中的第 2 项缺口。
- 进入本批前已有大量未提交生产修改；以下结论仅覆盖本批修订、取消及其受影响路径，不代表完整生产流程通过全部冻结契约。

## 实现与契约映射

### STOP_AND_REVISE 只修改 Task phase

新增业务命令 `StopAndReviseNoRunTask`、HTTP `POST /v1/tasks/stop-and-revise` 及 GPT / 本地 MCP `task_stop_and_revise`。请求必须提供精确 Task、取消前驱和预期 definition revision；调用者来自服务器认证的 Session 或本机 Operator，客户端不能指定 actor。

命令在一个写事务内检查 Active Development Task、当前身份、已完成的无后端取消、无 Run / preparation attempt / binding / start authority，以及未被 successor 消费的精确 eligibility。成功只把 `NO_RUN_SUCCESSOR_REQUIRED` 改为 `REVIEW_REQUIRED`：不清除 canceled predecessor，不改 revision、时间戳和其他 Task 列，不新建 receipt、Execution、Run 或恢复记录。同一身份在 `REVIEW_REQUIRED` 重试为纯读；后续 Review 推进后须刷新。

映射：`task.stop_and_revise_no_run_successor`、`SCN-NO-RUN-STOP-AND-REVISE`、`SCN-NO-RUN-OP-STOP-AND-REVISE`。生产 `REVIEW_REQUIRED` 对应冻结契约的 Task Planning 阶段。

### 新 Review 批准后才能替换取消前驱

v96 迁移保留 v95 已有保护，在精确取消事实和新 Review 序号下开放修订路径。STOP 与新 execution pointer 切换分别要求除 phase / pointer 外所有 Task 列不变。新 approval、materialization、resolution receipt 和 definition revision 必须属于同一 Task / Project / current Review；已有普通审批、补充 Review 与 successor 的约束继续生效。

v1 通过新 Review 审批创建新 revision / candidate；v2 的 `NO_START` 只批准新定义并保留取消前驱，后续显式 `START_APPROVED` 再创建执行。确认预检校验精确的新审批事实，并使用新 Task 的 workset；新启动可选择新的 runtime / backend，不沿用旧取消执行的锁定配置。

显式启动已提交 effect、但确认步骤 receipt 写入失败时，只允许原 actor / action / request 与原确认内容的完整 expected identity 找回新 candidate，再读取既有 runtime handoff receipt。不同请求、修改后的预期身份不能借用旧取消记录。这一恢复不创建第二个执行，也不根据最新一条历史推测结果。

相同校验也覆盖 Run 创建前的故障窗口：Task 仍 READY、新 candidate 已 ADMITTED、尚无 Run 时，原请求不能把预期前驱改成现在的 candidate 来绕过校验。保留原身份的请求可继续完成唯一 Run 的创建。

Execution 历史读回的 `ReviewApprovalID` 改为从该执行绑定的不可变 revision 读取。无 revision 的历史记录保留原 fallback，避免旧取消执行在 Task 重新审批后显示新审批 ID。

旧且尚未消费的 successor 请求在 Task 版本变化后优先返回 `TASK_NOT_CURRENT`，不产生 receipt 或 successor 效果；既有 request receipt 的重放仍优先读取。

映射：`SCN-NO-RUN-OP-REVISION-INVALIDATES-OLD-IDENTITY`、`task.reject_stale_no_run_expected_identity`。测试验证禁用 backend 后旧请求仍优先被身份 guard 拒绝；没有新增 authority-reader 计数探针，零当前 authority 读取的证据是代码中身份检查先于该 reader 的调用。

### Task 取消与旧取消回执重放

Task cancellation 的未完成 Execution 检查加入 `ADMITTED`，阻止尚未结清的 admitted owner 被 Task 取消绕过。已结清的 READY / NO_RUN_SUCCESSOR_REQUIRED 可以取消 Task，保留 phase、revision、current pointer 和原 Execution cancellation receipt。

原 Execution 取消的后置投影在 Task 已终态、current owner 已切换、新 Review 已推进或新定义已批准时保持只读，不把业务状态退回旧接续流程。

异常路径测试还复现了新 Review 恢复被旧取消事实遮蔽的问题。boot 的豁免现只排除精确已结清的旧 Execution cancellation，不再排除整个 Task 的其他恢复事实；新 Review 的恢复状态也不会被旧取消投影覆盖。新 Review 的 REVISE 入口在精确修订身份下保留取消前驱，并继续使用原有 Review admission 检查。

映射：`SCN-NO-RUN-READY-CANCEL-TASK`、`SCN-NO-RUN-SUCCESSOR-CANCEL-TASK`。READY fixture 通过实际生产 APPROVE 与 admission 建立前置状态；不以此宣称完整 APPROVE_AND_START T1/T2/T3 前缀已实现。

## 验证证据

- HTTP / GPT MCP / 本地 MCP 真实适配器：phase-only、错身份零写入、同身份重试、原取消回执重放、同 DataRoot 两次关闭重启、零 backend 启动。
- 五类真实拒绝路径：未取消的 ADMITTED、已有 Run、Task 已取消、错误 caller scope、缺少 Operator Access；完整持久化及 runtime 快照不变。
- STOP 与 successor 顺序竞争及并发竞争：只有一个赢家，不改原 revision、取消历史，不创建 Run。
- v1 新 Review → 新 approval → 新 revision / execution；审批重放及数据库冷重启稳定，旧记录和旧读回值不变。
- v2 新 Review → `NO_START` → 显式 `START_APPROVED`；使用不同于旧执行的合法 runtime，生成新执行，原 commit receipt 可重放。
- 确认步骤 COMPLETED 写入故障：execution / Run / start effect 已提交时，原请求的纯读取与重试找回既有结果；不同请求被拒绝，持久化对象数量不变。
- Run INSERT 前故障：保留 READY + ADMITTED + 无 Run 的真实中间状态；原请求六字段篡改均拒绝，正确重试完成唯一 Run。仅在临时 overlay 撤回父 guard 时，该测试精确失败；真实源文件保持修复，两个故障窗口均通过。
- v95 带取消与 eligibility 数据升级到 v96，重复打开数据库不改历史；保留独立的 v94→v95 迁移测试。
- SQL 负向检查：STOP 夹带 timestamp / pointer / revision / semantics 写入、已消费 eligibility、无新 Review、缺失审批 receipt / materialization 均拒绝并回滚。
- 新 Review 的真实恢复记录与 FURTHER_REVIEW_REQUIRED / REJECT / REVISE 出口：重复旧取消投影及 boot lifecycle reconcile 保持当前业务阶段、恢复记录及历史。
- GPT 工具发现目录、闭合 schema、文档接口目录一致。

所有 Go 命令使用 `GOCACHE=/private/tmp/devbridge-skeleton-audit-go-cache`。七个受影响包完整回归记录如下；合计 **2252 个测试节点通过、67 个环境依赖节点跳过，最终各包运行零失败**。没有运行的 App Server / candidate binary / opt-in 真实模型测试不计为通过。

| 包 | 通过节点 | 跳过节点 | 耗时（秒） |
| --- | ---: | ---: | ---: |
| `internal/storage/sqlite` | 994 | 0 | 150.305 |
| `internal/controlapi` | 407 | 62 | 420.921 |
| `internal/localmcp` | 138 | 5 | 317.351 |
| `internal/execution` | 301 | 0 | 53.245 |
| `internal/bridgehttp` | 82 | 0 | 70.673 |
| `internal/mcpserver` | 328 | 0 | 38.849 |
| `internal/workflowprogression` | 2 | 0 | 0.541 |

完整回归之后，最终收窄的原请求身份守卫、Run 创建前窗口和相关 Task 启动另作定向回归：**50 个节点通过、零跳过、零失败**（SQLite 45 / 7.539 秒，Core API 5 / 6.694 秒）。此前确认 / Review / no-Run 定向回归 62 个节点通过，独立复核的最终修订链路 15 个节点通过；这些重叠记录不与上表重复累加。最终七包 `go vet`、34 个本批 Go 文件格式和 `git diff --check` 均通过。

完整回归最初暴露两项测试兼容问题，已修正并定向通过：历史 v42→v43 测试构造旧表结构前须先移除引用 checkpoint 列的后续触发器；新增公开修订命令后 GPT 工具数量由 57 更新为 58。生产 SQL 保护没有为迁移测试放宽。

冻结证明已通过：470 个 Skeleton 文件与本批开始前 SHA256 相同；无 staged / unstaged / untracked Skeleton 变化；freeze commit 是 HEAD 祖先，HEAD 中 Skeleton tree OID 精确匹配。`make generate-check production-parity` 通过。上一批已运行完整 Skeleton `make check`（1532 节点通过），本批未重复执行未修改的契约长测。

与本批入口哈希清单相比，共 37 个生产、测试和文档文件变化，无文件删除；既有其他工作区修改保留。本批涉及的生产入口、v96 migration、Review 审批交接、Task 启动、取消投影、恢复及其测试均在上述回归范围内。

主要验证命令：

```sh
go test ./internal/bridgehttp ./internal/mcpserver ./internal/workflowprogression -json -count=1 -timeout=30m
go test ./internal/controlapi -json -count=1 -timeout=60m
go test ./internal/execution -json -count=1 -timeout=30m
go test ./internal/storage/sqlite ./internal/localmcp -json -count=1 -timeout=60m
# 修复两项测试兼容问题后分别完整重跑：
go test ./internal/storage/sqlite -json -count=1 -timeout=30m
go test ./internal/localmcp -json -count=1 -timeout=30m
go test ./internal/storage/sqlite ./internal/controlapi -run '(NoRun|TaskExecution|ApprovedExecution|StartApproved|StartEffect|DefinitionOnly)' -json -count=1 -timeout=20m
go vet ./internal/storage/sqlite ./internal/controlapi ./internal/localmcp ./internal/bridgehttp ./internal/mcpserver ./internal/execution ./internal/workflowprogression
```

## 边界与后续

本批不实现完整 T1/T2/T3 部分进度台账、preparation 安全放弃、追加式验证补证和旧验收失效、COMMIT / MERGE / FINALIZE 三阶段 operation ledger。上一批记录对这些缺口及 NO_CHANGE / FeatureArea merge 契约映射的说明继续适用。

新增跨前驱启动恢复使用持久化 confirmation 内容证明原 expected identity。独立 Operator 启动优先读取已有精确 Operator receipt；如果效果后该 receipt 尚未提交且没有确认内容证明原身份，继续保守拒绝，不推测其前驱，也不把本批结果当作该独立窗口的完整恢复验收。

生产变更保留在工作区，未提交；不会将旧有其他未提交修改混入一次批量提交。
