# 真实 Core 的无模型接入模拟器

`devbridge-sim` 是项目内可重复运行的集成测试程序：真实启动 `devbridge`
Core、GPT MCP proxy 和 Local MCP proxy，使用确定性的上游 GPT、显式测试用户、
下游 pull worker 三种角色驱动公开 HTTP/MCP 接口。不是模拟 Core 状态机，
也不直接读写 SQLite 或插入审批/执行记录。

> **边界：模拟的是 GPT/用户/中性 pull Agent，不是 Codex App Server。**
> 实际执行 `DIRECT_FILE` 文件工作流的场景使用明确的 **fixture-only 验证豁免**，因此
> `verificationStatus` 必须一直是 `NOT_RUN`。即使 fixture 用户随后验收并合并，
> 也不能描述成原生命令验证通过或真实 AI 工作完成。

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
| `happy` | 创建 Task → UPPER_LLM Review → worker 真实读文件/提交报告 → 用户批准但不启动 → 用户单独启动 DIRECT_FILE → 实际写入两个精确成员 → 正式结果 → `ACCEPTANCE_PENDING` → 冷启动历史回执 → 用户明确豁免验收 → Task commit → 用户单独确认 FeatureArea merge |
| `claim-race` | 8 个并发相同 claim 仅一个获得可执行 payload，其余只读同一回执；显式取消只关闭权限，不声称外部 worker 已停止或 Task 已取消 |
| `file-crash` | 一个文件效果获得回执后 SIGKILL **自有 Core**；重启后旧 worker 为 `OUTCOME_UNKNOWN`，无重新派发；旧回执可读、旧权限不可新写；不支持的继续被拒绝、epoch/generation 不刷新；用户明确取消 |
| `review-cancel` | Review 已派发后用户取消；Run/权限取消但 Task 回到 `REVIEW_REQUIRED`；旧文件访问和晚到报告拒绝；冷启动后保持同一取消回执和未知外部 worker 状态 |

`happy` 同时检查原会话/worker 身份、相同路径的不同仓库成员、非目标 backend、
越界路径、旧 digest、改 payload 的重复请求、结果后晚到写入/新报告等拒绝。
拒绝后会读取真实文件或 Task 投影，避免只看“报错了”而漏掉已发生的副作用。
审批、Agent 报告、Task 验收、Git 交付是分开的动作与断言。

`all` 先运行 `task-admission`、三个 Review 阶段场景、两个独立启动阶段场景，然后回归原有四个场景。单独验收初始阶段：

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

- Codex/Pi 真实模型质量、原生沙箱、命令执行、`VERIFYING`/GPT run analysis、
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
