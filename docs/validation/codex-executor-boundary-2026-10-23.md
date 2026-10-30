# Codex 工程执行边界修复

## 触发与结果

原实施 Codex 从继承的 DevBridge Apps 工具说明中发现 aiSessionId/taskId 字段，转而寻找 Core 授权回执，正常结束但没有代码变更。问题在执行与工作流控制边界。修复后 Core 管理身份与准入；Codex 在分配的工作区执行工程任务，通过原生权限请求处理运行限制。

## 实现

- 两类托管 App Server（stdio 与独立服务）均使用子进程级 Apps/插件关闭选项，不改个人 Codex 设置。
- 每次 thread/start 或 thread/resume 按 CWD 解析有效 MCP 配置，逐个禁用服务；不使用不能清空继承配置的空表假设。正式派发前分页核对该 Thread 的实际 MCP 清单，拒绝活动服务、工具、资源和无法确定的结果。
- 独立的运行职责指令明确执行工程任务，不查找工作流回执或向用户索取流程 ID；已批准任务正文不做重写。新方案编写说明要求只交付工程内容，禁止条件授权和身份查询措辞。
- 冷连接要发起新 Turn 时，先读取准确 Thread 的 idle 状态和实际 CWD，再应用隔离。活动 Turn 的观察/重连不重启、不修改原任务。恢复动作在记录新派发尝试前完成隔离检查。
- 新会话名称使用任务标题和工程阶段，不再用内部 operationId/taskId 命名；Core 仍保存精确关联。
- 原生文件/命令工具、精确绑定的工程 dynamic tools、工作区及只读/写入权限上限保留。正常完成仍不代表工程验收，不增加 Codex 判决 JSON。

## 验证

原生 0.159.2 与 0.163.0：配置启用 Apps/插件并包含无效控制服务地址时，真实 Thread 可建立，服务状态 disabled，工具与资源为空，未启动服务。0.159.2 已完成真实工程 smoke：写临时文件、执行内容校验命令（观测退出码 0）、同一 Thread 重新续接。另验证独立 App Server 的真实启动、连接与工具隔离。

单元/协议覆盖有效配置缺失或格式错误、RPC 失败、实际工具仍可用、分页清单、活动旧 Thread 不可重新配置、工程模型/思考等级及工作区不变。原有 prompt 字节/SHA、权限上限、派发不确定性与旧回执守卫继续回归。新增查询已补入旧协议夹具；测试没有跳过准入守卫。

构建与部署材料：`build/full-flow-update-20261023-58/`。部署结果以该目录验证清单与回执为准。此次不提交业务确认、不修改生产业务表、不重试原任务、不发消息到原 GPT 会话。

## 官方依据

[官方配置参考](https://learn.chatgpt.com/docs/config-file/config-reference)：features.apps、features.plugins 与 mcp_servers.<name>.enabled。配置仅应用于 Bridge 托管工程子进程/Thread。

## 部署回执

已通过 Desktop 托管停止/启动部署 `0.1.0-dev.full-flow-20261023.58+gd3969cf`；Core、MCP、Local MCP 与隧道 READY，schema 131，面板仍为 v29。停机前无活动 Run，数据库完整性检查通过。重启前后 22 张业务表哈希一致，原 Task 保持 ACTIVE / ACCEPTANCE_PENDING，当前定义版本 3、原审批 PARTIAL 回执和已记录 Run 均保留。没有触发原任务重试、重新分析或业务确认。新边界在后续正式派发/续接时应用；不抹除旧会话历史。

发现并更新的旧夹具：新增 config/read 与清单查询协议；单仓验收旧夹具错误地读取另一个 Task，且未附当前完成合入方案，现使用准确 Task 和 CompletionPlan，重跑通过。没有放宽 Core 门禁。

最终相关包全量回归：Codex、codexlaunch、execution、MCP、appserver 均通过；controlapi 相关准入与完整交付用例通过（旧单仓夹具修正后单独重验）；相关包 go vet 通过。真实原生工程验收观测到文件变更与命令退出 0，0.159.2 同一会话续接成功，独立服务隔离成功。未运行整仓测试或新增 UI 布局测试。
