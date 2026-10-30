# 原生会话续接的阶段策略修复

状态：2026-10-25 已部署 `0.1.0-dev.full-flow-20261025.71+gc4ae1d3`，面板 v39、确认单 v11、数据库 schema 132。本次只修改宿主发给 Codex 的运行时指令，不改用户批准的工程指令，不修改冻结权限或历史运行结果。

## 原因与准确案例

原任务的 `run_impl_execution_acceptance_0ad8ada9e0ad7986` 具有 `purpose=IMPLEMENTATION` 的冻结 TaskPermissions，授权写入原 Skeleton 工作树。其原生 Thread `01a16836-37ae-7550-a804-0c210df7bb6e` 的压缩历史仍含 `purpose=VERIFICATION` 的旧开发者策略。原生完成状态为 `SUCCEEDED`，工程输出却明确报告 `IMPLEMENTATION_BLOCKED / UNKNOWN`，且没有新增实现；运行结束不等于工程交付。

存在两处相互叠加的问题：

1. `TaskPermissions.Instructions` 为实施也无条件附带“验证副本源码只读”的说明，并未列出准确写入 grants，导致实施角色与高优先级指令冲突。
2. 新 Run 仅通过 thread start/resume 配置指令，没有为新 turn 显式声明当前角色替换历史阶段限制。持久会话与压缩历史中的旧开发者消息不能作为当前阶段的授权依据。

只读诊断证据位于 `build/codex-turn-policy-20261025/production-readonly-evidence.json`。没有为修复直接修改业务数据库、原生生产会话文件或旧 Run。

## 修复

实施、验证和只读诊断分别生成运行时策略。实施明确列出冻结 `REPOSITORY / PATHS` grants，其他登记仓库仍只读；验证仍仅允许准确副本产物和 scratch 写入。网络权限仍来自原冻结方案。

每个真实执行器新 turn 通过原生 `collaborationMode.settings.developer_instructions` 发送当前策略，明确替换历史宿主阶段角色，同时固定原模型和推理配置。原工程指令仍只作为一个 user input 原文发送。缺少冻结模型时在派发前拒绝。`permissions`、`approvalPolicy=never`、工作集及 Run 身份检查不变；此文本不提供或扩大沙箱权限。

原生协议支持依据为仓库固定的 `TurnStartParams.json` 与 `CollaborationMode / Settings` schema，并通过已安装原生运行时验证。已有活动 Turn 的只读重连不重新派发或改写历史指令。

## 验证

日志位于 `build/codex-turn-policy-20261025/`：

| 检查 | 结果 |
| --- | --- |
| 完整 Codex 模块测试和运行权限模块编译 | PASS |
| 完整 execution、probeexecution、run 模块测试 | PASS，70.874s / 0.966s / 1.385s |
| Core 中断续接正式确认及验证恢复定向回归 | PASS，6.240s |
| 受影响模块 go vet | PASS |
| 原生压缩会话同 Thread 的验证 → 实施 → 验证 | PASS |
| `.70` 原代码执行同一原生回归 | 按预期失败：实施仍含验证只读说明，没有当前轮次显式替换策略 |

原生回归使用隔离 CODEX_HOME、本机 Responses 夹具和固定返回文本，捕获 Codex 实际发送的开发者上下文；在隔离 rollout 中插入格式正确的旧验证压缩历史。它没有让真实模型执行工程任务，也未使用生产 Task 或正常账户凭据。通过原生 `command/exec` 实测：验证阶段写源码被拒绝；实施阶段只有准确 PATHS grant 可写，范围外拒绝；切回验证后原可写路径也被拒绝。三个阶段保留同一 Thread，且当前 scratch 身份正确。

这证明原生指令渠道、历史覆盖和实际沙箱切换有效；不将夹具返回文本当作付费模型的工程实施成功。旧业务 Run 的工程阻塞事实保留，后续工程执行仍使用 Core 当前正式资格与冻结方案。

## 部署核对

`build/full-flow-update-20261025-71/` 保留准确源码清单、构建哈希、候选启动检查和部署记录。构建基于 `.70` 的准确源码，仅叠加本次六个实现及测试文件，排除其他工作区改动。对该隔离源码的 Codex、execution 和 probeexecution 模块测试也通过。

部署前无活动 Run 或领域占用，Desktop 停止托管服务后核对 SQLite 完整性，再替换三个二进制并启动。Core、GPT MCP、本地 MCP 均就绪，两个 MCP 初始化版本为 `.71`，OpenAI 隧道就绪；schema 保持 132，没有创建回滚备份。

重启前后 32 张业务表的行数和内容哈希一致。原 Task 仍为 `ACCEPTANCE_PENDING`，绑定 `execution_acceptance_0ad8ada9e0ad7986` 和原第 10 版定义；原工程阻塞输出哈希不变。没有自动重跑生产工程任务，后续新轮次才应用新策略。
