# Task 通知与 Codex 历史

普通 ChatGPT 对话继续支持项目、领域、任务、审查选择、确认卡片和手动刷新。**不需要切换到 Work / Cloud，也不需要订阅事件。** AI 文件审查仍由发起对话中的 AI 读取授权文件并提交报告；Codex 审查仍由选定的后台执行器运行。

以下两项是独立的可选增强。代码通过本地测试后仍需构建并重启 Core 才会生效。

## Work / Cloud 的可选主动通知

GPT MCP 接口在 `server/discover` 中声明 `events`，并实现 `events/list`、`events/subscribe`、`events/unsubscribe`。普通工具仍沿用原协议；事件接口单独要求 MCP `2026-07-28`。当前提供一个事件：`task.updated`。

用户明确选择一个 Task 进行订阅后，Task 状态、Run 状态、需要输入的运行请求和完成节点可产生通知。事件只包含 Session、Project、Task、Run 引用和变化类型；不包含报告正文、文件内容、运行问题原文或签名密钥。GPT 收到后必须通过现有工具读取当前事实，不能把通知当作批准或执行命令。

官方当前支持范围包括 ChatGPT 网页 Work 对话、桌面选择 Cloud 的 Work 对话及 dots。普通对话保留原卡片和手动查询；没有订阅时不会产生回调请求。[官方 MCP Events 文档](https://developers.openai.com/plugins/build/mcp-events)

### 设置与真实验收

1. 构建并重启更新后的 Core，MCP adapter 继续代理到同一 Core。升级会应用 SQLite migration 127，新增订阅和投递表。
2. 在 ChatGPT 插件设置中重新扫描 MCP 服务，确认事件列表出现 `task.updated`。
3. 在 Work / Cloud 对话中选择已绑定的 Project 和一个真实 Task，再明确要求：“监控这个 Task；完成、失败或需要我输入时提醒我。实施和验收仍由我另外确认。”
4. 确认宿主调用 `events/subscribe`，参数为准确的 `aiSessionId`、`taskId`。回调地址和 `whsec_` 签名密钥由宿主提供，不需要用户粘贴。
5. 关闭卡片，以正常业务操作产生一个状态变化，确认对话收到通知并回读正确 Task。检查重复通知没有重复提交业务操作。
6. 在对话中停止监控，确认 `events/unsubscribe` 后不再产生新的投递。
7. 回到普通对话，检查创建、审查选择、卡片和刷新仍正常；不应被要求启用 Work 才能使用这些功能。

2026-10-21 已更新本地托管服务、应用 migration 127，并确认新版服务发布 `task.updated`、新旧协议均可列出原有工具；ChatGPT 网页已执行“刷新工具”。随后在真实 Work 对话中通过 Tunnel 建立订阅，完成签名挑战验证并在 Core 持久化为 ACTIVE，宿主回读确认启用。**实际业务事件投递、GPT 提醒及停止订阅仍待下一次状态变化后验收。** 扫描完成、订阅成功、回调 ACK 与 GPT 实际收到通知是不同验收步骤。详细范围见[验证记录](../validation/task-events-and-codex-history-2026-10-21.md)。

### 投递与诊断

订阅在 Core 中持久化，默认有效期 24 小时；可请求 1 分钟至 7 天的有限有效期。`ttlMs: null` 使用默认有限有效期，不授予永久订阅。当前不提供历史重放，`cursor` 必须为空。

回调校验和投递都要求 HTTPS，禁止私网、回环、链路本地地址、环境代理和重定向。业务事务通过 outbox 保存待投递事件；重试保留原事件 ID，采用退避，最多尝试 8 次。永久拒绝停止该订阅，重试耗尽留存诊断状态。重启后继续未完成投递；已过期、停止或失去 Session / Project 归属的订阅不投递。

若系统 DNS 对域名返回 `198.18.0.0/15` 代理 fake-IP，回调客户端仅在这种情况下通过固定 Cloudflare DoH 端点重新解析域名，并再次校验、固定真实公网 IP 后连接。不会直接连接 fake-IP；字面 IP 和其他私网 DNS 答案仍直接拒绝。DoH 只发送域名，不发送回调路径、密钥、挑战或业务内容；TLS 校验、禁用代理和重定向的规则同样适用于这个解析请求。[Cloudflare JSON DoH 文档](https://developers.cloudflare.com/1.1.1.1/encryption/dns-over-https/make-api-requests/dns-json/)

Core 的本地 JSONL 日志记录 `mcp.events.requested/succeeded/failed`，订阅失败另有 `workflow.subscription.failed` 的固定阶段与原因代码，可区分协议、授权、DNS/连接、回调响应和持久化错误；日志不记录完整回调地址、签名密钥或原始网络错误。

本地只读诊断接口：

```text
GET /ui/api/tasks/{task_id}/notifications?project_id={project_id}
```

返回活跃订阅、停止订阅、待投递、已送达及重试耗尽的数量，不返回地址或密钥。`delivered` 只证明回调收件，不证明 GPT 已处理或任务已成功。

## Bridge 内查看真实 Codex 历史

Desktop 和 Web Console 的会话列表展示实际 `thread_id`；没有准确 Thread 的 Run 会明确提示。列表沿用 Bridge 的持久化 Run 归属，不读取其他私人 Codex 会话，也不依赖官方客户端默认的会话来源过滤。

选中一个具有 Thread / Turn 的 Run 后，在会话页点击 **读取 Codex 历史**。这是单独的只读请求：通过该 Run 原始且仍可核验的 App Server 实例，读取其准确 Turn 的持久化历史，并支持下一页。不会发送 `thread/resume`、`turn/start`、回答审批或启动模型。

原 App Server 离线、旧 Run 没有实例绑定或后端不支持分页时，页面显示不可用原因，并继续保留 Bridge 已采集记录及其历史分页。不会切换到当前默认后端，也不会启动替代进程。AI 文件审查没有 Codex Thread，不提供伪造的 Codex 历史入口。

```text
GET /ui/api/runs/{run_id}/session/native-history?project_id={project_id}&limit=20
GET /ui/api/runs/{run_id}/session/native-history?project_id={project_id}&limit=20&cursor={nextCursor}
```

可额外提供 `task_id` 进行准确归属检查。每页最多 50 项；页面最多保留 500 项原生历史。未知工具类型只展示记录标识，不返回工具参数或原始推理块。打开、切换、关闭页面均不恢复任务。

本功能改善 Bridge 内的索引与阅读，不保证官方 Codex Desktop / CLI 的默认列表自动显示 Bridge 会话。
