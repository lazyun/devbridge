# 真实 Core 的无模型接入模拟器

`devbridge-sim` 是项目内可重复运行的集成测试程序：真实启动 `devbridge`
Core、GPT MCP proxy 和 Local MCP proxy，使用确定性的上游 GPT、显式测试用户、
下游 pull worker 三种角色驱动公开 HTTP/MCP 接口。不是模拟 Core 状态机，
也不直接读写 SQLite 或插入审批/执行记录。

> **边界：模拟的是 GPT/用户/中性 pull Agent，不是 Codex App Server。**
> `DIRECT_FILE` 场景使用明确的 **fixture-only 验证豁免**，因此
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
| `happy` | 创建 Task → UPPER_LLM Review → worker 真实读文件/提交报告 → 用户批准但不启动 → 用户单独启动 DIRECT_FILE → 实际写入两个精确成员 → 正式结果 → `ACCEPTANCE_PENDING` → 冷启动历史回执 → 用户明确豁免验收 → Task commit → 用户单独确认 FeatureArea merge |
| `claim-race` | 8 个并发相同 claim 仅一个获得可执行 payload，其余只读同一回执；显式取消只关闭权限，不声称外部 worker 已停止或 Task 已取消 |
| `file-crash` | 一个文件效果获得回执后 SIGKILL **自有 Core**；重启后旧 worker 为 `OUTCOME_UNKNOWN`，无重新派发；旧回执可读、旧权限不可新写；不支持的继续被拒绝、epoch/generation 不刷新；用户明确取消 |
| `review-cancel` | Review 已派发后用户取消；Run/权限取消但 Task 回到 `REVIEW_REQUIRED`；旧文件访问和晚到报告拒绝；冷启动后保持同一取消回执和未知外部 worker 状态 |

`happy` 同时检查原会话/worker 身份、相同路径的不同仓库成员、非目标 backend、
越界路径、旧 digest、改 payload 的重复请求、结果后晚到写入/新报告等拒绝。
拒绝后会读取真实文件或 Task 投影，避免只看“报错了”而漏掉已发生的副作用。
审批、Agent 报告、Task 验收、Git 交付是分开的动作与断言。

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

- 一键脚本完整运行与独立复跑均通过 **288 个断言**：happy 115、claim-race 72、
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
