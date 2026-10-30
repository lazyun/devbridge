# GPT 审查重启后的授权提示（2026-10-23）

.46 部署核对的业务表及任务状态保持一致，但这不足以证明原文件授权仍有效。Core 每次启动更换运行时 authority epoch，原 GPT 审查授权因此被 fencing；这是已有的权限合同，不是遗失 Codex 结果。上一轮现场验收只检查持久化状态，遗漏该运行时门禁。

.47 为准确的当前 UPPER_LLM Review/Run 归属提供 GPT_REVIEW_AUTHORIZATION_REQUIRED 展示原因，中文说明服务重启及如何处理。任务显示「GPT 审查待重新授权」，WebUI 的原 RECOVER 控件显示「重新授权 GPT 审查」；原恢复请求、observed expectedUpperVersion、权限边界和请求幂等合同不变。重新授权只处理原审查的授权版本，GPT 仍需在原对话按正式入口准备文件读取并提交报告。

运行列表和会话详情均从准确 Review、Run 的 epoch 及当前 Core authority 得到同一只读授权提示，优先于等待读取/提交报告。终结报告和终态运行不被改成待重新授权。未知执行者仍不推断为 GPT。任务的重新授权展示不生成 RECOVER_RUN_RESULT 下一步，不声称遗失 Codex 输出；普通实施和验证的原结果恢复行为保留。

本次不自动重新授权，不重建任务、不准备/启动/恢复业务操作，不提交报告、不改变订阅，也不直接修改生产业务表。沿用用户此前修复后可直接重启的授权部署 .47，页面按钮由用户本人决定是否操作。

验证及部署结果见 build/full-flow-update-20261023-47/validation-results.json 和 deployment.json。

## 验证结果与正式部署

Node 468 项：464 通过、4 跳过；四个完整 Go 包及 controlapi 真实冷重启/正式恢复聚焦测试通过，go vet 及三个程序构建通过。MCP 测试初次受沙盒临时端口限制，获得回环测试权限后完整通过。

已通过 Desktop 托管停止/启动部署 .47。Core、MCP、本地 MCP 及隧道就绪，MCP 诊断确认版本 .47，schema 131。真实任务列表、详情和运行会话均只读验收为待重新授权，按钮可用且未点击。重启前到最终页面检查后 13 张业务表保持一致，单独确认 authority epoch 已轮换：业务表一致不能替代运行时授权有效性校验。原 Task、Review、Run 保留，未授权文件、恢复操作或提交审查报告。
