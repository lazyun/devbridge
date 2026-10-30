# Review wakeup capability fallback

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-2cb021061121c727)。

CHATGPT_REVIEW_WAKEUP_FALLBACK_IMPLEMENTED

MANUAL_CHATGPT_WAKEUP_ACCEPTANCE_PENDING

## 基线与修改

Starting HEAD：`4ed8e3e7aaa584f4f7f46190b6273e2a068d754f`；branch：`dev_20260908_1913`；repository：`/Users/phoenix/Documents/devbridge`。

开始时已有上一轮未提交的 `dev_bridge_ui.html`、`dev_bridge_review_wakeup_test.js`、新增 `dev_bridge_host_capability_test.js`、Host diagnostics 报告/证据，以及 signature-gap 报告。全部保留并在同一代码基础上继续实现。

本轮修改：上述三份 Card 生产/测试文件；signature-gap 报告追加 resolution；diagnostics 报告追加后续适配说明；新增本报告及证据目录。没有修改后端状态机、GPT 工具目录、审批、正式数据或展示/selector 业务逻辑。

## 官方契约

[OpenAI Developers Plugin UI Reference](https://developers.openai.com/plugins/reference#windowopenai-component-bridge) 声明组件 follow-up API 的 prompt 和可选 scrollToBottom。已核对 [官方 examples 类型文件](https://github.com/openai/openai-apps-sdk-examples/blob/main/src/types.ts) 的 Promise<void> 返回类型。签名缺口已解除。

生产调用唯一参数对象：

```javascript
window.openai.sendFollowUpMessage({prompt:messageText,scrollToBottom:true});
```

没有 undocumented fields；静态测试核对唯一调用和对象全文。Promise 完成只按 request-returned 处理，不声称 durable delivery 或 GPT turn 确认。

## 原始与当前调用链

原始：`requestReviewWake → reviewWakeIdentity → wakeReceipt/wakeInFlight → deliverReviewWake → 保存 IN_FLIGHT → 标准 message gate → server claim → ui/update-model-context → fresh read + identity fence → ui/message → receipt`。

当前：同一 completion detection/identity/dedupe → `reviewWakeTransport` 在发送前选择 → 同一 claim、context、fresh read → 保存 messageAttempted fence → `sendReviewWakeupMessage` → 保存保守 receipt。业务层不构造两套消息或分别实现 transport 错误重试。

`reviewWakeTransport` 和既有 diagnostics 共用检测结果。标准布尔 true 与仓库已有的标准空 capability object `{text:{}}` 均支持；字符串、数组等 malformed 声明不误判。ChatGPT function 与标准声明独立检测，不根据 Host 名称或 UA 猜测。

| 调用前 capability | transport | 实际发送 |
| --- | --- | --- |
| 标准 message 支持（无论 extension 是否存在） | STANDARD_UI_MESSAGE | 只发 ui/message |
| 标准缺失/false，extension 为 function | CHATGPT_SEND_FOLLOW_UP | 只调用公开签名一次 |
| 两者均不可用 | MANUAL_FALLBACK | WAKEUP_UNSUPPORTED / HOST_FOLLOW_UP_UNSUPPORTED，零发送 |

model-context update 与消息仍是两个步骤：现有 context 缺失/失败契约继续阻止发送，不把其成功当新 turn。diagnostics selectedPath 表示所选 transport，不表示 context gate 已通过；实际阻断记录在 receipt.contextDiagnostic / lastAttempt。

## 去重、失败和保守成功语义

identity/key 保持：aiSessionId/projectId/featureAreaId/taskId/reviewId/runId/resultSha256/provenanceSha256/decisionId/currentState/target。两条 transport 共用完全相同的短 messageText，包含 “this is not user approval”，不传 Review 长文、不触发 decision_respond。

新增 presentation receipt 字段：path、messageAttempted、claimAcquired、deliveryGuarantee、两项布尔 capabilitySnapshot。只扩展现有 widgetState/privateContent receipt 和 runtime Map，不写业务 SQLite。

- 调用前保存 messageAttempted=true。即使响应丢失或 teardown，已实际尝试发送的事件不允许 manual/background 重发。
- WAKEUP_SENT（包括旧版本 receipt）与 WAKEUP_UNKNOWN 均阻止重发；重挂载/polling 不因 transport 改变生成新 completion event。
- claimAcquired 只在取得原 server claim 后记录。前置 context 失败可按原契约手动继续自己取得的 claim；另一张 Card 的 ALREADY_ATTEMPTED 不作为手动跨卡发送许可。
- 标准调用 resolve：WAKEUP_SENT + STANDARD_UI_MESSAGE_REQUEST_RETURNED + UNCONFIRMED。
- extension resolve：WAKEUP_SENT + CHATGPT_FOLLOW_UP_REQUEST_RETURNED + UNCONFIRMED。
- extension throw/reject：WAKEUP_FAILED + 有界固定分类；不记录异常 payload、不重发。
- timeout/close/teardown：WAKEUP_UNKNOWN。extension 采用现有 15 秒 RPC timeout，并在 dispose 时结束等待；迟到 resolve 不覆盖 UNKNOWN。
- extension 未返回约定 Promise：UNKNOWN / UNEXPECTED_RETURN_TYPE，不将 undefined 当成功。
- 标准普通失败为 FAILED，timeout/closed 为 UNKNOWN；所有已经尝试过的标准调用都不会再切到 extension。公开错误契约不能证明未接受，因此未实现“失败后自动 fallback”。

UI：成功仅显示“已向 ChatGPT 请求继续处理 Review。”；不支持显示“Review 已完成。当前 Host 不支持自动通知 GPT，请在对话中继续处理 Review。”；UNKNOWN 提示先检查当前对话以免重复；FAILED 表达通知失败而非 Review 失败。投递详情单独展示 path、diagnostic、UNCONFIRMED。

## 实际 mock

生产 Card VM 的 `message.text=false + sendFollowUpMessage=true` 测试输出，完整记录见 [mock-extension.json](archive/chatgpt-review-wakeup-fallback.md#file-958c25152145b63c)。

```json
{
  "arguments": {
    "prompt": "[Dev Bridge] Review completion event. Exact identity: {\"aiSessionId\":\"session\",\"projectId\":\"project\",\"featureAreaId\":\"area-a\",\"reviewId\":\"review-a\",\"runId\":\"\",\"resultSha256\":\"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\",\"provenanceSha256\":\"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\",\"decisionId\":\"REVIEW_APPROVAL:review-a\",\"currentState\":\"REVIEW_APPROVAL_PENDING\",\"taskId\":\"semantic-task\",\"target\":{\"kind\":\"REVIEW\",\"operationId\":\"review-a\",\"ownerTaskId\":\"semantic-task\"}}. Follow the current Dev Bridge instructions; this is not user approval.",
    "scrollToBottom": true
  },
  "receipt": {
    "status": "WAKEUP_SENT",
    "claimAcquired": true,
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

这是隔离 fixture，不是真实 ChatGPT 发送成功证据。测试断言调用次数恰好一次、完整参数对象相等、ui/message 次数为零，随后 refresh/remount 不重发。

## 自动测试

`node --test internal/mcpserver/*test.js`：837 total / 833 PASS / 4 SKIP / 0 FAIL（[日志](archive/chatgpt-review-wakeup-fallback.md#file-7bb0500f7338e87c)）。计数包含导入共享 harness 注册的重复基础测试；四项 candidate 外部 fixture 未提供，仍跳过。

覆盖标准优先、extension fallback、无能力、malformed/absent window.openai、context 阻断与允许的前置重试、reject/throw/timeout/dispose/未返回 Promise、迟到 Promise、标准 timeout/普通失败绝不切换、UNKNOWN/SENT remount、不同 Review、新 digest、精确 identity、消息语义、唯一公开参数对象、跨卡 server claim。

`go test ./internal/mcpserver` PASS（[聚焦日志](archive/chatgpt-review-wakeup-fallback.md#file-1339b6ace83bd854)）。生产 HTML 嵌入资源及协议断言通过。

## 真实 ChatGPT 手动验收：NOT_RUN

1. 在允许的隔离环境展示 Card，使用 fixture/隔离 Review completion；不要操作已有真实 Review/Decision。
2. 打开 Host capabilities，记录 message.text 原值、sendFollowUpMessage function 检测、model-context update 能力。
3. 记录 selectedPath 和实际 receipt 的 status/path/diagnostic/deliveryGuarantee，并人工观察聊天中是否出现新消息/turn；区分 Promise 返回和聊天实际观测。
4. 分类 A：标准支持 → STANDARD_UI_MESSAGE；B：标准不支持且 extension 存在 → CHATGPT_SEND_FOLLOW_UP（仍受原 context gate 约束）；C：两者均无 → MANUAL_FALLBACK；D：结果未知 → UNKNOWN，不重发。
5. refresh/remount 应保持原 receipt；不同 Review 才允许新的通知。不要响应审批来证明 wakeup 成功。

本轮没有启动浏览器/Desktop，没有实际 Host 消息、真实 Review/Decision 或 Agent 操作；不声称 ChatGPT 实际支持或成功执行 fallback。未 commit/merge/push/deploy。

最终验证：`go test ./...` **PASS**（[全量日志](archive/chatgpt-review-wakeup-fallback.md#file-0ad499f46e7911c2)）；`go build ./...`、`go vet ./...` 均 exit 0、无输出（[记录](archive/chatgpt-review-wakeup-fallback.md#file-555cfbb9f666e165)）；`git diff --check` PASS。真实 Agent smoke 已显式禁用。

最终 HEAD 与 Starting HEAD 相同。工作树保留原有诊断修改并包含本轮 fallback、测试与报告，未提交。完整文件状态见 [working-tree.txt](archive/chatgpt-review-wakeup-fallback.md#file-f58dce88b84a88df)。

## 后续修正：context update 可选

本报告中“context gate 阻止发送”属于 fallback 阶段的历史行为。后续 CONTEXT_OPTIONALITY_FIX 已移除该错误依赖；context unsupported/failed/timeout 不再阻止安全复核后的 follow-up，并对两类诊断独立记录。当前行为与验证见 [context optionality 报告](chatgpt-review-wakeup-context-optionality.md)。
