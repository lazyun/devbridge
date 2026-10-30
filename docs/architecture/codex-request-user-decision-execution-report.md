# CODEX_REQUEST_USER_DECISION 执行记录

本轮独立工程状态恢复的合同、实施与未闭合边界见 [Recovery Status 记录](codex-recovery-status-reconciliation-v1.md)。新 Recovery Turn 不证明旧回答被消费；旧 UNKNOWN 与普通 finalization guard 保留。

本轮 CODEX_REQUEST v1 的权威状态、UNKNOWN 阻断、取消/successor、统一客户端投影及验收见 [回答消费状态合同](codex-request-unknown-disposition-v1.md)。该合同保留固定 0.149.0 的 `CODEX_RESPONSE_RESOLUTION_READBACK_PROTOCOL_BLOCKED`，历史报告中的阶段性状态不代表本轮验收结论。

后续校正：固定 0.149.0 的 loaded Thread resume 会重投递原 pending request，已由真实离线 App Server 实验验证；本文件原重连限制属于此前实现状态。新的协议证据、v79 接线和仍无法回补 consumed/resolved 的边界见 [跨 Core continuity 记录](codex-request-cross-core-continuity.md)。整体仍为 partial，不能据此宣称全部 closure。

本轮状态：**PARTIAL_IMPLEMENTATION**。不声明 `CODEX_REQUEST_USER_DECISION_CLOSED`。

主要功能已经落地并通过隔离的生产链 fixture；未完成项是固定 App Server 协议下的跨 Core 重启 request 重绑与原 dispatch 恢复。完整领域、API、生命周期及协议证据见 [用户决定契约](codex-request-user-decision.md)。

## 工作树与数据边界

- Starting / Final branch：`dev_20260908_1913`。
- Starting / Final HEAD：`fd8a6afeb213972e8aed00ff23f090ae058b8530`。
- Starting schema v77；Final schema v78，仅测试自有数据库运行 migration。
- Starting / Final index：空；未暂存、提交、合并、push 或切换正式分支。
- 继承 216 dirty entries；初始 1776 个非忽略文件的逐字节快照位于 `/private/tmp/devbridge-user-decision-baseline.zitam6bg`。
- 实际增量按该快照计算，而非相对 HEAD：修改既有 49 个文件、新增 19 个文件、删除 0 个文件；最终 268 dirty entries。完整路径清单位于 `/private/tmp/devbridge-user-decision-increment.json`。
- Git init/commit/worktree 等动作仅发生于本轮测试创建的临时仓库。未修改正式仓库历史、live DataRoot、用户 Codex 配置；未调用真实模型、外部服务、浏览器或 UI。

## 已实施

1. 复用 RuntimeRequest 与 `codex_server_requests`，增加 exact identity、request kind、payload/response schema hash 与状态。v78 新增不可变 UserDecision，以 Run FK 和原请求 composite FK 关联。
2. 提交先持久化决定；官方响应只能从该决定序列化。原始文本、所选选项、多问题逐项答案保持；elicitation 大整数不经过 float64 改写。直接 model-authored raw response 不能消费 CODEX_REQUEST。
3. WAITING_FOR_USER → USER_DECISION_RECORDED → RESPONDING → UNKNOWN → RESPONDED；backend ack 才是消费确认。未发送失败可重试原决定；已写入但无确认不会重发或再次询问。CANCELED/STALE 不复活。
4. GPT mandatory-user-response 写入实际 ChatGPT 与 Local MCP server instructions。AGENTS.md、项目建议、通知成功、历史偏好都不是回答。只读 hash preparation 不创建决定或发送响应。
5. CODEX_REQUEST 与 RUNTIME_APPROVAL 分别 admission。生产 fixture 在同 Turn 产生两者，先处理权限审批仍保持问题未回答，错 kind 被拒绝。
6. Implementation、Verification、Coding Review、Inspection 使用 canonical response。等待期间保留 Run/Thread/workset 与既有 Task phase；回答不扩大 revision 或 VerificationSpec。
7. STOP_AND_REVISE/CANCEL_OPERATION 使用既有 cancellation ledger。Inspection 加入该链；取消 ack 不冒充 quiescence。Coding Review 取消后 exact Task 恢复 REVIEW_REQUIRED，旧 Review 不变。明确的新确认创建 successor revision/Execution/workset；旧回答不能用于新请求。
8. operation/workspace projection、ChatGPT Card、WebUI Session workbench 和 Console 展示原始问题及选项，无默认选项。Desktop 嵌入同一 HTTP UI。已回答 UNKNOWN 显示为“回答已记录，响应状态未知”；refresh/wakeup 不提交回答。
9. Card 的 exact persisted request claim 与通知只影响通知状态；消息成功或失败均不创建 UserDecision。真实 Codex smoke 默认关闭，必须由操作者写入当次明确回答。

## 未完成：跨重启续接

固定协议的 `ThreadReadResponse` / `ThreadResumeResponse` 不返回 pending server-request identity 或未消费证明；`serverRequest/resolved` 仅是实时通知。当前 Session waiter 绑定旧连接，Core 重启后不能安全重绑。

已验证：实际 ControlPlane 关闭重开后，UNKNOWN 与原 UserDecision 保留，exact replay 返回原决定，没有第二次 backend response。

未实现：重启后将旧 WAITING 问题恢复为可回答的同一个请求，以及将 USER_DECISION_RECORDED/NOT_SENT 接回原 backend dispatch。也没有权威的 UNKNOWN → pending-proof → safe-resend 路径。当前保留证据并 fail closed，不以新 Run/Thread、猜测答案或假协议补齐。因此完整 G6 / 第十一节不满足，不能报告 CLOSED。

## 测试与证据

所有 Go 日志均使用 `go test -json`，区分顶层测试、子测试、FAIL 与 SKIP；下列组可能重叠，不能相加为独立用例总数。编译采用 `-run '^$'`，不计作功能测试。

| 测试组 | 顶层 / 子测试 | PASS / FAIL / SKIP | 原始日志 |
|---|---:|---:|---|
| 生产链集中回归 | 29 / 16 | 44 / 0 / 1 | `/private/tmp/devbridge-user-decision-production-final.json` |
| 新增用户决定生产链（上行子集） | 8 / 11 | 19 / 0 / 0 | `/private/tmp/devbridge-user-decision-production-final.json` |
| Runtime 全包 | 27 / 29 | 56 / 0 / 0 | `/private/tmp/devbridge-user-decision-agentruntime-full-final.json` |
| Runtime/SQLite 定点领域与迁移 | 10 / 16 | 26 / 0 / 0 | `/private/tmp/devbridge-user-decision-core-final.json` |
| SQLite runtime/迁移扩展组 | 11 / 14 | 25 / 0 / 0 | `/private/tmp/devbridge-user-decision-storage.json` |
| 七项 repaired execution + 七项 runtime 安全回归 | 14 / 9 | 23 / 0 / 0 | `/private/tmp/devbridge-user-decision-execution.json` |
| UserDecision race | 5 / 7 | 12 / 0 / 0 | `/private/tmp/devbridge-user-decision-race.json` |
| MCP + Local discovery 定点 | 9 / 0 | 9 / 0 / 0 | `/private/tmp/devbridge-user-decision-mcp-targeted-final.json` |

集中生产组包含 1 个 helper 顶层 PASS，不计为能力证明；1 个真实 smoke SKIP，不计 PASS。新增用户决定生产链是该组的子集，不重复计数。

- Workbench + exact wakeup JS：3 PASS / 0 FAIL。Console canonical user-decision JS：1 PASS / 0 FAIL。
- Root 与 Desktop 编译均 exit 0；定点 vet exit 0；`git diff --check` exit 0。
- `REAL_CODEX_REQUEST_E2E_NOT_RUN`；没有执行真实 backend smoke。


集中命令：

```sh
GOCACHE=/private/tmp/devbridge-go-cache go test ./internal/controlapi -run '^(TestRuntimeUserDecisionProduction|TestMultiRepositoryCoding|TestMultiRepositoryUpper|TestRealCodexRequestUserDecision)' -count=1 -json
GOCACHE=/private/tmp/devbridge-go-cache go test ./internal/agentruntime -count=1 -json
GOCACHE=/private/tmp/devbridge-go-cache go test -race ./internal/agentruntime -run 'TestUserDecision' -count=1 -json
GOCACHE=/private/tmp/devbridge-go-cache go test ./internal/storage/sqlite -run '^(TestRuntimeUserDecision|TestAgentRuntime|TestMigration|TestInspectionCancellation)' -count=1 -json
GOCACHE=/private/tmp/devbridge-go-cache go test ./... -run '^$'
# 同一编译命令也在 desktop/ 下运行。
GOCACHE=/private/tmp/devbridge-go-cache go vet ./internal/agentruntime ./internal/codex ./internal/probeexecution ./internal/controlapi ./internal/storage/sqlite ./internal/bridgehttp ./internal/mcpserver
node --test internal/bridgehttp/product_session_user_decision_test.js internal/mcpserver/dev_bridge_runtime_request_wakeup_test.js
node --test --test-name-pattern 'runtime user decision' internal/bridgehttp/product_ui_test.js
git diff --check
```

迁移运行自带 `foreign_key_check`；历史请求缺少完整身份时不会通过当前 owner 或默认值获得新回答权限。已有 v77/v76 数据和账本保留。

## 继承失败与本轮修正

本轮修正了旧测试中的异步 telemetry 读取竞争、metadata envelope/schema 过时断言，以及双仓库 wire 断言两次遍历 map 导致偶发丢失 expected root 的问题。

扩大测试范围仍可见旧 fixture/产品契约不匹配：SESSION_INITIALIZATION_REQUIRED、旧 Review author source、缺失 FeatureArea agent context、旧 `project_open`/`task_create` 工具与 Card entry API 期望。初始快照重跑确认了代表性 Go 失败与旧取消同步 gate 失败；不能算作本轮能力 PASS。

- Card 旧全量 JS：初始快照与本轮均 106 tests，3 PASS / 103 FAIL。新增 exact-request wakeup 单独验证。
- Desktop 全量 JS：49 tests，48 PASS / 1 FAIL（旧 Console 标题断言）。Desktop 编译结果单独记录。
- MCP loopback 限制已通过获准的本机随机端口重跑排除；重跑仍出现旧 Session admission/工具目录 fixture 失败，不伪装成沙箱原因或成功。
- 较大 cancellation 套件中旧 Session/Review/上下文 fixture 和同步 gate 仍失败；本轮新的生产取消链、Inspection 和已修复七项 execution fixtures 单独验证。

## 状态保留

```text
MULTI_REPOSITORY_CODING_EXECUTION_V1_CLOSED
TRUSTED_EXECUTOR_MODEL_V1
CODEX_SANDBOX_HARDENING_DEFERRED
PARTIAL_IMPLEMENTATION
REAL_CODING_AGENT_E2E_NOT_RUN
REAL_CODEX_REQUEST_E2E_NOT_RUN
SAFE_REPOSITORY_UNLINK_CLOSED
MANUAL_DESKTOP_ACCEPTANCE_PENDING
MANUAL_CHATGPT_CARD_ACCEPTANCE_PENDING
```

真实 smoke 的 SKIP 不计 PASS。未声明 GPT 自动回答、hostile executor sandbox 或 Project-centric 全平台完整实现。
