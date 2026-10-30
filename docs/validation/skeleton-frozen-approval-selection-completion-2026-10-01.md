# Frozen Skeleton：显式启动、确认终态与审查并发准备

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-541750c9cac8e0bf)。

日期：2026-10-01。任务类型：`IMPLEMENT_FROZEN_SKELETON`。

## 基线与范围

- Freeze commit：`4406e717949af5bce6547af0b71525b74e206c81`。
- Skeleton tree OID：`942d056a9c78a7e1b28319f4e535bdc893b3509a`；`skeleton/**` 只读。
- 接续[平台资格持久化基础](skeleton-frozen-runtime-platform-foundation-2026-10-01.md)。进入本批前的其他未提交修改保留。

本批最初核对 revision runtime selection binding 的生产来源，发现现有记录还缺少原提交冻结的资格身份、Core platform issuance 及完整不可变 formal source archive。`confirmation_commit_requests.outcome=CONSUMED` 只证明请求已经消费，不等同于业务完成，不能用“有审批回执 + 事后选当前资格”补造来源。

核对过程中发现三处生产缺陷：定义-only 审批继承的 Review runtime 会覆盖后续明确选择；已完成确认可被迟到写入降级；前台与后台续接会并发准备同一 Upper Review 工作树。以下改动处理这些选型、终态和并发准备问题，不发放平台 selection source 或 revision binding。

## 定义-only 后的启动使用当前请求选择

v2 Coding 的 `NO_START` 不创建获准执行的 candidate。历史审批 materialization 可能保留 Review 使用的 runtime/backend，它们不能据此成为后续 Implementation 的选型授权。

新实现区分审批事务中的 runtime-authorized candidate 创建和后续第一次显式启动。后者从精确 AI Session、当前 `START_IMPLEMENTATION` 步骤请求、`START_APPROVED` intent 和原始消费回执读取选择，核对原 normalized commit hash、frozen content hash、variant、Task/revision/phase/current pointer 及完整 expected identity。来源必须唯一；删除旧的 `LIMIT 1` 任意匹配和“旧 runtime 字段为空才读新选择”的判断。

已有 runtime-authorized candidate 沿用原锁定选择。v1 既有路径保留。v2 definition-only 若没有本次明确选择来源，普通 Operator / 原始启动入口拒绝新建执行，不能借用另一份 confirmation 或 Review 的审计字段。具体兼容边界：`StartTaskExecutionInstructions` / `ReserveTaskExecutionInstructions` 没有本次 runtime/backend 选择参数，不能作为这种 v2 首次启动入口，需走 `START_APPROVED`。原审批 materialization 和回执不批量改写。

契约映射：formal issuance 来源分类（本地 `skeleton/registry.json`，不随公开仓库提供） 中的 `RUNTIME_AUTHORIZED_REVIEW_APPROVAL_OR_CONFIRMATION` 与 `DEFINITION_ONLY_THEN_EXECUTION_CONFIRMATION`、runtime platform machine（本地 `skeleton/registry.json`，不随公开仓库提供） 中的 `TASK_RUNTIME_SELECTION_REQUEST_EXACT`、`TASK_RUNTIME_SELECTION_OWNER_CURRENT`。该修复是后续正式绑定的前置；不宣称已实现对应完整 binding 操作。

## 完成记录不可降级，精确重放保持只读

`RecordStep` 在 writer / epoch 检查前读取已完成结果并验证调用者、version 和原步骤全部内容。相同步骤及 `COMPLETED` 状态直接纯读返回；降级、改写结果或改变身份均拒绝。尚未完成的写入进入正常写事务后再次读取，先构造独立 next snapshot；校验或持久化失败时返回原持久状态，不暴露尚未提交的完成结果。

完成必须有原 commit、至少一个步骤、完整且唯一的步骤身份，且所有步骤都已完成。原消费回执必须可证明：新版沿用明确 outcome，历史缺少 raw submission 的回执验证原 typed payload hash，不补造新字段。

v98 增加 SQL 保护：消费后的步骤身份及顺序不可替换、删减；完成快照与 worker epoch 不可改写；空步骤、缺字段、`null`、非数组及无原消费事实不能标记完成。升级不重写历史业务数据。

Go 与 SQLite 对重复 JSON 字段的解析不同，因此 SQL 写入、单条读取和项目确认列表共用歧义检查：拒绝重复对象键，并要求 journal 根对象、commit、step 使用规范字段名，防止 `state` / `State` 或 Unicode 别名造成两端状态分歧。业务字典的大小写不同键仍允许存在。历史歧义记录的单条读取返回错误并保留已解码状态；列表整体报错，不静默跳过或返回伪完成，不自动修复或降级。

这些规范字段名同时是 v98 的持久化约束；以后新增 journal 根对象、commit 或 step 字段时，需要新迁移同步已安装触发器的字段集合，不能只修改 Go 结构体。

稳定完成结果在 recovery 中验证结构后直接返回，`CanResume=false`，不再依赖当前 worker epoch 或子业务对象。未完成记录恢复得到 allDone 时同样校验结构，避免空步骤或损坏身份被误投影为完成。

契约映射：purpose confirmation 场景（本地 `skeleton/registry.json`，不随公开仓库提供） 中的 `confirmation.receipt_committed_stable` 及完成状态读失败不降级的原则。生产 `COMPLETED` 是现有确认步骤的终态；本批未把它替换为冻结契约的正式平台完成归档，更未用它补造 T1 authority。

## 同一 Upper Review 的前后台准备冲突

前台 `START_REVIEW` 与后台 `AuthorizedContinuation` 共用一个 Coordinator，但原先没有协调相同 Review/Run 的准备过程。两个调用者都可能看到未完成的 admission，在成员尚未写回工作树身份时调用 `Worktree.Manager.Create`；先行者占用创建路径，后到者得到 collision，并把成功操作旁的准备记录标记为失败。

已通过真实 Worktree.Manager 和 Git ResolveCommit barrier 确定性复现：先行者成功进入 RUNNING，后到者得到与整套失败日志相同的冲突；取消的等待方也会误写失败记录。

现在按 ReviewID + RunID 使用可取消的 Coordinator gate，只协调同身份正在执行的准备，不缓存业务结果。后到者独立重读 Run，核对原 project / task / area / operation / review / purpose / epoch，再核对自己的 grant generation 和 target。已激活且 RUNNING 的结果必须通过 `CheckPreparationTarget` 后纯读返回，不重复创建工作树或写入 binding。失败投影完成后才释放 gate，取消等待者不写失败，引用清零后移除 gate。

真实外来路径冲突仍拒绝；该 gate 不提供跨进程、不同 Review 的全局互斥。取消后旧物理操作与新 owner 的边界仍由原有 claim、epoch 和最终 Prepare 写入校验负责。

## 验证

证据目录：`/private/tmp/devbridge-revision-binding-o7yefeir`。进入本批前后按 SHA-256 对比，限定本批 17 个变更文件，保留其他已有修改。

修复前后对照：`/private/tmp/devbridge-definition-only-before/overlay.json` 只替换三份修复前选型生产源码，保留最终新测试和其他生产修复。旧实现确实忽略明确选择的不同模型/后端，并接受没有 START intent 的原始启动（最终 RED 8.404s，`runtime-selection-final-red.log`）。最终补齐完整运行会话等待及 Verification fixture 后，修复后的四个顶层用例连续三次通过（72.770s，`runtime-selection-final-count3.log`）。早期 RED / GREEN 日志也保留于同一目录。

- 真实 Core/SQLite 路径：Coding Review 的规范物化结果 → `APPROVE` / `NO_START` → 规范上下文重置 → 明确选择不同 runtime/backend → Execution、Run scope plan 及 Implementation / Verification 两次 `turn/start` 参数一致。
- 反例：没有 intent、未消费 intent、已消费但 actor / 步骤请求不同、Operator 借用同一步骤请求都不能授权新执行；已有 runtime-authorized candidate 的模型和后端仍锁定。
- 同进程及关闭重开 Core 后原请求重放、冲突选择：原请求和业务身份不变，数据库全值快照不变。
- 完成保护：拒绝提前完成、完成后降级及结果改写；最后保存失败不返回未落盘的完成状态；陈旧 epoch 下精确重放只读；可证明的旧版消费回执可完成；v97 → v98 升级不改写原业务数据。
- 两个既有集成测试改为在最后 `RecordStep` 保存处注入故障，先验证业务回执确已提交，再验证只读恢复和原请求重放；不再通过把已完成记录强改为 `UNKNOWN` 模拟故障。
- 列表歧义检查的三个反例先失败复现，日志 `completion-list-red.log`，再补齐相同谓词。

最终代码验证结果（测试节点含子测试，各行存在重叠，不合并计数）：

| 检查 | 结果 | 日志 |
| --- | --- | --- |
| SQLite 全量，包含最后的列表保护 | 1083 pass、0 fail、0 skip；172.364s | `sqlite-full-final.json` |
| 执行层全量，包含并发准备修复 | 307 pass、0 fail、0 skip；66.297s | `runtime-core-mcp-full-final.json` |
| Core API 全量，包含并发准备修复 | 420 pass、0 fail、62 skip；506.263s | `runtime-core-mcp-full-final.json` |
| MCP 全量，包含并发准备修复 | 138 pass、0 fail、5 skip；338.617s | `runtime-core-mcp-full-final.json` |
| Upper Review 并发、取消等待和授权边界，含 race detector | `-race -count=3` 通过；2.367s | `upper-preparation-green.log` |
| Core 确认 / 工作区专项，包含最后的列表保护 | 59 pass、0 fail、0 skip；101.745s | `project-confirmation-final.json` |
| MCP 审批 / 执行 / no-run 专项 | 53 pass、0 fail、0 skip；154.007s | `localmcp-target.json` |
| MCP `workspace_get` / 跨会话项目读取，包含最后的列表保护 | 2 pass、0 fail、0 skip；5.525s | `workspace-mcp-final.json` |
| `go vet` 执行层 / SQLite / Core API / local MCP | 通过 | `vet-final.log` |
| `gofmt -l` 本批 Go 文件、`git diff --check` | 通过 | `format-check.log` |
| `make -C skeleton generate-check production-parity` | 通过 | `frozen-parity.log` |

过程中的失败也保留：首次完整 Core API 回归因两个旧测试试图改写 `COMPLETED` 而失败，已按真实保存故障改造；第二次出现一次 `TestRuntimeUserDecisionProductionApprovalSeparation` 在初始 `START_REVIEW` 准备工作树时的路径/分支冲突（419 pass、62 skip、1 fail，`controlapi-full-final.json`）。该例随后连续三次通过（22.283s，`approval-separation-recheck.json`）；未放宽断言或加入自动重试来掩盖。最终整套在其他回归结束后独立运行，结果另记上表。

该冲突随后在不同用例的首次 `START_REVIEW` 再次发生，fixture 每次有独立临时目录。此次不再依赖整套重跑，改用上节的真实 Manager barrier 回归锁定同身份并发准备问题；只撤掉本轮 Coordinator 改动的 overlay 可稳定复现最终新测试失败（0.597s，`upper-preparation-red.log`），修复后含 race detector 连续三次通过。

随后完整回归暴露新增快照测试的等待边界不足：Implementation 已成功，但同一 RunSession 仍在继续 Verification。加列级差异诊断后，在五次复测的第一次捕获到后续 Verification Run 从 `CREATED` 变为 `PREPARING` 并写入 `workspace_id`，其他表未变化（`/private/tmp/devbridge-definition-only-before/runtime-selection-race-diagnostic.log`）。测试改为通过 `RunSession.Wait` 等待明确的整个执行器收尾，再取完整数据库快照；没有删表、忽略差异或加入固定延迟。严格等待同时暴露协议 fixture 复用了两个阶段的 turn ID，现已分开，并在 Verification 中实际运行检查，要求两个 Run 及 Execution 均成功，不允许忽略会话错误。这些都是测试修正，生产代码未因此变化。

冻结证明 `freeze-proof.json`：470 个文件哈希一致，`git status -- skeleton` 为空，tree OID 与基线相同，freeze commit 仍为 HEAD 的祖先。

四个相关包的最终全量结果合计 1948 个测试节点通过、0 失败、67 跳过，不包含重复运行的专项测试。跳过项不计为通过：Core API 的 62 项依赖未选定的已安装 App Server 或需要显式启用的真实模型场景；MCP 有四项未提供 candidate binary 目录，一项真实 Codex Review smoke 未启用。

测试边界：Coding 后端是独立子进程的 app-server 协议 fixture，不是线上模型兼容性验证。冷重启用同进程关闭后重开 Core 验证。重放的零变更指数据库快照；此组未在重放后直接重计外部 RPC。并发 barrier 测试使用实际 Worktree.Manager 实现，Git IO 和存储为测试替身；不同 ID 用例验证锁互不阻塞，未单独证明两个合法独立 Review 同时完成。SQLite journal 单元测试不充当平台资格或真实业务签发证明。`production-parity` 只覆盖现有 Task phase 词汇映射，不代表全部冻结契约达成。

## 后续

真实 collector、原 formal producer 中的精确平台资格引用与 source archive、revision binding 和 APPROVE_AND_START T1/T2/T3 仍待实现。旧回执可沿原证明重放其业务结果，但不能从当前资格回填出新的 runtime binding。当前 `no_run_current_authority` 的旧近似 reader 也不能视为新的绑定读取器。

本批变更留在工作区，未提交。
