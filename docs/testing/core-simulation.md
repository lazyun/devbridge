# 真实 Core 的无模型接入模拟器

`devbridge-sim` 是项目内可重复运行的集成测试程序：真实启动 `devbridge`
Core、GPT MCP proxy 和 Local MCP proxy，使用确定性的上游 GPT、显式测试用户、
下游 pull worker 三种角色驱动公开 HTTP/MCP 接口。不是模拟 Core 状态机，
也不直接读写 SQLite 或插入审批/执行记录。

> **边界：模拟的是 GPT/用户/中性 pull Agent，不是 Codex App Server。**
> 实际执行 `DIRECT_FILE` 文件工作流的场景使用明确的 **fixture-only 验证豁免**，因此
> `verificationStatus` 必须一直是 `NOT_RUN`。即使 fixture 用户随后验收并合并，
> 也不能描述成原生命令验证通过或真实 AI 工作完成。

另有独立的 [test-only continuation gate](simulated-continuation-gate.md)：
通过明确的测试依赖注入，把受信任的确定性协议 Agent 接到真实 Core，建立真实
Coding Execution/验证记录，再测试 `DEFER_TASK` / `CONTINUE_TASK`。它不是本页
22 场景的生产二进制入口，也不证明原生资格或主机沙箱通过；两类证据不得混算。

## 一键运行

要求 Go 1.25+、Git、macOS/Linux；不需要 Codex、账户凭据、模型服务或 Tunnel。
在待测试源码版本根目录运行：

```sh
./scripts/verify-core-simulation.sh
```

脚本构建真实 Core、两个 MCP proxy 和模拟器，保存源码 HEAD、工作区状态、
各二进制 SHA256、测试报告、逐调用证据和进程日志。构建和运行产物默认位于
系统临时目录；需要保留在指定父目录时：

```sh
DEVBRIDGE_SIM_EVIDENCE_DIR="$HOME/devbridge-simulation-evidence" \
  ./scripts/verify-core-simulation.sh -scenario file-crash
```

每次都会在父目录下创建全新的私有子目录，不复用历史 DataRoot。
程序退出后仅停止自己启动的进程，证据文件保留供排障；请自行按需删除。
请勿把整个证据目录提交到项目或公开发布。

已有同一版本的编译产物时：

```sh
go build -o ./build/sim-bin/ ./cmd/devbridge ./cmd/devbridge-mcp \
  ./cmd/devbridge-local-mcp ./cmd/devbridge-sim
./build/sim-bin/devbridge-sim -bin-dir ./build/sim-bin -scenario all
```

`-timeout` 是每个场景的运行上限（默认 `5m`）。任一断言失败返回非零退出码，
报告明确记为 `FAILED`；协议/传输错误不会作为预期业务拒绝算通过。

## 场景与断言

| 场景 | 真实 Core 中检查的行为 |
|---|---|
| `task-admission` | 按生命周期从最早缺口开始：Task prepare/options 不创建业务对象；错误 hash/version 拒绝且不泄漏确认对象；无效 variant 形成可读的 `REJECTED_NOT_CONSUMED`，只有 Core 明确允许编辑选择才提交更正；显式仅创建一次 Task，重复/冷启动回执不多建或偷偷启动 Review；单独 Review prepare 仍无 Run，正确确认后只有一个精确只读 UPPER Review Run，停在 `REVIEWING` |
| `review-gates` | 冻结 Review 指令/只读成员与 claim 哈希；错 epoch、变更 worker/request、越界读取、错误成员/证据 digest/原始提交、缺少或错误 worker 的报告均拒绝且无业务副作用；正式报告关闭权限，精确回执与完整报告冷启动可读；单独批准停在 `IMPLEMENTATION_READY`，保留 `RUN` 验证要求，**不豁免、不启动实现** |
| `review-blocked` | 独立 fixture 提交真实读文件证据支持的模拟 `BLOCKED` 报告；Review Run `SUCCEEDED` 仅代表报告记录完成，Task 仍 `ACTIVE/REVIEW_REQUIRED` 并要求 DEEP；无可用批准/启动动作，无实现权限，拒绝晚到写报告，冷启动回执稳定 |
| `review-further` | 同一阶段的独立 `FURTHER_REVIEW_REQUIRED` 分支；不自动发起后继 Review、不创建新 Task，不把已有报告完成当成 Task 完成 |
| `execution-runtime-gate` | 保留批准的 `RUN` 验证要求；公开 runtime/backend 目录只是可选元数据，不是选择或执行资格；缺少 backend、选择实际目录 backend 但缺少 runtime 两次明确拒绝，保留可读未消费回执，冷启动不创建文件权限或悄悄豁免 |
| `direct-start-gates` | 独立 fixture 用户明确豁免后，旧 revision/篡改指令或范围、错误确认均无效果；pending 冷启动不消费；单独 START_DIRECT_FILE 只建立精确权限，claim 绑定冻结定义与真实成员；旧 epoch/generation/member、错误 worker 与范围外 CREATE 均不修改文件，停在首个成功写入之前 |
| `file-mutation-gates` | 在报告/批准前明确两个成员的三个可写路径；实际 CREATE/REPLACE/MOVE/DELETE 才进入 `IMPLEMENTING`，按全部 managed 文件增删改和公开成员读取交叉检查；旧回执不得重放旧效果，失败 digest 请求不能原地改写，显式取消不回滚部分效果、不生成结果 |
| `mutation-cancel-race` | 稳定 `IMPLEMENTING` 上并发一个真实文件请求与显式取消；只接受先写后关或先关拒写两种可观察顺序，记录实际顺序；已确认效果回执在 CLOSED/冷启动仍可读，不重新执行，不调用恢复 |
| `file-result-gates` | 实际修改一个成员、另一个不变；拒绝身份/版本/worker/空摘要错误，已知 FAILED digest 操作不制造结果也不阻止合法报告；Core 忽略虚假的 advisory Members，自行冻结全部精确成员、Git tree 字节与变更路径，关闭权限但不验收；精确结果/worker 回执冷启动只读，晚到写入和第二份新结果拒绝 |
| `file-result-no-change` | 零成功文件效果也可以如实报告；每个成员必须 NO_CHANGE、起止 tree 相同、changedPaths 为空，不虚构实现工作；仍 NOT_RUN、未验收、无 Execution/额外 Run/commit |
| `file-result-after-cancel` | 合法未 enrollment 文件权限经显式确认取消后，新 v2 结果必须拒绝；不依赖 worker 身份补充保护，冷启动保持 CLOSED/IMPLEMENTATION_READY、结果 NOT_FOUND，无业务或文件副作用 |
| `file-acceptance-gates` | 一个真实变化成员与一个未变成员的正式结果，必须等独立 fixture 用户验收；提前 task_commit_retry、错会话/revision/workflow/result/epoch/generation/protocol 均拒绝且不提交；真正验收后核对完整成员回执、精确 Git tree/唯一父提交，冷重放不多提交，停在 FeatureArea 合入之前 |
| `file-acceptance-no-change` | 零变化结果仍可独立验收，Task 可完成交付，但所有成员 NO_CHANGE、commit 字段为空、真实 managed branch HEAD 不变，不生成假提交、不声称命令验证通过 |
| `feature-delivery-gates` | 独立双成员 fixture 已验收并完成 Task commit 后，单独准备/确认 FeatureArea 合入；冻结每个 repository/member/worktree/源提交/目标分支/预期 HEAD，错误确认和 pending 冷重启不合入；显式确认后精确快进到已验收提交，重复/冷回执不再合入，backend 不变 |
| `task-action-gates` | 观察正数顶层 Task action generation；旧 generation 明确拒绝，nested/零/小数/字符串仅作为 advertised schema 校验；两个真实 GPT Session 竞争只有一个 pending owner，外会话不获私有 intent；显式丢弃推进 generation，旧卡晚到/冷启动不能消费或抢走新卡，始终无业务后继 |
| `review-successor-gates` | 在独立 Review 审批等待点，用独立用户 REVISE 创建同一 Task 的唯一新 UPPER Review/Run；冻结旧 workspace/只读成员与原指令加精确反馈；旧报告/worker 回执仍归旧 Review，外层 context 不借用新身份；新报告独立记录，确认重放/冷启动/丢弃已消费卡均不多建，不批准或启动实现 |
| `canceled-review-successor` | 未记录报告的 UPPER worker 经显式取消后，Task 仍 REVIEW_REQUIRED；另行准备/确认 START_REVIEW 才创建同 Task 的第二个 Review/Run；旧取消与 worker 回执始终 OUTCOME_UNKNOWN、无结果/新 payload，外层上下文仍归旧 Review；新报告单独记录，冷重放不复活旧 worker 或多建后继 |
| `canceled-file-supplemental-review` | 一个真实成员已发生部分文件效果，活动 grant 必须先显式取消；从当前 capability 复制完整五字段目标并保留 DEEP，下调深度/旧目标/错误选择均无效果；单独确认 REQUEST_REVIEW+START_REVIEW 保留旧定义、NONE 豁免与全部部分文件字节，新 Review 报告不替代原文件结果或验收 |
| `happy` | 创建 Task → UPPER_LLM Review → worker 真实读文件/提交报告 → 用户批准但不启动 → 用户单独启动 DIRECT_FILE → 实际写入两个精确成员 → 正式结果 → `ACCEPTANCE_PENDING` → 冷启动历史回执 → 用户明确豁免验收 → Task commit → 用户单独确认 FeatureArea merge |
| `claim-race` | 8 个并发相同 claim 仅一个获得可执行 payload，其余只读同一回执；显式取消只关闭权限，不声称外部 worker 已停止或 Task 已取消 |
| `file-crash` | 一个文件效果获得回执后 SIGKILL **自有 Core**；重启后旧 worker 为 `OUTCOME_UNKNOWN`，无重新派发；旧回执可读、旧权限不可新写；不支持的继续被拒绝、epoch/generation 不刷新；用户明确取消 |
| `review-cancel` | Review 已派发后用户取消；Run/权限取消但 Task 回到 `REVIEW_REQUIRED`；旧文件访问和晚到报告拒绝；冷启动后保持同一取消回执和未知外部 worker 状态 |

`happy` 同时检查原会话/worker 身份、相同路径的不同仓库成员、非目标 backend、
越界路径、旧 digest、改 payload 的重复请求、结果后晚到写入/新报告等拒绝。
拒绝后会读取真实文件或 Task 投影，避免只看“报错了”而漏掉已发生的副作用。
审批、Agent 报告、Task 验收、Git 交付是分开的动作与断言。

`all` 先运行 `task-admission`、三个 Review 阶段场景、两个独立启动阶段场景、两个文件效果阶段场景、三个正式结果阶段场景、三个验收/交付阶段场景、两个 Task action/Review 后继阶段场景、两个取消后再审查阶段场景，然后回归原有四个场景。单独验收初始阶段：

```sh
./scripts/verify-core-simulation.sh -scenario task-admission
```

初始阶段通过公开的项目 Task/Run 分页与 Task Run 历史（明确检查无截断）、
`workspace_get` 和精确 `confirmation_get` 交叉核对数量、状态、身份与回执。
校验未通过、已记录但未消费、已消费是不同结果；不能把报错或无回执当成安全重试。
此阶段不派发 worker、不批准实现、不启动继续任务，也不触及原生模型资格。
三个后续 Review 场景只使用中性 pull worker。`workerId` 是交付关联标签，不是
模型/进程身份证明，也不让文件读取变成某个 worker 的独占权限。

启动阶段不把 `RUN` 静默改成 `NONE`：正向中性文件准入使用另一个明确豁免的
fixture。`execution-runtime-gate` 只选择公开目录返回的真实 backend 身份而不提供
runtime，验证明确拒绝，不伪造平台 qualification 或尝试运行模型。
`direct-start-gates` 在成功 claim 后才检查缺少 worker 身份的写入拒绝；Core 本来
允许未 enrollment 的非 worker 文件使用，模拟器不把 claim 错当成所有文件操作
的强制前提。此阶段不提交实现结果、不验收、不取消或恢复已启动权限。

## 安全隔离与证据

- CLI **没有**连接既有 Core URL、既有 DataRoot、用户仓库或指定模型的参数。
- 三个真实服务均只绑定 `127.0.0.1:0`，使用新建私有 DataRoot；不会更换当前部署。
- 三个测试 Git 仓库都在新建目录中。只有显式 fixture merge 才改变它们自己的
  development branches；非目标 backend 始终不变。
- 子进程环境采用 allowlist；不继承用户 API token、代理、Codex/Pi 配置或 Git
  全局配置。使用私有 HOME/CODEX_HOME 和拒绝原生启动的 tripwire。
- 原生执行选择被固定为只记录并失败的 tripwire；若意外触发，整个场景失败。
  它不是伪造 Codex 版本、模型执行或平台资格的替代物。
- `transcript.jsonl` 标记 `upstream_gpt`、`fixture_user`、`downstream_agent`，记录
  请求与真实响应；`report.json` 记录逐断言、场景状态和版本指纹。
- 不自动重发结果未知的请求，不把 `OUTCOME_UNKNOWN` 改写为完成/外部进程死亡。
- `file-crash` 是**已确认效果之后**的真实进程崩溃，不冒充精确的
  “文件效果与回执提交之间”崩溃注入；后者仍由既有 journal/hook 测试覆盖。

## 自动测试

```sh
go test ./test/simulation ./cmd/devbridge-sim
go vet ./test/simulation ./cmd/devbridge-sim
DEVBRIDGE_SIM_BIN=/absolute/path/to/compiled/bin \
  go test ./test/simulation -run '^TestPublicBinarySimulation$' -count=1 -timeout 15m -v
```

普通单元测试不启动 Core；没有 `DEVBRIDGE_SIM_BIN` 时集成测试明确 `SKIP`。
单元测试通过或集成测试跳过，都不能当成真实二进制场景通过。

## 不覆盖什么

- Codex/Pi 真实模型质量、原生沙箱、命令执行、`RUN` 文件结果的原生 verification handoff、
  `VERIFYING`/GPT run analysis、
  原生请求权限/中断继续以及原生 runtime qualification。
- 真实 ChatGPT/dot 的执行环境、实时 connector、Tunnel、远程部署或持续运行保障。
- 任意恶意可执行程序的 OS 安全边界（所选 Core/proxy 二进制必须可信）。
- 中性 pull 协议不支持的 worker 继续，不通过修改数据库或伪造 qualification 绕开。

完整原生命令验收继续使用[原生工作流 gate](real-e2e-gates.md)。
此前独立的 pull-worker gate 与本模拟器不是同一次执行证据。以后部署本地真实联调时，应重新构建目标版本并
验证真实 connector/模型通路，不能用本报告替代线上验收。

## 开发验证记录（2026-10-30）

- 第一轮（新增 `task-admission` 前）的一键脚本与独立复跑均通过 **288 个断言**：happy 115、claim-race 72、
  file-crash 62、review-cancel 39；独立复跑也使用了相对 evidence 路径。
- 单元测试、单元 race 检查与 `go vet ./...` 通过；独立验证 SIGTERM 会停止三个
  自有服务进程并把中断报告记为失败，而不是遗留服务或误报完成。
- 开发中的一次冷重启遇到 Local MCP `initialize` / `server/discover`
  `unexpected EOF`。该次运行如实失败，后续完整复跑未重现；原因未确定，
  不称为已修复，也没有用自动重试掩盖。排查时应保留对应 Core/proxy 日志。
- 当前执行沙箱禁止部分 AF_UNIX socket 操作；一次全量运行还触及 controlapi
  包默认 10 分钟总超时（当时运行到初始化数据库的测试，不是该测试运行了 10 分钟）。
  仓库完整 `go test ./...` 中原有
  App Server 相关测试出现 `socket: operation not permitted`。本模拟器使用 TCP
  loopback，不能用其通过结果宣称整个仓库测试全绿或原生连续会话已验证。
- 后续把完整测试上限延长至 30 分钟后，全量测试运行到结束，仍不是全绿：
  App Server/controlapi 的原生 socket/recovery fixture 失败；另一次 continuation
  聚合断言失败，单独重跑三次通过，原因未确认，不能称已修复。
- 第二轮按顺序补 `task-admission`，没有抢先实现 continuation 场景。
  初始开发中的拒绝回执指针和 GPT 工具目录两处假设按公开契约修正，属于
  模拟器问题，不登记为 Core 缺陷。最终场景计数与通过情况以对应 `report.json`
  为准；历史四场景结果不能替代新增阶段的验证。

### Review 阶段发现的真实缺陷

新场景在未修改的真实二进制上复现：多仓 UPPER Review 正式报告已经记录，
按 `requestId` 读取回执成功，但不带 `requestId` 读取完整 `review_result_get`
报 `Review report identity mismatch`；`APPROVAL_READY`、`BLOCKED` 与
`FURTHER_REVIEW_REQUIRED` 三种报告均受影响。

原因是读取端仍把 v2 的冻结成员集合指纹与 v1 的单仓 `BaseCommit` 比较。
修复保留 Review/Run 身份检查，v2 按原始准入只读目标集合重算指纹，拒绝缺失、
混合或伪造基线；v1 仍校验自己的标量基线。不使用报告提出的写入范围替代
准入身份，也不省略检查“让测试通过”。单元回归同时覆盖 `operation_get`
的共享读取路径。该修复不产生新报告、审批或执行权限。

Review 交付/报告回执使用原始类型化 JSON 字节哈希，Confirmation 回执使用
规范化 JSON 哈希；模拟器分别验证，不能混用。开发时混用哈希导致的断言失败
属于模拟器自身问题，与上述实际报告读取缺陷分开记录。

### 启动阶段的契约边界

新检查使用已批准定义上的真实公开操作。开发时发现两处模拟器假设与公开契约
不符：私有 Core 仍可列出默认 backend 元数据（不等于真实执行资格）；workspace
的成员投影不包含完整 readiness，普通文件读取也不暴露所有内部成员字段。
检查改为冻结目标身份、实际读得的文件字节，以及 worker payload 内的完整成员
readiness 分别核对。这些是测试假设修正，不登记为 Core 缺陷。任何启动步骤前后
均核对已存在 managed 文件、源分支 HEAD/字节和公开 Run 历史，拒绝不能误算为
验证成功或 Task 完成。

### 文件效果阶段

`file-mutation-gates` 的额外 PATHS 和指令在正式报告/用户批准之前写入，不修改
已经冻结的授权。首个 CREATE 与后续 REPLACE/MOVE/DELETE 都核对实际全文件
变化集；同路径另一成员、未授权新文件和 backend 源分支不能被顺带修改。
旧 CREATE/REPLACE/MOVE 回执在路径后来已移动/删除后仍只读，旧 DELETE 回执
在同路径创建新对象后不能再删新文件。失败 digest 操作的 requestId 是不可改写
的历史事实，重新读取后的新授权操作使用新 ID，不冒充相同请求重试。

v2 `workflowComplete=true` 不是正式结果提交，必须在效果前拒绝；旧式
`direct_file_disable` 也不能绕过独立确认取消。取消 prepare 本身仍不撤销当前权限，
之后已确认的合法效果在取消消费后保留，Task 仍 ACTIVE/IMPLEMENTING、worker
仍 OUTCOME_UNKNOWN，精确结果读取仍 NOT_FOUND。这里不提交报告、不验收或
合并，也不声称 worker 外部进程已停止。

`mutation-cancel-race` 是正常公开请求的并发样本，每次报告保存实际观察到的
合法顺序；单次通过不代表强制触发了两种内部时序，更不是效果/日志之间的
精确故障注入。后者继续由既有受控 hook 测试覆盖。开发中一次模拟器把仅 MOVE
才有的 destination 当成必有字段，已修正；该失败不是 Core 缺陷。

### 正式文件结果阶段与取消权限修复

正式结果阶段停在 `ACTIVE/ACCEPTANCE_PENDING`，不调用用户验收。结果的 Members
是 advisory 输入，不能用它来决定实际成员或证据；模拟器故意提供 schema 合法但
错误且不完整的声明，随后用冻结 claim 身份、完整成员集合和实际 Git tree/blob
交叉验证 Core 自己生成的结果。一个成员改变、另一个不变，以及全部 NO_CHANGE
分别验收；零变更不是错误，也不能写成成功完成了修改。v2 没有伪造的聚合 Snapshot。

本阶段复现并修复真实 Core 缺陷：未 enrollment 的合法 v2 文件工作流在显式
`CANCEL_DIRECT_FILE` 已消费、权限 CLOSED 后，仍能提交一份全新的正式结果，
错误进入 ACCEPTANCE_PENDING。带 worker 的路径原有检查会拒绝，但无 worker
路径漏掉 fresh result 的 ACTIVE 门槛。修复仅在新 v2 结果路径检查当前 ACTIVE，
放在精确历史回执查询之后：已记录结果的重放不受影响，也不强制所有文件使用
必须 enrollment。回归分别覆盖无变更/已有部分效果、重启，以及 v1 合法的
先关闭再首次提交结果兼容路径。另行探测的未 enrollment 冷重启旧 epoch 提交
原本就被 RECOVERY_REQUIRED 拒绝，未据此虚构或修改 epoch 缺陷。

这些正向文件结果使用之前明确的 fixture-only NONE 豁免，因此只证明 NOT_RUN
结果等待用户验收。`RUN` 结果的原生验证选择、verification-only Execution 创建、
自动 START_VERIFICATION 与失败交接不在此无模型公共二进制覆盖内；启动阶段的
缺少选择拒绝也不能替代它们。真实 journal PENDING/UNKNOWN 和外部篡改故障仍由
专门测试覆盖，不通过写数据库或伪造 runtime 来制造本模拟器通过证据。

### 独立用户验收与 Git 交付阶段

结果提交、用户 `file_implementation_accept`、Task Git commit、FeatureArea merge
不是同一个事实。新场景在验收前读取并核对未验收结果、原 managed branch HEAD
与 `UNCOMMITTED`；`task_commit_retry` 不能代替初次验收。验收后通过公开
`taskView.details.commitReceipt` 与实际 Git 对象交叉证明：变化成员只产生一个
以原 StartHead 为父、tree 精确等于冻结 EndSnapshot 的提交；NO_CHANGE 成员
不产生提交。结果仍然 NOT_RUN，无原生 Execution/额外 Run。

这个公开文件验收路径在 Task commit 全部完成后可显示 `SUCCEEDED/DELIVERING`，
不代表已经合入 development branches。独立 FeatureArea 确认的 plan 才冻结并
允许后者。此处无目标分支分歧，实际合入应是快进到精确已验收提交，不制造 merge
commit；后端仓库与 source 分支在明确合入之前始终不变。完整 NO_CHANGE 场景
在 Task 无变更交付完成处停止，不为了“看起来完成”制造提交或无意义合入。
全部 NO_CHANGE 时投影可为 MERGED（无待合入内容），不是发生了 Git merge；
有变化成员在独立合入前必须 UNMERGED。worker get/claim 的只读结果会投影独立
验收为 accepted=true，原 result_submit 回放仍返回报告时的 false；两者都不得
重新派发或改变冻结文件事实。开发中将两种读取语义混同的断言失败属于模拟器
假设修正，不登记为 Core 缺陷。

验收错误身份检查另发现并修复一处错误分类问题：不存在于精确
session/project/task/revision/workflow/result 组合的结果原先返回 INTERNAL_ERROR。
实际没有发生越权验收或提交；问题是正常身份拒绝被包装成内部故障。修复只把
该查询的 `sql.ErrNoRows` 映射为普通 NOT_FOUND；六项身份条件、已消费请求冲突
优先级、真正数据库错误处理和事务回滚均保留。真实 Core 回归与公共接口场景
分别验证不存在、跨会话与错误身份不泄漏结果，正确独立验收仍可继续。

未覆盖的边界包括验收后 Git 部分失败的精确日志/效果间故障注入、原生 RUN
验证验收以及 `COMPLETE_TASK` 的另一条合并前终态保持流程。这里的通过结果
不能替代这些专门测试，也不引入 successor/continuation 权限。


### Task action 栅栏与首个正式 Review 后继

本增量不把历史反馈直接登记为当前缺陷。顶层 `taskActionGeneration` 在当前
公开 schema/真实 Core 路径可用；旧值得到 `TASK_ACTION_STALE`，嵌套字段等
错误仅在模拟器按 `tools/list` 校验时拒绝，不冒称已测 Core 业务授权。
竞争作者是实际初始化并由 fixture 用户绑定同一项目的第二个 GPT Session，
不是把只有 worker 工具的 Local ingress 当作上游作者。pending/discard/消费
回执与 Task/Run/源文件不变量分别检查；不会通过自动重试 UNKNOWN 制造成功。

`REVISE` 是显式用户对当前正式 Review 的决策，不是原生 `CONTINUE_TASK`。
prepare 仅冻结旧 Review/decision、观察到的 generation 与反馈；commit 才创建
同一 Task 的一个新 UPPER Review/Run，再显式准备其精确只读权限。新 worker
获得原始 authored instruction 加原样反馈，旧报告和 worker 回执仍以旧身份
只读，外层公开 context/workflow 必须标记 stale/refreshRequired 且无 nextAction。
两个 Review 的报告、Run owner、Task definition、源分支和 managed 文件字节
独立核对；不会因为旧卡重复提交或 cold replay 创建第三个 Review。

反馈边界按实际契约保留所有可接受的字节：非空空白是合法反馈，不擅自 trim。
模拟器用 schema 合法但 domain 禁止的 NUL 检查无副作用拒绝。开发中曾错误
预期空白被拒绝、把 Local 当作可准备 execution 的 ingress，以及把公开
`agentExecutionId=review:<id>` 兼容投影误当作原生 Execution；这些是已纠正的
模拟器假设，不是 Core 缺陷。真实 Task Execution 仍为空，无模型/thread/turn。

本增量未覆盖需真实 currentExecution/终态原生 Run 的正向 CONTINUE_TASK、
DEFER_TASK、REQUEST_REVERIFICATION、RETRY_IMPLEMENTATION 或 no-Run 原生
Coding successor。中性文件路径不能伪造这些资格。历史 UNKNOWN/migration
冲突测试使用受控 store fixture，不作为这里的纯公开二进制证据；单纯读取或
忽略成功响应也不称为真实响应丢失注入。本场景停在第二个 Review 的审批等待点；取消后重新 Review 与补充 Review
使用下面另两个独立 fixture，不从此场景暗中接续。


### 显式取消后的重新审查与补充审查

`canceled-review-successor` 首先真实读取文件并构造有效但尚未提交的旧报告，
然后由 fixture 用户取消该 Review。取消既不把旧报告记录成功，也不证明外部
worker 已终止；旧 worker 的 OUTCOME_UNKNOWN 必须保留。新的 START_REVIEW
有独立准备/用户确认/claim/报告。旧取消重放、worker get/claim 和晚到旧报告
分别验证：回执不改写，新 Review 不继承旧派发身份，旧响应 envelope 不带
后继的动作。两个 Run 明确分别 CANCELED 与 RUNNING/报告后 SUCCEEDED。

`canceled-file-supplemental-review` 先在已明确豁免的独立 fixture 中完成一个
真实 REPLACE。当前 v2 grant 活动时，补充 Review 不可借机关闭它；必须单独
消费 CANCEL_DIRECT_FILE。取消后从可用 REQUEST_TASK_REVIEW capability 读取
完整 `expectedReviewRequest` 五个显式字符串，空 execution/decision 是精确
事实而非通配符。旧 phase 和 FAST 深度分别拒绝，pending/错误选择/cold read
不创建后继；单独确认只执行 REQUEST_REVIEW 与 START_REVIEW 两步。

新 Review 保留同一 Task、原 approved definition、原只读 workspace/成员。
公开 currentReviewRequest 必须保留旧 revision/Review、原 IMPLEMENTING 目标
和原始 purpose；回执 phase=REVIEW_REQUIRED 是请求效果，不是原始 phase。
源分支、全部 managed 文件字节与已确认的部分效果保持不变；旧文件 grant
仍 CLOSED，旧文件 worker get/claim 仍 OUTCOME_UNKNOWN，精确旧文件 scope
不借用新 Review/Run/Execution 或动作（文件回执的 workflow 为空，并非 Review
回执的 stale 标记契约），无正式文件结果、无提交、
无原生 Execution。新 Review 报告也不自动批准替换旧定义。原 NONE 豁免保留，
但此时并无可冒称验证成功的文件结果。

此阶段没有新增 Core 修改或缺陷结论。开发时遗漏 schema 要求的显式空
repositoryId，以及把结果对象的 verificationStatus 错放到 Task view，均作为
模拟器协议/投影假设纠正；严格校验拒绝或断言失败保留在开发证据中。最终对
Task view 检查真实 currentVerificationSpec 与 UNCOMMITTED，而非检查不存在的字段。
