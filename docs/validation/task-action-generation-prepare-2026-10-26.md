# prepare 代际字段路径与旧工具定义诊断

日期：2026-10-26（Asia/Shanghai）。

用户报告：`Additional properties are not allowed ('taskActionGeneration' was unexpected)`。

## 生产只读核对

对本机生产 MCP 执行 initialize、tools/list，未调用真实任务的 prepare。生产版本为 `0.1.0-dev.full-flow-20261026.81+gc4ae1d3`。`execution_prepare.inputSchema.properties.taskActionGeneration` 已为 `integer / minimum=1`，位于参数顶层。`execution` 子对象不包含该字段，且保持闭合校验。

因此，嵌套 `execution.taskActionGeneration` 会被拒绝；当前生产 schema 接受顶层字段。若调用方确实发送顶层字段却仍被拒绝，应检查其缓存的工具定义。仅凭用户提供的错误片段不能确定该次调用的嵌套路径，也不能声称该次错误已通过浏览器复验恢复。

## 本轮源码修改

- 所有 prepare 工具说明及字段描述明确：`taskActionGeneration` 与 `aiSessionId`、`requestId`、`purpose`、`action` 同级，不能放入 `execution`、`review`、其他业务子对象或变体。
- 工作台审查决定、验证后决定和后续提案消息明确相同路径；顶层仍被旧 schema 拒绝时停止并报告工具定义问题，不删除代际、不更换请求身份绕过。
- 不修改 Core 的代际准入、幂等、审批或执行规则。

## 验证

- `go test -p 1 ./internal/mcpserver -run 'TestExecutionPrepare' -count=1`：PASS。包含顶层字段接受、嵌套字段拒绝，以及零、负数、小数、字符串拒绝。
- `go test -p 1 ./internal/controlapi -run 'TestExecutionPrepareWire' -count=1`：PASS。真实测试 MCP/SQLite/Core 验证旧代际返回 `TASK_ACTION_STALE` 且不增加确认意图；当前代际保存在 `PENDING` 内容中，Task 仍待审批且没有当前 Execution。
- `node --test internal/mcpserver/dev_bridge_panel_continuation_test.js`：20/20 PASS。
- 修改文件 `git diff --check`：PASS。

本轮未发送新 GPT 消息，未准备或消费生产任务确认，未启动或恢复工程运行。

## 部署结果

经用户明确要求部署，已通过 Desktop 正常停止托管服务、替换三个服务二进制并重启。当前版本为 `0.1.0-dev.full-flow-20261026.82+gc4ae1d3`，工作台 `v46`、确认单 `v14`，数据库版本仍为 135。保留 v45 等旧入口及 widgetState 恢复兼容。

部署前完整 MCP Go 回归通过，完整 UI 447 项中 443 通过、4 跳过、0 失败；隔离候选服务检查通过，未调用模型。部署后健康接口、两端 MCP 版本、v46 资源及顶层代际字段说明、安装二进制哈希均验证通过；远程 bridge_diagnostics 返回 `.82 / ready=true`。

部署前确认无活动 Run 或写入 claim；停止及重启后 32 张业务表计数和内容哈希均与本次部署前一致。没有更改设置、Session、任务、确认或运行历史。证据位于 `build/task-action-generation-deploy-20261026/`。

未向原 GPT 对话发送加载消息，也没有声称其旧工具定义缓存或此前的 Cloudflare 访问阻断已经恢复。
