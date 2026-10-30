# Pi 模型选择清理验证（2026-10-26）

- 已选模型列表只渲染 enabled 模型；取消选择后隐藏，搜索下拉允许重新启用原模型。
- 支持清空全部 Pi 模型选择，同时清除默认模型；Pi 可执行文件和推理偏好保留。
- 前端测试 79 项通过，Wails 生产构建通过。
- 已更新 Desktop 可执行文件，Core/MCP 保持 .79，二进制散列未变。
- 实际配置从 134 条记录／129 个已选模型清空为 0；默认模型为空。除 piAgentModels 和 piModel 外的设置散列与操作前完全一致。
- 实际 UI 显示“已选模型（0）”及空状态，默认模型不可选。
- 托管服务已恢复：Core、MCP、本地 MCP 和 OpenAI 隧道均 ready；bridge_diagnostics 返回 .79 且 ready=true。

证据：build/pi-clear-selection-20261026/ 下的 frontend-tests.log、desktop-build.log、deployment.json、pi-selection-empty.png。
