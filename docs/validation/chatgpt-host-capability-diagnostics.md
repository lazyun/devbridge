# ChatGPT Host capability diagnostics

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](README.md)。 整理前原文见[报告原文归档](archive/historical-reports.md#file-123c3cb4dd036a1e)。

CHATGPT_HOST_CAPABILITY_DIAGNOSTICS_IMPLEMENTED

MANUAL_HOST_CAPABILITY_PROBE_PENDING

## 基线与修改范围

Starting HEAD: `4ed8e3e7aaa584f4f7f46190b6273e2a068d754f`，分支 `dev_20260908_1913`。开始时工作树干净。

修改文件：

- `internal/mcpserver/dev_bridge_ui.html`：集中采集 helper、Card 设置里的折叠 Host capabilities、初始化 metadata 采集，以及 wakeup 最终结果的本地诊断。
- `internal/mcpserver/dev_bridge_host_capability_test.js`：新增能力矩阵、异常 getter、partial declaration、metadata、无 RPC UI、缓存隔离等 Node 测试。
- `internal/mcpserver/dev_bridge_review_wakeup_test.js`：真实 Card VM 中验证 extension 存在仍保持 manual fallback。
- 本报告及同名证据目录。

没有实现 sendFollowUpMessage fallback，没有改变 Review/workflow、权限或业务持久化。没有提交、部署、启动浏览器/Desktop、访问正式 Task/Review/Decision 或调用真实 Agent。

## 入口与实际 snapshot

展开 Card → ⚙ Card 设置 → **Host capabilities**。该项默认折叠，内容在用户展开时生成；更深层的“查看原始 capability snapshot（已脱敏）”也默认折叠。初始化失败的 Retry 页面也提供此诊断项。

`collectHostCapabilityDiagnostics()` 的结构：

| 字段 | 来源与含义 |
| --- | --- |
| hostInfo | ui/initialize.hostInfo 的 product/name/version 白名单，每个最多 96 字符；缺失为 `{status: "unavailable"}`，不使用 UA 或产品名称猜能力 |
| hostCapabilities | 当前初始化声明的 bounded/sanitized 结构；保留布尔值和对象形状，任意字符串/非声明值脱敏、敏感 key 排除；不是原样复制宿主 payload |
| mcp.messageText | message.text 的布尔 true 或标准 capability object；与 extension 分开 |
| mcp.updateModelContextText | updateModelContext.text 的布尔 true 或 capability object；不代表发消息或触发新 turn |
| mcp.toolsCall | 标准 serverTools 对象声明存在；无声明为 false/unknown，不主动发测试调用、不证明业务权限 |
| mcp.displayModes | hostContext.availableDisplayModes（优先）或声明中的 availableDisplayModes；只保留 inline/fullscreen/pip |
| mcp.visibility / recallPresentation | Dev Bridge presentation visibility/recall 的显式 true 声明 |
| declarations | messageText/updateModelContextText 的 supported / unsupported / unknown；缺省与 malformed 不当作 supported |
| chatgptExtensions | sendFollowUpMessage/setWidgetState/updateModelContext/callTool 的安全 typeof function；widgetState 是属性值是否非 undefined，不记录其内容 |
| runtime | initialized、presentationMode、hostCapabilitySourceAvailable、当前 presentationVisibility |
| reviewWakeup | 当前能力门控结果、原因、compatibleFallbackAvailable、extensionDiagnostic、解释文本和 lastAttempt |

所有信息只在当前 iframe runtime 和诊断 DOM 中使用。snapshot 在 initialize 成功后采集，打开面板重新 feature-detect 当前 extension；新挂载重新初始化，不读取 widgetState 里的诊断缓存、不写 SQLite/业务状态/日志体系、不产生网络或刷新动作。输入脱敏有 80 节点预算、最大 4 层、每层最多 24 项、key 最多 48 字符；DOM 使用 textContent 构造而非 HTML 注入。

## Wakeup 诊断枚举与行为边界

`selectedPath`：`STANDARD_UI_MESSAGE`、`MANUAL_FALLBACK`、`UNKNOWN`。它表达当前 capability gate 的判断，不宣称存在可投递的 Review 或已经发送。

`reason`：`STANDARD_MESSAGE_SUPPORTED`、`STANDARD_MESSAGE_UNSUPPORTED`、`MODEL_CONTEXT_UNAVAILABLE`、`HOST_INITIALIZATION_INCOMPLETE`、`HOST_CAPABILITIES_UNAVAILABLE`。

`extensionDiagnostic`：`CHATGPT_FOLLOW_UP_AVAILABLE_BUT_UNUSED` 或 `CHATGPT_EXTENSION_ABSENT`。extension 存在时始终明确说明当前实现未调用它，不将其伪装成标准 MCP 能力。

`lastAttempt` 独立记录现有 wake receipt 的 status、路径与枚举原因（另外包含 `REQUEST_FAILED`、`REQUEST_TIMEOUT`、`UNKNOWN`），不记录异常原文或 Review identity/payload。context update 超时与一般请求失败分开。`WAKEUP_SENT` 仍只表示标准 API 接受，不表示 GPT turn 已完成。

现有顺序和降级保持：标准 message 不支持就降级；标准 context update 缺失/失败就不自动发 message；原 claim、fresh read、manual retry、once/epoch fencing 不变。只有现有 finally 中增加本地结果记录。消息、model context、tools/call、UI state、visibility/recall 在 UI 中分别解释。

## Mock 的实际输出

以下由生产脚本 Node VM feature detection 生成，并非真实 ChatGPT 数据。完整结构见 [mock-extension-available.json](archive/chatgpt-host-capability-diagnostics.md#file-ec87fa4be30770da)。

```json
{
  "hostInfo": {
    "status": "unavailable"
  },
  "hostCapabilities": {
    "message": {
      "text": false
    },
    "updateModelContext": {
      "text": true
    }
  },
  "mcp": {
    "messageText": false,
    "updateModelContextText": true,
    "toolsCall": false,
    "displayModes": [],
    "visibility": false,
    "recallPresentation": false
  },
  "declarations": {
    "messageText": "unsupported",
    "updateModelContextText": "supported"
  },
  "chatgptExtensions": {
    "sendFollowUpMessage": true,
    "setWidgetState": false,
    "widgetState": false,
    "updateModelContext": false,
    "callTool": false
  },
  "runtime": {
    "initialized": true,
    "presentationMode": "unknown",
    "hostCapabilitySourceAvailable": true,
    "presentationVisibility": "HIDDEN"
  },
  "reviewWakeup": {
    "selectedPath": "MANUAL_FALLBACK",
    "reason": "STANDARD_MESSAGE_UNSUPPORTED",
    "compatibleFallbackAvailable": true,
    "extensionDiagnostic": "CHATGPT_FOLLOW_UP_AVAILABLE_BUT_UNUSED",
    "note": "ChatGPT follow-up extension is available but current wakeup implementation does not use it.",
    "lastAttempt": null
  }
}
```

测试另通过现有 Review wakeup fixture 触发实际 manual fallback，断言 extension 调用次数为 0、ui/message 与 ui/update-model-context 均未发起，lastAttempt.reason 为 STANDARD_MESSAGE_UNSUPPORTED。

## 自动验证

Node：`node --test internal/mcpserver/*test.js`，825 total / 821 PASS / 4 SKIP / 0 FAIL，见 [Node 日志](archive/chatgpt-host-capability-diagnostics.md#file-494cb79425493813)。此计数包含新测试导入现有 VM harness 时重复注册的 harness 测试。四个 candidate Card 外部 fixture 仍未提供，保持 SKIP，不代表真实宿主已验收。

原 Review wakeup 测试全部保留并通过。静态检查确认无 UA / host name 分支判断，无 extension 调用，无把 model context 当 message 的替代逻辑，无新增业务 mutation。

## 真实 Host 手动验收：NOT_RUN

1. 在真实 ChatGPT 展示 Dev Bridge Card，展开 Card → 设置 → Host capabilities。
2. 记录非敏感 hostInfo、runtime.initialized、hostCapabilitySourceAvailable；展开脱敏 snapshot 保存结果。
3. 逐项记录 hostCapabilities.message.text、hostCapabilities.updateModelContext.text、sendFollowUpMessage/setWidgetState 的 function 检测值、widgetState 属性存在性、serverTools / toolsCall、标准和 ChatGPT updateModelContext、displayModes、visibility/recall。
4. 分类：A：message.text 声明支持；B：message.text 不支持且 sendFollowUpMessage 存在；C：两者均未检测到；D：初始化或 capability declaration unavailable。缺失声明应保留 unknown，不把它当成已经验证过的 RPC 拒绝。
5. 若当前已有 wakeup 诊断，记录 selectedPath/reason/extensionDiagnostic/lastAttempt；不要为了探测新增 Review、响应审批或触发业务状态变更。
6. 本轮只收集脱敏证据，不调用 extension 测试消息，不根据结果自动实施 fallback。

当前真实 ChatGPT Host：**NOT_RUN**。没有足够真实诊断数据判断是宿主不支持，还是 Dev Bridge 未接入兼容接口。

最终 Go 验证：`go test ./internal/mcpserver` PASS（[聚焦日志](archive/chatgpt-host-capability-diagnostics.md#file-84e639ebeb3153de)）；`go test ./...` PASS（[全量日志](archive/chatgpt-host-capability-diagnostics.md#file-4d34a5802fd1c4e9)）；`go build ./...`、`go vet ./...` 均 exit 0、无输出（[记录](archive/chatgpt-host-capability-diagnostics.md#file-3df6012b1d51087d)）。`git diff --check` PASS。真实 Agent smoke 通过清空对应环境变量禁用。

最终工作树包含上述代码、测试和诊断报告，HEAD 保持 Starting HEAD；未 commit/merge/push/deploy。

## 后续能力适配

上述“extension available but unused”输出和 mock 属于诊断阶段的历史证据。后续任务已依据官方契约接入 compatibility transport；当前 selectedPath 可为 CHATGPT_SEND_FOLLOW_UP，extensionDiagnostic 为 CHATGPT_EXTENSION_PRESENT。能力 snapshot 保留，原始诊断证据不覆写。当前行为及新的测试结果见 [fallback 报告](chatgpt-review-wakeup-fallback.md)。
