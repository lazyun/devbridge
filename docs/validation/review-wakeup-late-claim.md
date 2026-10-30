# Review wakeup late claim ordering closure

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-6e4b6df802591e02)。

REVIEW_WAKEUP_LATE_CLAIM_ORDERING_FIXED

MANUAL_NEW_REVIEW_WAKEUP_ACCEPTANCE_PENDING

## 基线与修改范围

Starting HEAD：`4ed8e3e7aaa584f4f7f46190b6273e2a068d754f`；分支 `dev_20260908_1913`；仓库 `/Users/phoenix/Documents/devbridge`。

开始时已有前序 Host diagnostics、fallback、context optionality 的未提交实现、测试、报告/证据。保留全部工作，不回退、不清理。本轮新增修改集中在：

- `internal/mcpserver/dev_bridge_ui.html`：late claim 顺序、claim RPC unknown、ALREADY_ATTEMPTED 文案与诊断；claim 后只保留本地 fencing、widget receipt 和既定消息调用。
- `internal/mcpserver/dev_bridge_review_wakeup_test.js`：顺序、零 claim、并发、claim failure/race、legacy manual 测试。
- `internal/mcpserver/dev_bridge_ui_test.js`：VM harness 记录实际 RPC/receipt marker 事件及调用时的 receipt 状态，生产脚本没有增加测试 hook。
- `internal/mcpserver/review_wakeup.go`：仅 description/comment，ledger 与 eligibility 代码不变。
- `internal/mcpserver/review_wakeup_test.go`：description、ineligible 不占用 reservation、有/无 RunID 并发共享 reservation 的契约覆盖。
- `docs/interfaces.md`、本报告与证据；前序 context optionality 报告追加顺序修正说明。

没有 SQLite migration、新实体、release/reset、TTL、claim 清理、新工具或业务状态机修改。

## 根因与顺序

旧顺序：

```text
local eligibility → CLAIM → optional context → fresh read → exact eligibility
→ capability selection → widget message marker → SEND
```

process-local ledger reserve 一旦成功即不可撤销。context、fresh read、eligibility 或 transport 不可用均可能发生在 reservation 之后，使尚未发送消息的 identity 永久占用本进程 attempted entry。新 Card 无法证明没有发送，因而只能 ALREADY_ATTEMPTED / UNKNOWN。

新顺序：

```text
local identity/retry fence
→ optional context (RETURNED / UNSUPPORTED / FAILED / UNKNOWN)
→ fresh authoritative read
→ exact current identity / generation revalidation
→ host transport selection
→ no transport: UNSUPPORTED, zero claim
→ late CLAIM
→ final synchronous currentWake
→ claimAcquired + messageAttempted
→ save widget receipt + final local fence
→ one selected Host message call
```

messageText 和 transport 均在 claim 前准备。claim 后没有 context update、workspace refresh、其他工具、能力探测或等待预处理。最后仍检查 receipt 存储期间是否发生导航，安全失效时不发送。

## Identity 与跨 Card 去重

客户端 exact identity/key 原样保留，包括 Session/Project/FeatureArea/Task/Review/Run、hashes、Decision、currentState、target。服务端仍调用 eligibleReviewWakeup 读取 canonical projection，并检查当前 Task、Review SUCCEEDED、Approval PENDING、精确 Decision、hash/provenance 和可选 Run。

现有 server key 会将 RunID 归一为空，以让旧版无 RunID 与新版有 verified RunID 的 Card 共用同一预约；本轮没有改变这一历史兼容逻辑。服务端仍用 mutex 将查重/写入原子化，容量耗尽 fail-closed，不释放或过期。

两个 Card 可以分别完成 context enhancement 和 fresh read；随后争抢同一 late claim，仍只有一个 CLAIMED。context 可能发生两次，它不是聊天消息或业务 mutation，不能因此把 claim 提前。

## ALREADY_ATTEMPTED、manual 与 residual race

ALREADY_ATTEMPTED 表示当前 MCP process 有该 exact completion 的预约/尝试历史，不能证明 Host delivery。自动或没有本地 legacy proof 的新 Card 返回 WAKEUP_UNKNOWN + ALREADY_ATTEMPTED + UNCONFIRMED；不发送、不换 transport。UI 明确提示已有先前通知尝试、是否送达无法确认。

claim RPC failure/timeout 同样不能证明服务端未写入，因此返回 UNKNOWN 和 bounded claim diagnostic，禁止自动或按钮重申。messageAttempted 仅在 transport 已选、claim 成功、最终 identity 仍匹配时设置；context/refresh/capability discovery 不设置它。

manual 边界保持：SENT、UNKNOWN、messageAttempted=true 不重发；未申请 claim 的 preflight failure 可以显式重试；仅 previous.claimAcquired=true 且未 messageAttempted 的本地 legacy receipt 可保留既有 manual ALREADY_ATTEMPTED 兼容。新代码不再在 preflight 阶段产生这种 claim；不得凭新 Card 的 ALREADY_ATTEMPTED 自行制造 proof。

Residual race：CLAIMED 之后、消息调用之前 Card 仍可能 stale/disposed。此时牺牲 notification liveness，保留 reservation，不向错误上下文发送；既有 process-local at-most-once 方案无法同时消除这段窗口。没有增加两阶段协议或释放接口；进一步闭合只能作为独立架构候选。

## 根因回归的实际事件证据

新 fixture identity，message=false、followUp=true、context=false。实际 VM 事件与 claim 时的 receipt 快照如下：

```json
{
  "sequence": [
    "dev_bridge_card_get",
    "dev_bridge_review_wakeup_claim",
    "MESSAGE_ATTEMPT_MARKED",
    "CHATGPT_SEND_FOLLOW_UP"
  ],
  "atClaim": {
    "contextStatus": "CONTEXT_UPDATE_UNSUPPORTED",
    "claimAcquired": false,
    "path": "CHATGPT_SEND_FOLLOW_UP"
  },
  "receipt": {
    "status": "WAKEUP_SENT",
    "claimAcquired": true,
    "contextDiagnostic": "CONTEXT_UPDATE_UNSUPPORTED",
    "contextStatus": "CONTEXT_UPDATE_UNSUPPORTED",
    "path": "CHATGPT_SEND_FOLLOW_UP",
    "messageAttempted": true,
    "deliveryGuarantee": "UNCONFIRMED",
    "capabilitySnapshot": {
      "standardMessage": false,
      "chatgptFollowUp": true
    },
    "diagnostic": "CHATGPT_FOLLOW_UP_REQUEST_RETURNED"
  },
  "messageCount": 1
}
```

[完整事件样例](archive/review-wakeup-late-claim.md#file-8e703a29e4f908e4)。fresh read 已发生且 contextStatus 已记录后才 CLAIM；claim 时 messageAttempted 尚未设置，随后 marker 和 extension 恰好一次。标准 ui/message 也断言相同顺序。

## 自动测试

Node 全量：865 total / 861 PASS / 4 SKIP / 0 FAIL（[日志](archive/review-wakeup-late-claim.md#file-a06b845f655fbda9)）。共享 harness 导入包含重复基础测试；四项外部 candidate fixture 缺失仍跳过。

主要新增结果：

- context 四种状态 × standard/extension：实际 trace 满足 PREPARE → CLAIM → MARKER → SEND。
- 无 transport：claim=0，ledger size=0；随后明确能力升级与 manual retry 首次 CLAIMED 并发一次消息。
- preflight Task/Decision/Review/hash/Run/generation/dispose/read failure：claim=0、message=0、无 message marker。
- 两 Card 都完成 preflight 后竞争：ledger size=1，总 message count=1；另一个 UNKNOWN/ALREADY_ATTEMPTED，无 delivery 声明。
- CLAIMED 响应后 navigation/dispose：message=0，ledger 保留1；新 Card 仍 ALREADY_ATTEMPTED，不清理旧预约。
- claim RPC failure/timeout：UNKNOWN、一次 claim、零 message，不允许重申。
- legacy local proof 的 manual 兼容有独立测试；新 Card 无 proof 不绕过去重。
- Go 真实 SDK 验证不合格 authoritative projection 不占用 reservation；原 mutex/capacity/eligible/silent 契约继续通过。32 个混合有/无 RunID 的并发 reserve 只有一份 CLAIMED。

`go test ./internal/mcpserver` PASS（[日志](archive/review-wakeup-late-claim.md#file-f9e6a3d00110dd38)）。静态 review 确认 server production diff 仅注释/description，未改变 attempted map 操作。

## 当前历史 Review 未重新发送

未调用正式 MCP endpoint、未读取/修改正式业务 DB、未启动浏览器/Desktop/真实 Agent，未 kill/restart 服务。本轮所有发送、claim 和新 Review 都是 Node VM 或 Go 临时/内存 fixture。`review_fdd6b35ab6932fd84323c293fd3a9e00` 没有作为任何调用参数或自动测试 identity，也没有删除其 attempted entry、修改 hashes、创建假 Decision。

这说明本轮没有尝试重发历史 Review；没有检查其当前真实 Host delivery 状态，不能宣称历史 UNKNOWN 已修复。已消耗的历史 reservation 可以继续保持 UNKNOWN。

## 真实新 Review 手动验收：NOT_RUN

后续仅使用获准的新隔离 Review completion identity。记录 Host capability、contextStatus、fresh read、claim、message path/diagnostic，以及聊天实际观测。无 transport 必须零 claim；两 Card 同时观察时只允许一条消息；失败 preflight 不占用 claim。

不能使用既有 ALREADY_ATTEMPTED 的历史 Review 验收，也不能为此重启正式服务或清 ledger。真实 ChatGPT 新 Review 验收本轮 **NOT_RUN**；Promise 返回仍为 request-returned / UNCONFIRMED。

未 commit/merge/push/deploy，不标记 GLOBAL_READY。

最终验证：`go test ./...` PASS（[全量日志](archive/review-wakeup-late-claim.md#file-6084fd7363f79580)）；最后的 machine-code 诊断更新后 `go test ./internal/mcpserver` 再次 PASS；`go build ./...`、`go vet ./...` 均 exit 0、无输出（[记录](archive/review-wakeup-late-claim.md#file-97e7bec0588c1a9b)）；`git diff --check` PASS。真实 Agent smoke 显式禁用。

最终 HEAD 保持 Starting HEAD。未提交工作树包括原有诊断/fallback/context optionality 与本轮 late claim、测试和报告；[完整文件状态](archive/review-wakeup-late-claim.md#file-49e5d0e1d811bfde)。未清理其他工作、未提交或部署。
