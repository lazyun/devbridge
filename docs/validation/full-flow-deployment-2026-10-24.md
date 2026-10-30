# 全流程修复部署回执

部署完成时间：2026-10-24 01:20（Asia/Shanghai）。

已通过正在运行的 Desktop 停止托管服务，将 Core、GPT MCP、本地 MCP 三个程序从 `.62` 更新为 `0.1.0-dev.full-flow-20261024.63+gd3969cf`，随后由 Desktop 启动。数据库自动迁移至 schema 132，当前面板为 `task-panel-v34` / `2.22.0-task-panel-v34`。

Desktop 显示 Core、MCP、本地 MCP 和 OpenAI 隧道全部就绪。实际连接器 `bridge_diagnostics` 返回 `.63` / ready=true；本机 Core、两套 MCP readiness 及 Console HTTP 均通过。GPT MCP initialize 返回新版本及修订后的工作流指令，resources/read 返回包含准确 completionTaskId 和 REPLAN_REQUIRED 路径的 v34 HTML；本地 MCP initialize 也返回 `.63`。发布时核验了 1,722 个源码文件和三个已安装程序的 SHA256。

部署前无活动 Run。停止前、停止后、启动后 22 张业务表的数量和内容哈希完全一致，原任务、执行、定义和确认状态未改变。数据库 integrity_check=ok。旧 `.62` 三个程序及停止后的一致性数据库备份位于 `build/full-flow-update-20261024-63/before-update/`，数据库权限 0600。回滚 schema 时须先停止服务并同时恢复对应程序与数据库，不能只降级程序。

本次已验证服务实际提供 v34 资源；未刷新原 GPT 对话中已加载的旧组件，也未向其他对话发送消息。部署核验没有提交业务确认或启动任务。

证据：`build/full-flow-update-20261024-63/` 下的 `deployment.json`、`verification.json`、`diagnostics.json`、`binaries.json`、`source-manifest.json` 和三个阶段数据库快照。源码修复与测试见 [修复报告](full-flow-repair-2026-10-24.md)。
