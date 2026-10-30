# 冻结约束的运行资格与正式执行闭环

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-c1a4e0f5b94aeb6e)。

本批实现承接运行平台基础、定义审批归档和 no-Run 恢复实现，补齐真实平台采集、正式选型来源、任务版本绑定以及审批启动三阶段事务。`skeleton/` 仅作为只读约束来源；本批不修改它，也不运行其中的程序或整包回归。

## 实现边界

- `internal/platformcollector` 独立解析实际 Codex 安装、读取 Core/launcher/native 制品摘要、导出并验证协议 schema。隔离 HOME 与自有回环 Responses 服务中实际测量初始化、双 root 命令写入、结构化输出、冷 thread 恢复、只读拒写和中断；原始报告与协议制品按内容摘要保存。未测得的能力不会生成资格。
- `internal/runtimequalification` 将明确后端配置及其指纹关联到精确 descriptor、evidence 和资格代次。显式请求先保存身份栅栏；原请求更换制品、角色或后端在采集和签发前拒绝。项目/Core 启动不会自动授予资格。读取当前 lineage 仅用于取得 CAS 版本，不选择或替换 qualification。
- `internal/runtimeselection` 的来源只能由通过独立校验的正式业务归档产生。绑定 owner 是 project、Task、definition revision 与 runtime role 的完整组合；来源和绑定均不可改写。首次绑定要求 CURRENT；既有绑定可使用 SUPERSEDED；INVALIDATED 拒绝新的运行授权，原成功回执仍可只读重放。
- 首次创建 Task 时保存真实 authored draft，包含原 goal/constraints 而无批准、执行范围或运行授权。Review 入场事务记录它实际审阅的 revision/hash。首个 v1 draft 可以作为正式 v2 批准结果的父版本，两种 schema 分别校验，历史记录不做推断回填。

## 正式事务与启动

`APPROVE_AND_START` 的 Coding 分支按固定顺序提交：

| 事务 | 原子结果 | Task 阶段 |
| --- | --- | --- |
| T1 | 消费准确 Review Decision，写输出 revision、完整 operation identity 与来源起点 | IMPLEMENTATION_PREPARING |
| T2 | 写 ADMITTED Execution、冻结 Plan 和独立 T2 readback | IMPLEMENTATION_PREPARING |
| T3 | 写 preparation authority、最终 formal receipt 和 approved selection source | IMPLEMENTATION_READY |

三阶段均不创建 Run、不 dispatch、不写 revision binding。Core 在 T3 后单独物化 binding；后续准确的启动命令才能创建 Run。最终回执优先于部分进度和新鲜资格检查；损坏的最终回执不能退回部分进度或重新执行 T1。

`APPROVE` 保留 NO_START 语义。后续 `START_APPROVED` 必须引用准确的原定义审批归档，先提交 admission/Plan/preparation 的业务依据，再以独立事务提交 adoption final receipt/source，随后独立绑定、启动。原业务依据可以在中断后继续，不能重新选取资格或借用旧 Review 的运行配置。新确认的准备标记使缺失的正式回执无法冒充历史流程。

已有 Run 的原请求先走历史回放。新的 v2 Coding 启动在候选、start effect 和 Run 写入之前要求准确绑定；后台队列及 Run 创建处再次检查。Coordinator 在准备与启动进程前验证资格、绑定、后端和实际制品身份。仅有旧配置值、旧成功状态或静态协议标识不能获得新授权。

`REQUEST_CHANGES` 不属于冻结约束允许的正式运行选型来源。它不能把旧 binding 复制到新 revision；缺少新版本的正式来源时，变更及后继创建在同一事务内拒绝、回滚。同版本 retry/reverification 也必须保留准确的已绑定运行身份。Local Operator 的普通启动命令同样不独立签发来源，只能使用已经正式绑定的候选。

已完成确认的重放先只读核对原 request ledger，原请求即使遇到过期 writer epoch 也不重写业务事实。修改 variant、runtime 或 service tier 不会被原提交 hash 掩盖。无 Run 后继命令新增不可变的完整原请求身份；旧版本未保存 actor 的 receipt 保持原样，重复提交会明确返回 `ORIGINAL_COMMAND_IDENTITY_UNAVAILABLE`，不会从当前 Task 或 caller hash 猜测、补写历史 actor。已有后继事实仍可读取。

## 运维入口

关闭占用同一 DataRoot 的 Core 后，使用将要运行服务器的同一构建显式采集资格：

```sh
devbridge -mode qualify-runtime -data-root /absolute/data-root \
  -qualification-backend EXACT_BACKEND_ID \
  -qualification-role IMPLEMENTATION \
  -qualification-request UNIQUE_REQUEST_ID
```

该命令使用隔离的本地兼容性测试，不调用在线模型。它输出准确映射，原始证据保存在 DataRoot 的 `platform-evidence/compatibility`。资格描述实际制品，因此更新 Core 或 Codex 后必须重新采集；已有 revision binding 不会自动改绑到新制品。

## 验证记录

本地详细证据保存在 `build/remaining-constraints-2026-10-02/`：

- 已安装 Codex 0.159.2 的独立兼容性采集、证据损坏和错误 schema 负例、资格映射、请求竞争与冷恢复；collector race 检查通过。
- 真实 Task → Upper Review 证据报告 → 原确认 journal 的 T1/T2/T3 故障注入、冷恢复、同请求并发和精确历史重放通过。阶段检查覆盖没有多写 Run、准备授权、source、binding 或 start effect。
- 公开 Facade 的真实 native `APPROVE_AND_START` 与 NO_START → adoption 两条链均通过实际双仓库写入、实际验证命令、两次成功 Run、冷重开及原请求零写重放。
- 三个生产二进制的公开 HTTP/MCP 验收覆盖审批、显式资格采集、双仓库实施与验证、ACCEPT、Task 提交、FeatureArea 合并及三进程冷重放。
- 正式定义归档定向回归通过；runtime/platform、selection、definition、confirmation domain 及 runtime consumer 定向回归通过。
- 旧协议故障测试采用显式 `_test.go` 依赖替身，继续通过真实业务归档、source 和 binding；这些结果只验证协议与恢复行为，不能作为 native 兼容性证明。未给生产代码增加跳过资格的开关。
- Card 样本通过真实 MCP 流程重新采集；JavaScript 回归 196 通过、4 跳过、0 失败。相关 Go 包的 `go vet` 通过。

最终生产构建验收见 `packaged-native-final.log`，公开三进程生命周期 18.60 秒通过；两个真实 native Facade 分支见 `native-formal-final.log`，29.800 秒通过。`all-production-compile.log` 记录 `cmd/...` 与 `internal/...` 全部包编译成功。`qualification-effective-path-green.log` 验证后端留空时采用 Core 执行路径、显式后端路径优先，与实际启动规则一致。

实际 `controlapi` 全包初次回归暴露了旧测试的空初始版本和直接启动假设，修复后按失败集合及关联路径复验，没有再次运行整个包：

- Direct/Upper/启动组合 27 项通过（`direct-upper-startup-failure-set.log`，193.268 秒），另外 3 个共用入口用例通过（`direct-helper-dependent-recheck.log`，11.058 秒）。
- 定义审批、Local MCP、显式选型、noRun 故障、Operator 边界和冻结选型通过，见 `definition-runtime-regression.log` 与 `definition-runtime-negative-regression.log`；前者的三个旧错误码断言已由后者 20.779 秒通过覆盖。
- Coding/恢复/用户决定组 24 个顶层测试中的 21 项首轮通过（`coding-runtime-recovery-regression.log`，165.390 秒）；其余三项由 `coding-ready-final.log` 和 `continuation-binding-atomic-green.log` 的最终通过覆盖。
- 后继原子拒绝及同版本准确选型重试通过（14.270 秒）；原正式审批前缀回归通过（5.981 秒），既有 TaskAcceptance 回归通过（9.993 秒）。修复前的原子性失败保留在 `continuation-binding-atomic-red.log`，便于对照。
- 最终 `affected-go-vet-final.log` 无诊断，`git diff --check` 通过。

冻结核对结果见 `skeleton-read-only-verification.json`：470 个文件摘要全部不变，无新增、删除或修改，tree 为 `942d056a9c78a7e1b28319f4e535bdc893b3509a`。这里仅校验文件一致性，没有运行 skeleton。

独立兼容性策略证明本项目使用的本地 Trusted Executor 协议能力，不声明远程模型质量、在线供应商可用性或恶意宿主隔离能力。
