# 延续任务确认后的交接修复

原任务 `task_429bc7f6d36b3c3cab1eccf3027a2ad0` 的延续确认 `confirm_933d6d55171ed85ce0972e394be2a453` 已在 Core 完成：`RECORD_TASK_CHOICE` 为 COMPLETED，当前 Task/Execution/定义身份一致，`continuationConfirmed=true`。当时验证证据不完整，Core 明确禁止完成并合入，并允许同任务 REQUEST_CHANGES、REQUEST_REVERIFICATION 和 REQUEST_TASK_REVIEW。

断点发生在面板：`panelAfterConfirmation` 仅为 GPT Review 和 Direct File 处理确认后的交接，没有 CONTINUE_TASK 分支；界面还保留同名「延续任务」按钮，并隐藏了修订实施与重验能力。因此确认成功后 GPT 没有收到后续请求，用户也没有明确的下一步反馈。

面板 v35 增加准确延续回执交接。只有原用户提交返回的 EXECUTION / CONTINUE_TASK / COMPLETED / CONSUMED 回执，且 RECORD_TASK_CHOICE 子步骤与内容中的 Task、Execution、定义完全一致，才登记一次交接。刷新 Core 后还须确认当前身份、延续事实、无暂存及无交付意图。交接请求 GPT 读取原实施与验证证据，按 Core 当前资格准备后续方案；原范围修正优先 REQUEST_CHANGES，定义不变的证据重跑使用 REQUEST_REVERIFICATION，需要工程补审时才使用 REQUEST_TASK_REVIEW。不会由组件消费正式确认或启动 Run。

已确认状态明确显示「已确认延续原任务」及「准备后续方案」，并展示可用的修订与重验入口。消息请求的送达、超时和拒绝有明确反馈。交接在发送前持久化；超时或重载不会自动重发。恢复尚未发送的队列须重新读取准确原回执，不能从历史确认或显示状态推导新授权。准备 requestId 按准确任务/定义/Execution 持久化，重复请求保留同一身份。

既有已确认但没有交接记录的旧组件需要加载 v35，之后使用「准备后续方案」发起准备；无需重复延续确认。

验证日志：`build/continuation-repair-20261024/`。新增回归覆盖实际确认后的自动交接、重复回调、错误/未知/历史回执、任务和定义变化、暂存、消息超时与面板恢复后回执核对。

界面回归 392 项，388 通过、4 项按环境条件跳过；MCP 全包通过（52.113 秒），go vet 通过。首次受限环境测试因不能监听本机临时端口而失败；允许测试端口后重新全包通过。

已通过 Desktop 托管部署为 `0.1.0-dev.full-flow-20261024.64+gd3969cf`，schema 保持 132，面板 v35 / 2.23.0。Core、GPT MCP、本地 MCP 的实际 readiness 和 initialize 版本核验通过，服务提供 v35 HTML。备份及发布证据在 `build/full-flow-update-20261024-64/`；重启前后 22 张业务表完全一致，无活动 Run，数据库完整性为 ok。没有再次消费延续确认或启动业务执行。

原 Chrome 对话实际加载的脚本最初为 v33；重新加载并重试后仍为缓存 v34。已在已安装插件设置执行「刷新工具」，等待结束后未显示错误、未改变权限。

用户随后明确授权向原 GPT 发送一次「重展 v35 并准备原任务后续正式方案」请求。已发送一次，沿用真实 Session/Task；新面板初次加载失败后仅重试只读展示，没有重发消息。原宿主 DOM 中实际脚本为 `2.23.0-task-panel-v35`，显示准确原任务和「已确认延续原任务」。GPT 读取原运行分析后，实际调用 execution_prepare，固定 requestId=`continue_plan_20261024_64_429bc7f6`，生成唯一新确认 `confirm_0f57ee23c41b5a16f54890959207c6d9` / version 1 / REQUEST_CHANGES / PENDING。准确 contentHash 为 `bf10771c49765342a5dd7091a717187d763722b28d6c9c493285c0ace1cfdfd0`，内容保留 7 项验收标准和 8 项必需检查；confirmation_get 与 options_get 读回显示方案可用。

原 v35 面板已显示「修订实施方案 / 待确认」和「查看完整方案」。思考等级由用户选择，确认按钮尚未点击。Task 的阶段、Execution 和定义指针与请求前相同，Run 总数仍为 15，未新增运行或消费确认。真实回执、请求原文、待确认方案及截图在 `build/continuation-repair-20261024/authorized-handoff-{before,after}.json`、`authorized-message.txt`、`prepared-intent.json` 和 `host-v35-prepared.png`。
