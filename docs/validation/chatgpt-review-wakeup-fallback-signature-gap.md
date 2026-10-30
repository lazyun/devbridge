# Review wakeup fallback：调用签名证据缺口

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-39bdf63f30aa1a01)。

状态：IMPLEMENTATION_BLOCKED_SIGNATURE_EVIDENCE_MISSING。没有实现 fallback，不能标记 CHATGPT_REVIEW_WAKEUP_FALLBACK_IMPLEMENTED。

## 基线

Repository：`/Users/phoenix/Documents/devbridge`；branch：`dev_20260908_1913`；Starting HEAD：`4ed8e3e7aaa584f4f7f46190b6273e2a068d754f`。

进入本轮已有未提交的 Host capability diagnostics：`internal/mcpserver/dev_bridge_ui.html`、`dev_bridge_review_wakeup_test.js`、新增 `dev_bridge_host_capability_test.js`，以及 `docs/validation/chatgpt-host-capability-diagnostics.md` 和证据目录。全部保留。本轮只新增本报告，没有修改生产逻辑或测试。

## 当前实际调用链

```text
初始化/工作区更新/手动总结
  → requestReviewWake
  → reviewWakeIdentity：验证精确、当前、已成功完成且待审批的 Review
  → wakeReceipt + wakeInFlight：按现有 identity key 去重
  → deliverReviewWake
  → 保存 WAKEUP_IN_FLIGHT 到本地 Map / widgetState.privateContent.reviewWakeups
  → 检查标准 hostCapabilities.message.text
  → dev_bridge_review_wakeup_claim
  → 标准 ui/update-model-context
  → refresh 并重新检查当前 identity / generation
  → 标准 ui/message
  → WAKEUP_SENT / WAKEUP_FAILED / WAKEUP_UNKNOWN
  → 保存 receipt、更新 runtime-local diagnostics 与 UI
```

identity 包含 aiSessionId/projectId/featureAreaId/taskId/reviewId/runId/resultSha256/provenanceSha256/decisionId/currentState/target；key 为现有 identity values 的 JSON。没有改 dedupe key。

当前缺少标准 message 声明会在 claim/message 前产生 HOST_MESSAGE_UNSUPPORTED。上轮诊断可以分别检测 extension，但不调用它。context update 失败不等同消息已发送，现有路径会停止消息发送。标准发送超时/连接关闭后记录 UNKNOWN；background refresh/remount 不重发非 NOT_ATTEMPTED 的 receipt。当前 manual Retry 对 UNKNOWN 仍可用；本轮尚未修改，不能宣称已达到新任务的全部重复发送安全要求。

## 本地证据检索

在整个仓库执行含 hidden/ignored 文件的 sendFollowUpMessage 搜索，排除 .git、日志、source maps、验证报告和 dist：

```sh
rg -n --hidden --no-ignore -g '!.git/**' -g '!docs/validation/**' -g '!*.log' -g '!*.map' -g '!dist/**' 'sendFollowUpMessage' .
```

命中仅为：

- dev_bridge_ui.html：typeof feature detection 与诊断字符串。
- dev_bridge_host_capability_test.js：`sendFollowUpMessage(){...}` 或无参数函数 mock。
- dev_bridge_review_wakeup_test.js：无参数 mock，证明当前实现不调用它。

没有本地官方 SDK/type declaration、真实 host bridge 方法定义或实际调用样例。desktop/frontend/package.json 仅声明 Vite 开发依赖，未提供 OpenAI UI SDK。上述 mock 不能证明实际签名，不能据此选择 string、{text}、{prompt} 等参数形式。

## 停止依据与后续所需证据

用户本轮任务明确要求：

> 如果本地代码库没有足够证据确认签名：停止实施该调用，报告证据缺口。不要靠试错构造生产调用。

因此本轮没有构造或试发 extension 调用，也没有以网络资料替代这一明确的本地证据前提。

继续实施需提供可核验的官方 SDK/type declaration 或当前 host bridge 契约，确认参数结构、返回类型、Promise 接受语义与错误/超时语义。具备证据后再实现单一 transport adapter：标准优先，仅调用前标准能力缺失时选择 extension；任何已经尝试发送后的未知结果都不跨 transport 重发。不存在明确未接受的错误契约时，不实现失败后自动 fallback。

## 验证与工作树

本轮只有证据报告，未运行 Node/Go/build/vet；上一轮验证不能当成本轮 fallback 的验证。git diff --check 通过。

真实 ChatGPT Host fallback 验收：NOT_RUN。没有实际 fallback mock 输出，未执行真实 Review/Decision、浏览器或 Desktop 操作。未 commit/merge/push/deploy。最终工作树保留上一轮全部诊断改动，并新增本报告。

## Resolution：SIGNATURE_EVIDENCE_RESOLVED

后续用户明确解除本地类型声明前提，允许以 OpenAI 官方公开契约作为生产依据。本节取代前述阻断状态，保留此前检索记录作为历史。

已实际读取 [OpenAI Developers：Plugins → Reference → window.openai component bridge](https://developers.openai.com/plugins/reference#windowopenai-component-bridge)，公开签名为 `window.openai.sendFollowUpMessage({ prompt, scrollToBottom })`。

也已读取 [OpenAI 官方 examples 的 src/types.ts](https://github.com/openai/openai-apps-sdk-examples/blob/main/src/types.ts)：`(args: { prompt: string; scrollToBottom?: boolean }) => Promise<void>`。

公开类型没有 durable-delivery ACK；Promise fulfillment 在实现中仅作为 request-returned，deliveryGuarantee=UNCONFIRMED。不能证明新 GPT turn 已建立。

后续实现、自动验证和真实 Host 待验收见 [fallback 报告](chatgpt-review-wakeup-fallback.md)。
