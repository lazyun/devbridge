# 原生命令审批闭环修复

版本 .62 / task-panel-v33，schema 131。包含待部署 .61 的面板导航修复。

## 实际原因

原任务运行在 2026-10-23 21:04（北京时间）提出 item/commandExecution/requestApproval。它作为 RUNTIME_APPROVAL RuntimeRequest 持久化，没有 legacy approvals 表实体。运行详情为 WAITING_FOR_COMMAND_APPROVAL，但总下一步仍是 WAIT_FOR_IMPLEMENTATION。统一面板仅支持 legacy ApprovalID 路径，WebUI 工作会话页仅把 WAITING_FOR_USER 语义请求作为当前操作展示，因此原生 PENDING 权限请求没有可提交入口。

## 修改

MCP runtime_request_respond 的 RUNTIME_APPROVAL 支持互斥的 legacy Approval 实体与 native resolution 合同；原生请求先按准确 Session 读回，再逐项核对完整 runtime identity、requestId、itemId、runtimeRequestId、请求 payload hash、PENDING 和请求种类，交给既有 Core/runtime 校验响应。原生权限不能走语义 UserDecision。运行时原 owner 对带准确原生请求身份的提交增加同一 payload/状态检查，保留旧 API 兼容。

统一面板、WebUI 任务详情及运行会话页均展示原始命令、目录、权限和准确选项。允许本次、允许会话、保存命令/网络规则明确区分，最后由用户确认。UI 不默认选择、不在轮询时提交、结果不明不自动重发。历史请求与身份/版本漂移不可提交。

任务下一步从准确当前 Run 的原生 PENDING 请求显示等待命令/文件权限批准；不改变 IMPLEMENTING/VERIFYING 等生命周期。MCP compact workflow 也给出准确下一步。已知在等批准时不再用无活动提示暗示运行卡死。

## 验证

545 条 Node 回归：541 通过，4 条既有跳过。新增面板最终确认、空 projection actions 的 native 入口、requestId=0、内容漂移拒绝、WebUI 明确确认才提交、一次提交和 scope/epoch/类型分离测试。MCP/AgentRuntime/WorkflowPresentation/ConsoleAPI/BridgeHTTP 包全量通过，相关 go vet 通过。真实隔离 Core/Codex 夹具验证命令批准与用户语义问答互不替代。

真实 Chrome 隔离页使用生产组件；显示等待命令批准、命令/cwd、允许本次和保存规则，以及最终确认。隔离页拒绝所有正式写入。截图见 build/full-flow-update-20261023-62/native-approval-confirmation-isolated.png。

## 生效安排

生产服务仍为 .60，原实施保留。用户先后明确批准两次准确的单次命令，通过当前 Core 已有的原生审批 API 各提交一次 accept，均读回 CONFIRMED_RESOLVED / TRANSPORT_WRITTEN。第一次 SQLite 测试因 err 未定义编译失败；Codex 修正后第二次 SQLite 测试通过，Core TestDTOStateRoundTrip 失败。Codex 继续修改并提出第三次请求，该请求待用户确认，没有自动批准。授权、提交及回读证据保留在 .62/ 与 second-native-approval/，汇总见 approval-readback.json。不保存规则，不将权限批准等同于测试通过。

新版生效需要托管重启，等原运行结束并确认全部 Run 无活动后安全部署 .62。部署前逐项核对所有源码与三个产物 SHA256，不部署已被取代的 .61。后续跟进已更新为 .62 / v33，并要求主动识别全新待审批请求，避免将等待批准误报为仍在执行。

第三次请求随后由用户明确批准，并授权本次原运行后续仅在 internal/core/*.go、internal/adapters/sqlite/*.go 进行 gofmt 和重复完全相同的两包 go test；其他命令仍需确认，不保存全局规则。第三次确认已送达，两个包测试均退出 0，运行恢复 RUNNING / ACTIVE，无待审批请求。证据 third-approval-readback.json、third-native-approval/ 及 scoped-test-authorization.json。完整实施尚在进行，不能据此标记任务完成。

第四次格式化和同一两包重验请求严格匹配上述授权范围，单次批准已送达；运行继续，go vet 与 git diff --check 均读回退出 0。该范围授权不扩展为其他命令的自动审批。证据 fourth-native-approval/ 和 fourth-approval-readback.json。尚无服务重启或程序替换。

2026-10-23 23:37（北京时间）更新：原实施及后续验证均结束，所有 Run 无活动后已托管部署 .62 / v33。Core、MCP、本地 MCP、隧道均 READY，MCP 真实诊断回报 .62，schema131。数据库 integrity_check 与回滚备份检查均为 ok，22 张业务表前后相同。停止后残留 WAL 原样保留，通过只读连接和 SQLite backup API 获取含 WAL 的完整备份，未删除 WAL 或直接修改生产业务表。当前准确 Task 仍为 VERIFYING，下一步为 GPT 分析验证结果；未代交分析或执行任务验收。

原 ChatGPT 会话真实宿主已载入 v33，展示原 Task 的“待 GPT 分析验证结果”及对应入口。后台刷新保留结果标签与展开导航，手动领域/任务切换无旧刷新错误；未点击业务确认。当前没有待批准的原生请求，因此生产只验证展示与读取，正式提交路径由隔离夹具覆盖，没有制造生产审批来验收。宿主验证保留一个领域概览标题残留问题，详见 host-acceptance.json；这不改变业务身份或操作范围。
