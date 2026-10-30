# Pi Agent 适配边界

## 当前状态

`internal/piagent` 是 Pi JSONL RPC 的**临时会话适配器**，不是已通过 Core
运行平台资格认证的执行端。`Factory.AgentDescriptor()` 声明 `turn_started` 和
`interrupt`：前者表示准确的 prompt 请求已被接受，后者表示可向准确活动 turn
发送中断请求；都不表示任务成功、结果获接受或权限获批准。

适配器提供两条明确区分的入口：

- 旧 `StartForOperation` 不接受原生权限上限。当前 Pi 无法实施
  `TASK_SANDBOX_V1` 的原生文件系统和网络保证，不能把历史冻结的原生任务
  自动降级为“可信即可”，也不能静默切换 Agent。
- 新 `agent.Factory.StartAgent` 只接受明确选择、冻结且校验通过的
  `COOPERATIVE_TRUSTED_V1`、`agentId=pi` policy。该模式依赖受信任 Agent
  遵守范围和报告证据，不要求独立可信执行环境，也不声称有原生隔离。
  显式原生 TaskPermissions、错误运行身份、替代 executable、service tier
  或不同的绑定范围都会在启动前拒绝。

只读 cooperative 操作通过独立系统上下文表达只读角色，而非伪造沙箱保证。
其实际 Core 准入仍须满足新的显式策略选择和持久记录合同；旧记录保持原义。

## 已实现的传输合同

- 启动 `pi --mode rpc --no-session`；`StartThread` 用 `get_state` 获取实际
  `sessionId`，不用工作目录伪造线程身份。读取状态不调用模型。
- 同一进程的 CWD 不可改变；错误线程、关闭后的会话、非空待处理队列、
  正在运行的会话和实际模型/思考等级与冻结选择不一致均失败。
- prompt 原文保持不变，不追加 JSON schema 或其他代理专属合同。
- 已知的工具、资源和诊断 `ExtraArgs` 保持可用；拒绝模式、会话、模型、
  thinking、system prompt 的替换，位置参数 prompt，以及无法区分参数值的未知选项。
- `started` 回执后才调用 started callback。返回的 turn ID 是该会话内的
  **适配器 RPC 请求 ID**，不是 Pi 原生、可恢复的 turn ID。callback 错误不会吞掉。
- `handled`、`queued`、缺少 disposition 和拒绝回执都不能冒充成功运行。
- 等待 `agent_settled`，不把 `agent_end` 当作结束。最终内容来自最后一个
  assistant `message_end`，不是累积的流式片段；自动重试后成功可正常结束。
- `aborted` 和 assistant 的失败状态映射到带准确 thread/turn 身份的
  `agent.TerminalError`；只有传输断连、超时或协议损坏时，不伪造终态证明。
- 事件过载、畸形 JSON、没有最终 assistant outcome 等情况失败关闭。
  未知 prompt 结果不得在原客户端重发或开始新 prompt。
- `Close` 等待进程退出且有时限；被迫终止返回错误，不冒充正常关闭。
- 中断先清空待执行队列，再发送 `abort`；仅接收回执不会伪造终态，必须由
  原 prompt 独立观察到 `agent_settled`。错误线程或 turn 不发送中断。

## 独立 cooperative policy 通道

Core 的 `agentpolicy.Envelope` 包含准确身份、角色、CWD、读写根、网络位、
工程范围、完整系统文本与 SHA-256。`StartAgent` 验证并复制该 envelope，
把系统文本写入宿主传输专用的私有临时文件，通过 Pi 的
`--append-system-prompt` 传入。该文件不是新增的工程可写根。

成功读取实际 Pi session/model/thinking 状态后、任何工程 prompt 前，适配器
调用 `OnPolicyConfigured`，记录 policy hash、实际 executable/argv 和
`LAUNCH_CONFIGURED`。回调失败关闭会话且不派发工程 prompt。每次 turn 前
还会核对临时文件字节；结束时删除该文件。工程 prompt 始终逐字不变。

该回执证明适配器配置了哪些启动输入，不证明模型消费、原生强制执行、
工程完成、用户回答消费或运行平台资格。Pi RPC 没有独立的 system prompt
读取回执，不能把 `get_state` 称为完整策略回读。

Pi 的配置指纹独立于 Codex 配置身份：`AgentDescriptor.ConfigFingerprint`
对实际解析的入口路径、符号链接目标路径、入口文件字节及 Factory 的
模型默认值、思考等级、session directory、支持的额外参数进行确定性摘要。
不存在、不可读或无效的入口不提供指纹。新请求、已冻结 policy 与启动回执
必须携带相同的 `agentConfigFingerprint`；启动前、启动后、记录回调后及
工程 prompt 前均复核。参数、配置或入口字节变化不能被旧确认静默接受。
这不是整个运行平台的资格证明：解释器、导入包和继承的账号配置不在该摘要内。

## 明确不支持

| 能力 | 状态 |
|---|---|
| 跨进程 resume、同一 turn recovery | 不支持；`--no-session` 不持久化 |
| 原生结构化输出 | 不支持；不再通过修改冻结 prompt 假装支持 |
| 原生文件系统根 / 网络 / 只读权限上限 | 不支持；cooperative policy 是遵守约定，不是沙箱 |
| Core host tools、审批回执 | 不支持 |
| 持久化中断恢复 | 不支持；原始 `abort` 回执本身不证明进程停止 |
| 可靠 usage / command evidence | 不支持；缺失保持未知，不能填零当作测量 |
| service tier、verified executable、平台资格认证 | 不支持 |

因此会话不实现对应可选接口，也不提供假成功的 naming、resume 或 schema 方法。
Pi 自身可能具有其他功能，但未经本适配器实现和验证就不会声明为 Core 能力。
用户的 Pi 配置、扩展和 MCP 可拥有额外权限；此适配器不提供隔离保证。

## 验证

```sh
go test ./internal/piagent
go test -race ./internal/piagent
```

默认测试使用当前测试二进制构造的本地 RPC 子进程，不访问模型、账号或生产数据。
覆盖身份/CWD/配置约束、prompt 字节保持、快速完成、重试、提供商失败、abort、
无运行的回执、传输中断、事件过载、callback 失败和有界关闭；新增独立 policy
通道、准确启动回执、拒绝原生策略降级、策略文件篡改及清理测试。

已有真实安装探针需要显式 `DEVBRIDGE_PI_REAL_TEST=1`；真实 prompt 测试需要
另行显式 `DEVBRIDGE_PI_MODEL_TEST=1`，默认绝不因为 PATH 中存在 Pi 就调用模型。
本次环境没有安装 Pi，实际 Pi 版本、账号认证和真实模型往返均未验收。

## 上游合同

依据当前 [RPC 生命周期](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc.md)、
[RPC 命令](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/rpc-commands.md)、
[事件格式](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/json.md)
和 [CLI 会话语义](https://github.com/earendil-works/pi/blob/main/packages/coding-agent/docs/cli.md)。
这些是接口参考，不代替对具体安装版本及执行平台的资格验证。
