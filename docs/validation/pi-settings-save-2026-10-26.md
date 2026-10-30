# Pi 配置运行中保存修复（2026-10-26）

问题：清空 Pi 模型后重新添加第一个模型改变了 Core 的启动开关，SaveSettings 将其与基础设施配置一并拒绝；前端添加按钮未体现此约束。

修复：Pi 运行配置可在托管服务运行时持久化，当前子进程继续使用原启动配置。Status 根据实际启动配置与已保存配置投影 piRestartRequired，Pi 设置展示待应用提示和“应用并重启”按钮。默认模型和推理级别允许运行中编辑。显式 Restart 或 Stop 后 Start 会重建启动配置。基础设施字段继续要求先停止服务，外部 Core 的 Pi 配置不由 Desktop 接管。

验证：

- Desktop 全部 Go 测试通过；前端 80 项测试通过；Wails 生产构建通过。
- 新增回归覆盖：多次保存、首个模型启用、最后一个模型清空、撤销待应用配置、持久化失败、运行中 Start 不应用配置、显式 Restart 和 Stop/Start 应用新参数、外部 Core 拒绝接管。
- 实际 UI 复现用户先前选择的 group/gpt6.1，运行中添加成功并显示“已保存，重启服务后生效”。默认模型仍留空，等待用户选择。
- 本次未点击“应用并重启”；当前运行环境未因模型保存发生切换。
- Core/MCP 保持 .79，三个二进制散列未变；Core/MCP/Local MCP 的 readyz 均为 HTTP 200；Desktop 显示 OpenAI 隧道 ready。
- 除 Pi 模型列表和默认模型外，其余设置散列与清理前一致。

证据位于 build/pi-settings-save-20261026/：desktop-tests.log、frontend-tests.log、desktop-build.log、installed.json、deployment.json、pi-save-success.png。
