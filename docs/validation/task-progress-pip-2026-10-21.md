# ChatGPT PiP 任务进度接入（2026-10-21）

## 实现

- 新资源 `ui://devbridge/card/read-only-pip-v3` 声明 `inline`、`pip`、`fullscreen`；保留此前三个资源 URI 的兼容读取。
- 在选定 Task 的工作区提供“悬浮查看”，悬浮视图提供“返回对话”和刷新。展示名称、Task 阶段、全流程阶段条、待处理项、阻塞与最后读取时间。
- 按宿主能力请求显示模式，兼容 OpenAI 显示桥接。宿主未声明能力时，仅在用户点击后尝试显示请求；不自动探测，不把未确认的请求当成成功。只有宿主确认的返回模式才用于切换；拒绝、超时或不可用时继续当前展示。
- 接收宿主显示模式通知，支持手机端将 PiP 呈现为全屏。保留原内联展开/收起状态。
- 悬浮时使用已有准确身份与只读刷新链，包括等待审批阶段。从收起入口进入也持续监测；隐藏或卸载停止读取。
- 只有 Task 成功、失败或取消后请求回到内联；Review 或 Run 结束不会被误认为整个 Task 结束。宿主拒绝自动退出时不循环请求。
- 没有新增业务写工具，没有消费批准、启动实施或修改报告；普通对话和通知订阅继续工作。

## 验证

| 检查 | 结果 |
| --- | --- |
| `node --test internal/mcpserver/dev_bridge*test.js` | 132 PASS，3 SKIP，0 FAIL |
| 新增 PiP 行为用例 | 9 PASS，覆盖声明、无能力回退、标准桥接、OpenAI 兼容桥接、移动全屏、拒绝/超时、模式通知、任务终态和卸载 |
| `go test -p 1 ./internal/mcpserver` | PASS |
| 资源声明与旧 URI 的真实 MCP 读取 | PASS，三种模式、同一组件与只读策略 |
| `go build -p 1 ./cmd/...` | PASS |
| 修改文件空白检查 | PASS |

测试使用临时构建缓存与本机 HTTP 测试服务器。没有运行项目全量测试；本轮不改动此前验证方案准入及交付恢复问题。

托管服务版本：`0.1.0-dev.pip-20261021.5+gd3969cf`。
实际 Tunnel `bridge_diagnostics` 返回 `ready=true`。更新前无活动 Run，旧版二进制保留供回退。

## 真实宿主验收

在既有 ChatGPT Work / Cloud 对话展示同一 Task，保持普通对话和原 Events 订阅。首次出现沙箱加载失败，使用宿主“重试”后可挂载；没有变更宿主安全设置。

页面最初继续执行旧脚本，摘要为 `5c8cd413c3d9861d9c271b1cea0456eaa3ecad021a67246461dd2fe51529232a`。在现有插件设置执行“刷新工具”，再展示新卡片后，公开 DEBUG 确认实际脚本摘要为 `c315c8e7fe000ad0e25b1b9d9d5f2c6e8eb74cc4b5233a70e6d1187b28002b5b`，与最终源码一致。

| 真实行为 | 结果 |
| --- | --- |
| 普通对话内联卡片 | PASS |
| 宿主侧栏中的紧凑任务进度、阶段条与待审批提示 | PASS |
| 悬浮布局沿用同一任务身份并持续只读刷新 | PASS，侧栏显示模式下观察到读取时间推进 |
| 从宿主侧栏返回内联卡片 | PASS |
| 当前入口切换为真正 PiP 浮窗 | NOT_VERIFIED，最新版卡片在此入口仍未获得 PiP 选项；不把侧栏视为 PiP 成功 |

真实任务仍为 `REVIEW_APPROVAL_PENDING`，没有当前 Execution，Events 订阅为 `ACTIVE`，SQLite quick check 为 `ok`。本机截图保存在被忽略的 `build/pip-evidence/chatgpt-progress-sidebar.png`，包含私人对话界面，不纳入公开仓库。

## 官方依据与边界

- [Plugin UI reference](https://developers.openai.com/plugins/reference)：资源显示模式声明、初始化声明与 `window.openai.requestDisplayMode`。
- [Add UI to your MCP server](https://developers.openai.com/plugins/build/chatgpt-ui)：通过 MCP Apps 桥接接入，能力探测与普通工具兼容。
- [UI guidelines](https://developers.openai.com/plugins/concepts/ui-guidelines)：PiP 用于并行于对话的持续活动；宿主控制显示，结束后回到内联。

PiP 是 ChatGPT 内的悬浮展示，不是系统级桌面置顶窗。移动端可能显示为全屏。不支持组件或显示模式的客户端仍保留普通对话能力。
