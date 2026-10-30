# OpenAI 能力调研与 Dev Bridge 接入建议

调研日期：2026-10-21。依据：当日可访问的 OpenAI 官方文档、本地工作区源码，以及本机 `codex-cli 0.159.2` 导出的实验性协议 Schema。本文是接入建议，不表示相关能力已经集成或通过真实宿主验收；未调用付费模型、修改运行配置或创建订阅。

后续实现及验收边界见 [Task 通知与 Codex 历史](../user-guide/task-events-and-codex-history.md)。普通对话继续保留；主动事件通知是可选增强。下文的“项目现状”指调研时状态。

## 结论与优先级

最值得补充的是任务事件通知、会话历史查询和持续可见的任务界面。现有本地 Codex App Server 路径仍然适合项目；先补这些缺口，再评估新增云执行后端。

| 优先级 | 能力 | 对项目的价值 | 接入判断 |
| --- | --- | --- | --- |
| P0 验证 | MCP Events | 审查完成、等待输入、失败时，让订阅的 GPT 对话收到事件并按指令处理 | 先验证目标 Work/Cloud 客户端与 Tunnel 组合 |
| P0 实施候选 | App Server 会话列表、历史分页 | 在 Bridge 内找到真实 Codex 会话，取消后仍可查看历史 | 本机 Schema 已存在相应方法；需接入并测试 |
| P1 | Plugin Extensions | 提供会话旁任务面板、项目入口与报告查看界面 | 作为卡片之外的可选入口，按宿主能力降级 |
| P1 | App Server 运行中补充输入、原生代码审查 | 减少取消重建，增加对具体代码差异的审查 | 分别受冻结范围与现有报告合同约束 |
| P1 可选 | Workspace Agents API 触发 | Bridge 可触发已发布的工作区 agent，并得到 ChatGPT 对话链接 | 需要工作区配置及专用令牌，不能触发任意普通对话 |
| P2 | Agents API | 托管长任务会话、状态和恢复，可选择自托管执行环境 | 新增可选执行后端，保留现有本地路径 |
| P2 | Sign in with ChatGPT 的计划额度授权 | 为开源本地应用提供自己的用户登录与授权流程 | 当前为预览；与 API 计费路线分别设计 |

P0 的顺序取决于宿主：目标客户端支持 Events 时，先做端到端通知验证；不支持时，先完成 Bridge 内的会话查询。

## 1. MCP Events：让后台结果回到 GPT

官方已提供事件订阅与 Webhook 投递接入。当前适用范围是 ChatGPT 网页的 Work 对话、桌面端选择 Cloud 的 Work 对话，以及 dots；要求 MCP 2.0 `2026-07-28`。这不是普通 MCP SSE 进度通知，也不能据此承诺所有 GPT 对话或 Codex CLI 都能被唤醒。官方说明见 [MCP Events](https://developers.openai.com/plugins/build/mcp-events)。

**项目现状：** `internal/mcpserver/dev_bridge_ui.html` 依赖读取投影、进程内 wakeup claim 和 `ui/message` 尝试发送继续消息；它依赖卡片及宿主交互生命周期。`internal/mcpserver/server.go` 当前声明 Tools、Resources，未实现业务事件订阅。Go MCP SDK v1.7.0 已包含 `2026-07-28` 协议基础，因此不能简单归因于 SDK 版本过旧。

**建议设计（项目方案，不是 OpenAI 内置事件名）：**

- `review.ready_to_read`：AI 文件审查授权已就绪，供明确订阅该动作的对话继续读取。
- `review.completed`：正式报告已落库，提醒查看报告或准备后续确认。
- `runtime.input_required`：准确的运行请求等待用户回答。
- `operation.failed`、`task.acceptance_required`：需要处理失败或验收结果。

实现时让 Core 事务保存业务变化及待投递事件，再由独立投递器发送；订阅按用户、Project、Task 过滤。保存签名密钥、订阅有效期、稳定事件 ID 与投递进度，重启后继续。通知只携带对象引用和必要摘要，收到后通过现有工具回读事实。

Webhook 收件确认不能当作任务完成或用户批准。首个验证场景应是：用户明确订阅一个 Task；关闭卡片；模拟报告完成；确认目标 Work 对话收到事件；重复投递不重复推进；取消订阅后停止。**Tunnel 能否承载该组合需要实际验证，本次未验证。**

## 2. 会话可见性与历史读取：近期收益最确定

官方 App Server 文档提供会话列表、分页历史与运行中输入等接口，且会话列表默认来源过滤可能排除 `appServer`。新版持久化会话采用分页历史；应检查继续使用完整历史读取的兼容性。参见 [Codex App Server](https://learn.chatgpt.com/docs/app-server)。

**本机 Schema 核对结果：** `thread/list`、`thread/read`、`thread/turns/list`、`thread/items/list`、`thread/name/set`、`turn/steer`、`review/start` 均存在；这只能证明协议声明，不能代替真实运行测试。

**项目现状：** 已通过 `thread/start`、`thread/resume` 和 `thread/read` 管理执行；`internal/agentruntime/service.go` 已处理计划、消息、命令输出和最后活动时间，Web Console 已有 Run 会话 SSE。此次源码检索未发现对会话列表及历史分页方法的调用。`internal/codex/continuity.go` 仍有 `includeTurns: true` 的读取路径。连续性版本校验已经包含 0.149.0 与 0.159.2，不能将整个项目描述为只支持旧版本。

**建议：**

1. 建立准确的 Project / FeatureArea / Task / Run / Thread 映射，绑定实际 Codex 数据目录及后端身份；只展示当前用户有权访问的会话。
2. 列表显式包含需要的 `appServer` 来源，处理归档与 worktree 路径过滤；不用列表结果替换已保存的准确 Thread ID。
3. 增加历史分页读取，失败时保留原执行身份；取消业务任务后仍可读取历史。
4. 区分“读取历史”和“重新订阅或继续运行”；进入详情页不应触发恢复执行。

这些措施改善 Bridge 自己的会话界面。它们**不保证官方 Desktop/CLI 的默认列表自动显示 Bridge 会话**。AI 文件审查也仍然没有 Codex Thread，不能为它补造一个会话。

## 3. Plugin Extensions：从聊天卡片扩展到任务面板

官方文档列出侧栏应用、会话旁面板、文件查看器、深链接、扩展表单等入口；具体客户端与账户有可用性差异，例如 composer mentions 仅限桌面端。参见 [Plugin Extensions](https://developers.openai.com/plugins/build/extensions)。

**建议用途：** 在会话旁展示当前 Project、领域、Task、待确认事项、最近活动及报告，保留卡片作为单次确认入口。领域/任务创建表单可以采用结构化输入，但仍提交现有冻结 intent。界面通过 MCP 工具访问 Core，不假定 ChatGPT 能直接打开用户的本机 Console 地址。

现有确认卡片已经采用 MCP Apps `ui/*` 桥接、`_meta.ui.resourceUri` 与工具可见性控制，这部分方向与官方推荐一致，无需整体重写。参见 [MCP UI 接入](https://developers.openai.com/plugins/build/chatgpt-ui)。

## 4. 运行中补充输入与原生代码审查

本机导出 Schema 中 `turn/steer` 带有 `expectedTurnId`；可以作为“对当前运行补充说明”的候选接口。发送前必须绑定准确的 Run / Thread / Turn；超出获批工作集或改变实施定义的要求仍走停止、修订和重新确认。补充说明的发送状态也应持久化，连接失败不能盲目重发。

`review/start` 的目标包含工作区变更、基准分支、指定提交和自定义指令，适合评估“实施后代码差异审查”。项目现有 Review 同时承担需求澄清、问题发现与实施提案等业务合同，不能直接用原生代码审查输出替代。若接入，需要保留正式结构化报告的校验与落库步骤。

CLI 文档还有连接 App Server 的远程终端入口，但 WebSocket 传输明确标为实验性。项目当前使用受控的文件化 stdio 与恢复绑定，研究阶段不建议为获得 CLI 界面就替换传输；尤其不能让两个客户端无协调地回答同一个审批请求。

## 5. Agents API：可选的长任务后端

官方于 **2026-09-10 发布公开 Beta**。它提供 OpenAI 托管的 Codex harness、持久化会话和进度接口；模型使用按 API 计费。它与本地 `codex app-server` 是不同集成路线。参见 [发布记录](https://developers.openai.com/api/docs/changelog)、[Agents API](https://developers.openai.com/api/docs/guides/agents-api/overview)。

项目可将它作为一个可选 Agent 后端，保存独立的 Provider / Session / Turn 身份，继续由 Core 决定审查、授权、验收及 Git 交付。先试只读审查或独立副本，不把云端结果直接套用到本机工作树。

官方还支持在自有环境运行 `codex exec-server`，由执行器向服务端发起出站连接。这保留自有文件环境，但 agent orchestration 由云端管理；不是现有 App Server 的简单替换。参见 [Self-hosted sandboxes](https://developers.openai.com/api/docs/guides/agents-api/environments/self-hosted)。

会话 Webhook 可报告等待动作、开始处理、空闲、失败；空闲不等于上个 Turn 成功。断流恢复需要读取持久化记录，不能只依赖实时事件；平台的会话也不会因此成为官方 Desktop/CLI 中的普通本地会话。参见 [Session webhooks](https://developers.openai.com/api/docs/guides/agents-api/sessions/webhooks)、[运行和继续会话](https://developers.openai.com/api/docs/guides/agents-api/sessions)。

## 6. 开源本地应用的登录与额度授权

Sign in with ChatGPT 已文档化开源、本地应用申请用户计划额度授权的流程。Dev Bridge 的开源本地定位值得评估该路线，以应用自己的 OAuth 注册、主机身份和凭证存储提供登录，而不是复制其他客户端的认证文件。该授权不开放用户既有 ChatGPT 对话。参见 [ChatGPT plan usage](https://developers.openai.com/siwc/token-sharing-open-source)。

当前预览要求流式、非持久化的 Responses 请求，并不支持 `background` 等部分参数；本地 App Server 历史仍可单独保存。不能把计划额度授权、普通 API key、Agents API 和 ChatGPT 对话存储当成同一服务。参见 [Preview limitations](https://developers.openai.com/siwc/token-sharing-open-source/preview-limitations)。

## 7. Workspace Agents API：由 Bridge 发起工作区 agent

官方还提供触发已发布工作区 agent 的接口：可用稳定的 `conversation_key` 延续对应 agent 对话，触发后获得 `conversation_url`；Beta 可轮询运行状态，但当前不能通过该 API 读取 agent 回答正文。它不是向任意既有 ChatGPT 对话发送消息的接口。参见 [Trigger workspace agent runs](https://developers.openai.com/workspace-agents/trigger-runs)。

这条路线适合配置一个专用审查 agent，由 Bridge 在用户授权后触发，让 agent 通过既有 MCP 工具提交正式报告。它与 MCP Events 是两种触发方式，应选择一个触发所有者并去重，避免同一审查并发启动两次。工作区管理员须开启相应能力及个人访问令牌创建，调用使用专用 Workspace Agent access token。参见 [Workspace Agent authentication](https://developers.openai.com/workspace-agents/authentication)。

## 8. 其他能力与不必重复建设的部分

| 能力 | 适用判断 |
| --- | --- |
| Responses Background + Webhooks | 可用于新增 API 驱动的报告生成；不是给当前 ChatGPT 文件审查加一个后台开关。参见 [Background](https://developers.openai.com/api/docs/guides/background)、[Webhooks](https://developers.openai.com/api/docs/guides/webhooks)。 |
| Async tool calling | 模型可在工具执行期间继续处理独立工作，后台工具仍由应用执行。可在未来自有 Responses agent 中评估，不能假定现有 ChatGPT MCP 工具自动支持。参见 [Async tool calling](https://developers.openai.com/api/docs/guides/async-tool-calling)。 |
| Secure MCP Tunnel | 项目已经采用的接入方向；维持通道健康、权限与工作区绑定即可，不用重新造公网代理。Tunnel 接通不等于事件订阅已生效。参见 [Secure MCP Tunnel](https://developers.openai.com/api/docs/guides/secure-mcp-tunnels)。 |
| 更快模型、多 agent、语音 | 可按具体负载评估，但它们不能直接修复“审查根本未继续”“结果未送回对话”或“会话被列表过滤”。当前不作为流程修复的前置依赖。 |

## 建议的首轮交付边界

先交付 **一个 Task 的事件通知验证 + Bridge 会话索引与分页历史**：确认用户订阅、准确对象身份、重启恢复、取消后历史、重复投递，以及不支持宿主的明确提示。成功标准是“用户能找到正在执行什么、需要做什么、结束后在哪里看结果”，不是“消息发送 API 返回成功”。

本轮调研没有真实验证账户权限、Events 经 Tunnel 的兼容性、Extensions 在目标宿主的展示，或 Agents API 的模型执行。应在选择路线后逐项验证，再写入产品支持承诺。
