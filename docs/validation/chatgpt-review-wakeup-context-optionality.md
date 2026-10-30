# Review wakeup：model-context update 可选增强

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-80ab2ef046bbd1d4)。

CHATGPT_REVIEW_WAKEUP_CONTEXT_OPTIONALITY_FIXED

MANUAL_CHATGPT_WAKEUP_ACCEPTANCE_PENDING

## 基线与范围

Starting HEAD：`4ed8e3e7aaa584f4f7f46190b6273e2a068d754f`；branch：`dev_20260908_1913`；repository：`/Users/phoenix/Documents/devbridge`。

进入本轮时已有 Host diagnostics / fallback 的未提交代码、测试、报告、证据和 signature resolution。保留这些改动；本轮继续修改 `internal/mcpserver/dev_bridge_ui.html`、`dev_bridge_review_wakeup_test.js`、`dev_bridge_host_capability_test.js`，新增本报告及证据，并在原 fallback 报告追加行为变更说明。

没有修改 Task/Review/Execution 状态机、审批/验收权限、GPT 工具目录或其他 Card 业务；没有提交、部署、浏览器/Desktop、真实 Review/Decision 或真实 Agent 操作。

## 根因与修复

此前 selectedPath 已能选择 CHATGPT_SEND_FOLLOW_UP，但 contextUndelivered 的 return/throw 把可选 model-context update 变成了消息发送硬前置。manual throw 又把 contextDiagnostic 复制到 diagnostic，造成重复详情。

现在依次执行：精确 completion identity 校验 → claim/dedupe → 可选 context（至多一次）→ 当前 identity 检查 → 权威 fresh read 和再次 fencing → 选择 transport → 原有 messageAttempted fence → 单一 message adapter。

context unsupported、普通失败、timeout 均继续到权威状态复核，不等于 Review 失败。组件失效、导航/Session/Project 改变、Review/Decision 不再 current 或 fresh read 失败仍会停止发送。没有为补偿 context failure 而创建长消息或另一轮业务操作。

## 独立状态与诊断

| Context 状态 | 含义 | 后续 |
| --- | --- | --- |
| CONTEXT_UPDATE_RETURNED | context RPC 返回 | 继续复核并选择消息 transport |
| CONTEXT_UPDATE_UNSUPPORTED | 当前未声明 context 能力 | 同上；零 context RPC |
| CONTEXT_UPDATE_FAILED | context RPC 普通失败 | 同上 |
| CONTEXT_UPDATE_UNKNOWN | context timeout/closed，或尝试开始但结果未记录 | 同上，前提是当前实例/identity 仍安全 |

新增 `receipt.contextStatus`，`contextDiagnostic` 只表达同一 context 结果。before-RPC 先持久化 UNKNOWN，保证中断/重挂载的允许手动继续不会再次请求 context。兼容读取上一轮 receipt.contextDiagnostic 的状态。unsupported 也记录到 receipt，不在同一 completion event 内反复探测/重试；不同 exact Review/digest 可尝试自己的 context。

follow-up 保持原 adapter：STANDARD_UI_MESSAGE 优先，其次 CHATGPT_SEND_FOLLOW_UP，再次 MANUAL_FALLBACK。`receipt.diagnostic` 表达消息请求结果，与 contextStatus 独立；`stateDiagnostic` 单独记录 widget state 存储不可用。

Promise fulfillment 仍为 WAKEUP_SENT + *_REQUEST_RETURNED + UNCONFIRMED，不是 durable-delivery ACK。message timeout/closed 为 UNKNOWN；已经尝试任意消息 transport 的事件绝不跨 transport 重试。context timeout 后 message 正常返回时，最终是 WAKEUP_SENT，而 contextStatus 独立为 UNKNOWN。

诊断 UI 用集合去除相同详情值，兼容旧 receipt 中重复的 CONTEXT_UPDATE_UNSUPPORTED；不把两种不同事实合并。Host diagnostics 的标准 message 路径原因不再被 context 缺失覆盖，lastAttempt 另含 contextStatus。

## Manual 与 automatic

两条路径共享完整流程和 transport 选择。automatic 不重跑已有 receipt；manual 只允许原有未实际发送、非 UNKNOWN 的情形，并需要合法 claim。两者都不再 throw contextDiagnostic。SENT、UNKNOWN 或 messageAttempted=true 的事件仍禁止重发。

已存在的旧版本 `WAKEUP_FAILED + CONTEXT_UPDATE_UNSUPPORTED` receipt 可以在原允许的 manual 入口继续，复用 context 结果、不重复 context、不改变 dedupe key。新增测试模拟了此场景。

## 关键 mock 的实际结果

Host fixture：message=false、followUp=true、context=false。生产 Card VM 中实际 extension 调用次数 **1**，ui/message **0**，context RPC **0**；精确消息包括非空 run-a 和 exact target，Promise resolve 后如下：

```json
{
  "arguments": {
    "prompt": "[Dev Bridge] Review completion event. Exact identity: {\"aiSessionId\":\"session\",\"projectId\":\"project\",\"featureAreaId\":\"area-a\",\"reviewId\":\"review-a\",\"runId\":\"run-a\",\"resultSha256\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"provenanceSha256\":\"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\",\"decisionId\":\"REVIEW_APPROVAL:review-a\",\"currentState\":\"REVIEW_APPROVAL_PENDING\",\"taskId\":\"semantic-task\",\"target\":{\"kind\":\"REVIEW\",\"operationId\":\"review-a\",\"ownerTaskId\":\"semantic-task\",\"runId\":\"run-a\"}}. Follow the current Dev Bridge instructions; this is not user approval.",
    "scrollToBottom": true
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
  }
}
```

完整样例：[mock-context-unsupported.json](archive/chatgpt-review-wakeup-context-optionality.md#file-b612c3bbe36e6453)。UI 详情中 CONTEXT_UPDATE_UNSUPPORTED 只出现一次，随后单独显示 CHATGPT_FOLLOW_UP_REQUEST_RETURNED / UNCONFIRMED。

## 测试

`node --test internal/mcpserver/*test.js`：852 total / 848 PASS / 4 SKIP / 0 FAIL，见 [Node 日志](archive/chatgpt-review-wakeup-context-optionality.md#file-3a80610bde63cd6c)。包含共享 VM harness 导入时的重复基础测试；四项外部 candidate fixture 保持跳过。

覆盖 context 四种结果 × extension resolve/reject；标准消息 + context unsupported；context 任意结果 + 无 follow-up；context timeout 不污染 follow-up 状态；manual 可继续与历史 failed receipt 兼容；同事件至多一次 context；不同 Review；remount/refresh 去重；消息 timeout 不跨 transport fallback；Review/Decision identity 失效阻止发送；新旧 receipt 详情不重复。既有 fencing/claim/transport 参数测试继续通过。

`go test ./internal/mcpserver` PASS，见 [聚焦日志](archive/chatgpt-review-wakeup-context-optionality.md#file-bcb24dd8ee2a6be1)。生产 HTML 嵌入资源验证通过。

## 真实 Host 手动验收：NOT_RUN

用户已报告实际 capability 组合为 standard message 不可用、ChatGPT extension 可用、context update 不可用；本轮自动测试使用相同组合的隔离 mock，没有自行操作真实 ChatGPT。

后续手动在获准隔离环境触发一个新的 Review completion，记录上述能力、selectedPath、contextStatus、message diagnostic 和聊天实际观测。应不再停在 context unsupported，应只请求一次 CHATGPT_SEND_FOLLOW_UP；详情区分 context unsupported 与请求返回，refresh/remount 不应生成重复消息。Promise 返回不能代替人工观测新消息/turn。

本轮真实 ChatGPT 验收 **NOT_RUN**，不声称已经在真实 Host 验证成功。

最终全量验证：`go test ./...` PASS（[日志](archive/chatgpt-review-wakeup-context-optionality.md#file-94a29569cceb6b25)）；`go build ./...`、`go vet ./...` 均 exit 0、无输出（[记录](archive/chatgpt-review-wakeup-context-optionality.md#file-8de6f72148513765)）；`git diff --check` PASS。真实 Agent smoke 显式禁用。

最终 HEAD 保持 Starting HEAD；工作树包含此前 Host diagnostics/fallback 与本轮可选 context 修复、测试和报告。未 commit/merge/push/deploy。最终文件状态见 [working-tree.txt](archive/chatgpt-review-wakeup-context-optionality.md#file-7320ae8df46943ec)。

## 后续顺序收口

本报告中先 claim 后 context/fresh read 的顺序已由后续 late-claim 修复替代。context 仍为可选增强，但所有 preflight 和 transport 选择都在 claim 之前。当前行为与验证见 [late claim 报告](review-wakeup-late-claim.md)。
