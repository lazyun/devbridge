# Codex App Server 跨 Core continuity：固定协议与实施记录

2026-10-02 更新：当前精确版本门禁支持 0.149.0 和 0.159.2。真实 0.159.2 已通过原实例 active-Turn 重连、正式资格绑定下连续两次 Core 重启和 Core SIGKILL 恢复；socket 别名身份、旧启动授权跨 epoch 恢复以及 Task 阶段误判已修复。当前支持范围与可复跑证据见 [0.159.2 验收记录](../validation/codex-01592-active-turn-continuity-2026-10-02.md)。下文 G0–G11 与 `/private/tmp` 路径保留为 0.149.0 初版历史，不代表本次 0.159.2 验收结果。

本轮独立工程状态恢复的合同、实施与未闭合边界见 [Recovery Status 记录](codex-recovery-status-reconciliation-v1.md)。新 Recovery Turn 不证明旧回答被消费；旧 UNKNOWN 与普通 finalization guard 保留。

本轮 CODEX_REQUEST v1 的权威状态、UNKNOWN 阻断、取消/successor、统一客户端投影及验收见 [回答消费状态合同](codex-request-unknown-disposition-v1.md)。该合同保留固定 0.149.0 的 `CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED`，历史报告中的阶段性状态不代表本轮验收结论。

初版记录状态：`PARTIAL_IMPLEMENTATION`。pending request 的真实协议能力已经证明，不能再沿用“协议不支持 pending rebind”的旧结论；但不声明 `CODEX_REQUEST_CROSS_CORE_APP_SERVER_CONTINUITY_CLOSED` 或 `CODEX_REQUEST_USER_DECISION_CLOSED`。0.149.0 和本次实测 0.159.2 在“回答已消费、原 resolved 通知丢失”窗口均未提供可重放的消费确认，G5 仍有协议边界。

## 基线与操作范围

Starting branch `dev_20260908_1913`，HEAD `fd8a6afeb213972e8aed00ff23f090ae058b8530`，schema v78，index 空。继承 268 条 dirty status，1795 个非忽略文件字节快照位于 `/private/tmp/devbridge-continuity-baseline.vkv2sbxh`。本轮增量相对此 manifest 计算，不相对 HEAD 混算。

未暂存、提交、切换分支或修改用户 live DataRoot/CODEX_HOME/Codex binary。实验仅使用自有 provider、数据目录、socket、进程和 Git fixture。未读写 Codex SQLite；没有自研请求 Broker、真实模型调用或 Desktop UI 操作。

## 实际 binary 与协议

Desktop 已有设置选择 `codex`，PATH launcher 为 `/Users/phoenix/.nvm/versions/node/v23.6.0/bin/codex`；实际 JS launcher 为 `.../@openai/codex/bin/codex.js`，SHA-256 `134063e133f0b4244fa3b251acf973d4fe4b4aeeacbdc135211bf480f59f1477`。

实际 native 为 `/Users/phoenix/.nvm/versions/node/v23.6.0/lib/node_modules/@openai/codex/node_modules/@openai/codex-darwin-arm64/vendor/aarch64-apple-darwin/bin/codex`，SHA-256 `f4a74117b8142cda581c95ff753abf4508b5636d89682c1ed77e4a9249af8963`，版本 `codex-cli 0.149.0`。

初版生成的实际 schema、版本和 help 在 `/private/tmp/devbridge-continuity-g0/`。G0 规范化 schema 摘要为 `c6ce758605922a1064829eb74a1c2ecafb39a5a2440e28e1b40d12600cf25e31`。每个生产 instance receipt 从所选 native 重新生成 experimental schema 并记录有序 JSON 文件 SHA-256。初版只开放 0.149.0；当前另开放已验证的 0.159.2，其他版本仍拒绝。

App Server 支持 stdio、Unix socket、WebSocket listener。Unix socket 上承载 WebSocket framing，不能按 stdio JSON lines 直接读写。Client 复用原 JSON-RPC transport/server-request processor，只增加 Unix WebSocket 字节适配。

| 能力 | 固定协议 | 实际离线验证 | 生产边界 |
|---|---|---|---|
| loaded Thread 列表 | `thread/loaded/list` | 是 | 原实例分页读取 |
| active / idle / exact Turn | `thread/read(includeTurns=true)` | 是 | 无业务推进权 |
| 原实例重连 | initialize + Unix WebSocket | 是 | process/socket/binary/epoch 校验 |
| 原 Thread 订阅 | loaded Thread 的 `thread/resume` | 是 | 仅原 active Turn，禁止 cold resume |
| pending request 重投递 | resume 后自动 replay | 是 | exact transport ID + method +完整 payload 匹配 |
| 新连接响应旧 request | 原 JSON-RPC ID response | 是 | 必须有不可变用户决定 |
| consumed response 确认重放 | 无 observed query/replay | 否，实际未重放 | idle/terminal 不足以判 RESPONDED |
| 旧/new instance 区分 | 协议无 instance token | OS identity 已验证 | Bridge durable binding，非 pid 单独判定 |

本地固定版本 source 的 `outgoing_message.rs::replay_requests_to_connection_for_thread` 与 `request_processors/thread_lifecycle.rs` 解释了 pending replay；此前仅查看 schema 无法得出该结论。source inspection 与真实运行分别记账。

## G0 原始证据

可复运行 `scripts/probe_codex_continuity.py --executable <native> --output <新的短临时路径> --scenario waiting|recorded|consumed|multi`。本地 Responses provider 确定性返回 `request_user_input`；真正的 App Server 创建 Thread/Turn/request，provider 不冒充 App Server。所有 prompt、答案都是测试预设内容。

- waiting：`/private/tmp/dbcc-g0-wait-2/{result.json,wire.jsonl}`。原 Thread `01a0ada7-25bc-7f62-9170-6c62d04b5469`、Turn `01a0ada7-25c5-7e01-a157-c6eae14992ee`、request ID `0`；B 收到同 ID、同 payload，回答后原 Turn 完成。process/socket 不变。
- recorded：`/private/tmp/dbcc-g0-recorded-1/{result.json,wire.jsonl}`。先保存预设回答，不发送，A 断开；B 收到原 Q 后发送该回答并收到 resolved。此 G0 的 JSON 文件只证明 wire 实验，不冒充生产 UserDecision store。
- consumed：`/private/tmp/dbcc-g0-consumed-metadata/{result.json,wire.jsonl}`。A 已发送且 provider 第二次调用证明实际消费，A 不读取 resolved 即断开。B 看见原 Thread idle、原 Turn completed，但没有 replay Q 或 resolved。记录 `UNKNOWN`、`recoverability=false`；不能从测试 provider 的内部知识制造生产 RESPONDED。
- 同文多请求：`/private/tmp/dbcc-g0-multi-agent/{result.json,wire.jsonl}`。同 S1 两个 Thread/Turn，同文问题、不同 request；B 回答第一条后第二条仍 pending，C 再次重连第二条并回答。无串答。

补充完整 identity/schema 元数据后的重跑位于 `/private/tmp/dbcc-g0-{waiting,recorded,multi,consumed}-metadata/`，均保存 wire、pid/start/socket、instance UUID、schema hash。前三项恢复成功，consumed 的实验断言通过但恢复结论仍为 UNKNOWN。只清理本轮持有身份的进程；早期失败测试遗留的两个进程核验与退出证据见 `/private/tmp/dbcc-owned-orphan-cleanup.json`。

首次 sandbox loopback bind 被限制，发生在启动 S1 前；后续授权的本地进程/loopback执行完成实际 gate。该环境错误不算协议失败。

## 生产接线

`internal/appserver` 是最小进程 owner：启动独立 session、file-backed stdio，记录 receipt，校验/关闭 exact 进程；不解析 CODEX_REQUEST 或持有用户决定。Core 直接连接 S1。instance 身份包含随机 ID/epoch、OS 原生进程启动标识、pid、executable path/hash、version/schema hash、socket canonical path/dev/inode、DataRoot owner、CODEX_HOME、backend fingerprint。Mac 使用原生微秒级 process start，Linux 使用 boot identity/start ticks。相同 socket path 不足以复用。

生产 instance epoch 为新启动的微秒级 epoch，request/API 同时绑定随机 instance ID。DataRoot 记录 canonical path 与 dev/inode；bootstrap 校验实际 writer 所属 root，复制数据库到另一 root 或替换原 root 目录不能获得 S1 控制权。官方 npm launcher 解析到其本次选定安装下的 native，不另找替代安装。新实例初始化失败只回收该新实例；已恢复 Session 后执行器接管失败会关闭该 Session，而不终止 S1。

v79 仅新增 binding/checkpoint/reattachment receipt/observation，并给现有 epoch 不可变触发器增加严格 receipt 约束的 adoption。原 UserDecision 与旧 request FK 保留；原 semantic RuntimeRequest ID 不变，新 connection generation 有独立历史行。旧 Core callbacks 与旧 epoch command 被拒绝。

Core 启动读取 bound candidates，验证原实例，读取 loaded/exact Thread/Turn/CWD，先建立当前 epoch Session，再订阅原 Thread，防止 replay 早于 owner。恢复同一个 executor checkpoint，不重新 prepare workspace、BeginRun、发送 prompt、StartTurn 或分配替代 Run。Implementation 的后续 Verification 仍走原批准 Execution 的 canonical callbacks。

新操作显式 opt-in：`DEVBRIDGE_CODEX_APP_SERVER_CONTINUITY=1` / `controlplane.Config.AppServerContinuity`。已有 bound operation 即使开关关闭仍尝试恢复。没有 binding 的历史 stdio operation 不视为可恢复。当前独立 probe runner 不接此 checkpoint 恢复路径。

Core graceful close 只 detach active transport；不能发隐式 JSON-RPC error 消费 pending Q，也不能 kill S1 或把原 Run 终态化。真正取消仍走 canonical cancellation 和原 Turn disposition。canonical terminal 后，只有 durable store 无 active owner 且 S1 所有 loaded Threads 都 idle，才可关闭 exact S1。launch 到 durable receipt 间崩溃仍可能留下未绑定进程；不会自动认领或用新实例替换。

## 状态与只读接口

- WAITING：exact replay 后继续展示同一问题；无用户决定不发送。
- RECORDED：exact replay 后仅序列化不可变原答案；不再次询问。
- RESPONDING/UNKNOWN：必须有同实例 exact pending replay 才可证明尚待回答；无证据保持 UNKNOWN。
- idle/terminal：不会反推已消费。丢失的 resolved 无法回补时仍 UNKNOWN。
- instance lost/mismatch：显示 `Codex 实时会话已丢失，原问题无法继续响应`，保留原问题与答案，隐藏回答动作，不发送到 S2。

`Snapshot.runtimeObservation` 和 `operation_get(include=RUNTIME_REQUESTS).runtimeObservations` 返回持久化观察：instance/epoch/status、connection、Thread/Turn/status、reattachment、timestamp、source method 与 failure reason。它不是业务 Decision。GET/refresh 不启动进程、不 resume、不发送答案。Console 文案区分等待、已记录、恢复发送、UNKNOWN 和 session lost。

## 验证边界

生产/协议 gate 的最终执行结果与本轮字节增量见文末验收表；不得把 store-only、transport fake、compile-only 或 SKIP 记作完整 Core reopen acceptance。

G5 当前不能满足“查询已消费并收敛 RESPONDED”；这是固定协议缺少确认重放的边界。不得因此抹去已证明的 pending replay，也不得用新的 Turn、Run、prompt 或读取 Codex SQLite 弥补。

保留：`MULTI_REPOSITORY_CODING_EXECUTION_V1_CLOSED`、`TRUSTED_EXECUTOR_MODEL_V1`、`CODEX_SANDBOX_HARDENING_DEFERRED`、`PARTIAL_IMPLEMENTATION`、`REAL_CODING_AGENT_E2E_NOT_RUN`、`REAL_CODEX_REQUEST_E2E_NOT_RUN`、`REAL_CODEX_REQUEST_RECONNECT_E2E_NOT_RUN`、`SAFE_REPOSITORY_UNLINK_CLOSED`、`MANUAL_DESKTOP_ACCEPTANCE_PENDING`、`MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING`。

## 实际验收结果

| Gate | 结果 | 实际证据与限制 |
|---|---|---|
| G0 | pending PASS；consumed UNKNOWN | 真正 installed App Server + 本地 Responses fixture；不是 fake server |
| G1 | graceful Core reopen PASS | 真实 controlplane.Close/Open、同 pid/start/socket/instance；强制终止 Core 子进程专项未执行 |
| G2 | PARTIAL | active/idle 真实协议，unavailable/instance loss 真实 Core；notLoaded/systemError 的解释有代码/transport 测试，未实际制造 native systemError |
| G3 | PASS | 同一 RuntimeRequest，当前 epoch/new connection，仍等待原用户回答，无新 Run/Turn |
| G4 | PASS | 原持久化 UserDecision 跨 SQLite/Core reopen，原请求 resolved，原 Turn completed |
| G5 | PROTOCOL_BLOCKED | 没有可补读的精确消费确认；不能强行收敛 RESPONDED |
| G6 | fail-closed 已验证，完整故障注入 PARTIAL | consumed G0 + durable UNKNOWN/精确 replay store 测试；真实 Core 丢失 resolved 故障专项未闭合 |
| G7 | PASS | S1 停止后新 S2 有不同 ID/epoch；Core 保留原决定、投影 LOST/UNKNOWN，不绑定或发送到 S2 |
| G8 | protocol PASS，production PARTIAL | 同 S1 两个同文请求的真实 wire 实验通过；多 owner Core reopen 集成尚未覆盖 |
| G9 | request continuity PASS | Implementation、Review、Verification 均真实 Core reopen；不把 fixture terminal 等同于 Verification 业务检查成功 |
| G10 | PARTIAL | store cancellation/old-epoch 拒绝 + detach 竞态回归通过；完整用户取消/重连/迟到 resolved 生产组合尚未全部覆盖 |
| G11 | 定点 PASS，广泛 suite 有既有阻断 | 详见下表；不声明全仓 runtime acceptance |

| 执行集 | 命中与结果 | 日志 |
|---|---|---|
| 真实离线 Core | 3 顶层、5 子场景 PASS；正常运行 30.571s | `/private/tmp/dbcc-core-offline-final.jsonl` |
| 原用户决定生产回归 | 8 顶层、11 子场景 PASS，0 SKIP | `/private/tmp/dbcc-userdecision-final.jsonl` |
| 多仓库 Coding/Direct 既有 gate | 24 顶层、13 子场景 PASS，0 SKIP | `/private/tmp/dbcc-prior-gates-final.jsonl` |
| migration/workset | 5 顶层、3 子场景 PASS；包含真实 72→78→79→79、历史 bytes/FK、v76/v77 | `/private/tmp/dbcc-history-final.jsonl` |
| transport | 8 新顶层通过 race；另有 quiescence 3 子场景，完整 codex 包通过 | 对应 agent 定点命令 |
| supervisor/store | 独立 supervisor identity、exact store reopen/immutable decision/race 通过 | 本轮定点执行 |
| MCP contracts | 6 顶层 PASS；2 顶层/2 子场景被既有 `Session admission unavailable` 阻断 | `/private/tmp/dbcc-contracts-network-final.jsonl` |
| JS | 2 测试 PASS；两份改动 JS syntax check PASS | `/private/tmp/dbcc-js-final.log` |
| root/Desktop | 全包 compile-only PASS，明确不计运行验收 | `/private/tmp/dbcc-root-compile-final.log`、`/private/tmp/dbcc-desktop-compile.log` |
| 定点 vet | PASS | `/private/tmp/dbcc-vet-final.log` |
| 真实模型 smoke | NOT_RUN；默认 gate SKIP 不计 G0 | `/private/tmp/dbcc-real-smoke-status.log` |

整组真实 Core race 的总 180s 超时发生在 Verification fixture setup；此前 Implementation 三子场景、Review 两子场景和两个顶层均 PASS，无 race 报告。Verification 独立 race 初次因原 15s fixture 等待不足而失败，扩大有界等待后通过；正常运行的全部三顶层亦通过。分别记录，不能把失败/超时的整条命令改写成 PASS。

Verification 最终源码独立 race：35.60s，PASS，日志 `/private/tmp/dbcc-verification-core-reopen-final-race.jsonl`。DataRoot owner 隔离之后的完整正常复验另存 `/private/tmp/dbcc-core-owner-final.jsonl`。App Server supervisor 3 个测试覆盖存活身份、未验证版本拒绝、DataRoot 目录替换；另在同测试中校验 copied-root 拒绝和同目录 symlink 接受。

广泛 `internal/execution` suite 存在旧 fixture 未提供 frozen backend selection 的失败，并在旧 approval correlation 测试等待至 600s；`/private/tmp/dbcc-execution-all.log` 保留失败，未声称全 suite 通过。

默认关闭的真实模型 smoke 为 `TestRealCodexRequestReconnect`。明确启用还需 `DEVBRIDGE_REAL_CODEX`、`DEVBRIDGE_REAL_CODEX_RECONNECT_CONFIG_FILE`、`DEVBRIDGE_REAL_CODEX_RECONNECT_AUTH_FILE`；仅复制明确指定的 fixture 到新 home，操作者在打印路径中提供 exact UserDecisionInput。不会读取默认用户 home 或生成语义答案。本轮未开启。

## 最终交接与字节审计

Starting / Final branch 均为 `dev_20260908_1913`；HEAD 均为 `fd8a6afeb213972e8aed00ff23f090ae058b8530`；schema v78 → v79（只迁移测试数据，未打开 live DataRoot）；index 始终为空。

继承 dirty 268 条。最终非忽略文件字节相对于开工 manifest 的增量见 `/private/tmp/devbridge-continuity-final-audit/manifest.json`、`increment.json` 与 `increment.patch`；这些路径保存本轮边界，不将继承未提交变更计入本轮。最终指标由同一审计脚本更新。

<!-- FINAL_INCREMENT -->
最终 dirty status 292 条；本轮变更 53 个文件（新增 26 个），相对开工 bytes +180312；新增 4121 行、删除 100 行。变化文件原字节 914642，最终字节 1094954。

本轮停止于固定协议确认缺失与表列未完成生产故障矩阵，不启动自研 Broker，不升级 binary，不用新 Turn/替代 Run/prompt 注入补齐。完整 continuity 两个 CLOSED 状态均不发布。
