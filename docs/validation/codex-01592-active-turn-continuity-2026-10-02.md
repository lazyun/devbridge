# Codex 0.159.2 运行中重连验收

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-399110dbb715769f)。

结论：真实 `codex-cli 0.159.2` 的原实例 active-Turn 重连、正式资格绑定后的 Core 重启恢复、Core 被 `SIGKILL` 后恢复均已通过。恢复保留原 Run、Thread、Turn、App Server 和运行资格；待答请求保留原 Request，已记录答案保留原 UserDecision。

支持范围是 **独立 App Server 仍存活，原 Thread/Turn 仍加载，原资格与身份校验通过**。新运行仍显式启用 `DEVBRIDGE_CODEX_APP_SERVER_CONTINUITY=1` 或 `controlplane.Config.AppServerContinuity`；已有 binding 在 Core 启动时恢复。历史 stdio 运行、App Server 自身死亡或替换不属于原 Turn 恢复范围。本轮未增加同一 Core 内的后台自动重试策略。

## 修复内容

1. **版本门禁**：保留 0.149.0，新增精确 0.159.2；实测版本必须匹配显式版本要求，其他版本与带后缀的未验证构建仍拒绝。
2. **Unix socket 别名**：0.159.2 将指定 listener 路径创建为指向私有 daemon socket 的符号链接，旧 `Lstat` 检查因此误拒绝。现在同时绑定链接 inode、最终 canonical 路径和 socket inode，检查两端 owner 与父目录权限。替换链接或目标仍使旧身份失效；历史 direct socket identity 保持兼容。
3. **恢复权限**：原启动授权属于旧 Core epoch，不能用新启动的 epoch 条件验收原 Turn。新增 `ValidatePreparedRejoin`，沿持久化的 exact Run/Thread/Turn/binding/connection 恢复回执追溯原启动 epoch；保留 workset、observation、owner、revision、topology/context 检查。原启动授权和 workset binding 全行保持不变，旧授权仍不能用于新启动。
4. **任务阶段**：启动扫描只延后确属 App Server 恢复队列的原 Run/claim 判定，不提前将其 Task 改为 `RECOVERY_REQUIRED`。独立取消、正式恢复记录、交付不确定性仍生效。实际重连失败时，失败观察和当前原 Execution 的 Task 恢复状态在同一事务中记录。

## 实际验收矩阵

最终统一门禁：`build/codex-continuity-01592-2026-10-02/gate.SqnZql/`；总入口日志：`build/codex-continuity-01592-2026-10-02/final-gate.log`，退出码 0。

| 层次与场景 | 结果 | 核验事实 |
|---|---|---|
| native 协议：waiting / recorded | PASS | 同进程、socket、Thread、Turn、request ID 与完整 payload；明确答案后原 Turn 完成 |
| native 协议：同文多请求 | PASS | 两个 Thread/Turn 各自保留请求身份，回答不串线 |
| native 协议：普通推理中断 | PASS | 保持原 Responses 流，重连未新增模型请求 |
| native 协议：已消费但确认丢失 | UNKNOWN，安全断言 PASS | completed/idle 不充当消费确认，不推断成功或重发 |
| 正式资格 + Core waiting | PASS | 连续两次 Close/Open；两个新 writer epoch、新 connection，原问题与业务阶段不变；两条恢复回执可追溯原启动授权 |
| 正式资格 + Core recorded | PASS | 持久原答案跨 Core 重开，对原请求有效发送一次，原 UserDecision 不变 |
| 正式资格 + Core inflight | PASS | 原流继续、原 Turn 完成，provider 请求总数 1 |
| 正式资格 + Core lost | PASS | 关闭原实例后启动不同身份实例；原 Run 不改绑、不向替代实例回答，Task 进入恢复状态 |
| 正式资格 + Core SIGKILL | PASS | 单独 Core 子进程被强制结束；原 native 存活，新 Core 恢复原 Turn，provider 请求总数 1 |

Core 测试使用真实 authored Review、native 资格采集、正式批准、精确绑定和公开启动接口。没有用手工插入的资格数据替代资格流程。SIGKILL 测试同时比较重启前后完整启动授权/workset binding 行；所有 Core 场景检查原 App Server receipt 和唯一 Implementation Run。

所有 Responses 内容由隔离的本地回环 provider 确定性提供；App Server、Thread、Turn 和请求处理来自实际安装的 native。未调用线上模型、未读取或复制用户认证、未修改 live DataRoot 或全局 Codex 配置。fixture 的 `SCOPE_CONFLICT` 只用于结束原 Turn，不等同于业务实现或 Verification 成功。

## 固定 native 与协议证据

- native：`/Users/phoenix/.nvm/versions/node/v23.11.1/lib/node_modules/@openai/codex/node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex`
- 版本：`codex-cli 0.159.2`
- binary SHA-256：`16593cc2f422d5f398a8e40f550ebbaf1245392528957be342c295920a300704`
- probe 规范化 schema SHA-256：`f68680250816b7422adccbde86c5616379ab3b570cd9e1c64894190e77285985`
- 使用字段兼容检查：`build/codex-continuity-01592-2026-10-02/continuity-schema-fields.json`。

`initialize`、`thread/loaded/list`、`thread/read`、`thread/resume`、请求/回答和完成事件的使用字段兼容；保留原手选 wire 类型的 0.149.0 生成来源，不将局部兼容声明为整个 0.159.2 协议面覆盖。[官方 App Server 文档](https://learn.chatgpt.com/docs/app-server) 提供方法语义；上述固定版本的运行中恢复能力以本地 wire 和真实 Core 证据为准。

每个 `native-*` 目录保存 `result.json`、`wire.jsonl`、导出 schema 和实例事实。`core-*` 目录保存 qualification evidence、provider 请求、原 binding、最终 Run 和权限快照；强制终止场景另存 child/recovered identity 与 `result.json`。

## 回归与复现

```sh
DEVBRIDGE_PLATFORM_NATIVE_BINARY=/absolute/path/to/codex-0.159.2 \
  scripts/verify-codex-continuity.sh
```

可用 `DEVBRIDGE_CONTINUITY_EVIDENCE_DIR` 指定输出父目录。脚本每次创建独立证据目录，顺序执行五种 native 协议场景、App Server 身份/版本测试、资格恢复门禁、SQLite 工作集/生命周期定点测试，以及 Core 重开和 SIGKILL 测试；任一失败即退出。需要允许本地 loopback 与 Unix socket。

最终 `appserver.log`、`qualified-recovery.log`、`recovery-store.log`、`core-native.log` 均 PASS；Core 两个顶层测试及四个 reopen 子场景无 SKIP，执行耗时 63.314 秒。SQLite 回归覆盖独立取消不被 binding 掩盖、缺少 Thread/Turn 的运行不能延后恢复、工作集扩权拒绝和既有生命周期约束。

另有 transport race、supervisor/store 定点回归通过，日志为 `transport-continuity-race.log`、`continuity-store-supervisor.log`；受影响七个包的静态检查记在 `affected-vet-final.log`。`skeleton-read-only-final.json` 确认 470 个约束文件 SHA-256 与原基线全部一致，未修改或运行 skeleton 整包回归。

真实失败证据保留：`core-qualified-old-gate-red.log`（版本门禁）、`core-qualified-symlink-red.log`（socket 别名）、`core-qualified-replay-diagnostic.log`（原授权 epoch）、`gate-lifecycle-red.log`（Task 阶段）。后续 GREEN 不覆盖这些失败记录。

## 保留的边界

`serverRequest/resolved` 在答案已消费后丢失时，0.159.2 实测仍无精确消费确认回读；`CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED` 保留，状态保持 `UNKNOWN`，不得自动重发。多 owner Core 恢复、全部取消/重连竞态、原生 systemError 故障和线上模型质量不在本轮验收声明内。

本轮关闭的是“真实 Codex 0.159.2 active-Turn 重连尚未验证”这一缺口，不发布整个 continuity 协议或整个项目全部完成的声明。
