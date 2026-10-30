# BRIDGE_PROMPT_NEUTRALITY_CLOSURE

1. **Starting HEAD**：`21da1ea14518fe73bb7c2b46f6f5afc87bcb7b87`。起始工作区干净。
2. **Task-owned v44 baseline**：已 fresh 确认 Task → Review [0:N]、Review 1:1 Run、current_review_id、latest-only Approval、Execution purpose uniqueness、immutable TaskDefinitionRevision、Acceptance、FeatureArea Workspace / AgentConversation ownership。关系、phase 和生命周期均未修改。v44 SQL 唯一改动是 Review instruction 上限由 16,384 字符统一为 API 已声明的 65,536 UTF-8 bytes；没有升版本或操作既有数据库。
3. **Production prompt paths**：见下面逐路径审计矩阵。覆盖 Task Review、Implementation、Verification、Context Probe、Scope Review / expansion、recovery / continuation、Codex resume / follow-up。
4. **删除注入**：删除 Review 的 ImplementationResultInstruction 定义和拼接，删除 scopeConflictInstruction 定义和拼接；不再添加 output-format、read-only、生成 proposal、scope 或 architecture 行为 prose。
5. **删除 fallback**：删除 BuildPromptWithInstruction 的 CodexInstruction → Goal → WorkSpec.CodexInstruction 选择；删除 Coordinator 的 Goal fallback；删除 Scope Review admission/start 的 Instruction → EngineeringGoal fallback。旧单 Run handoff 显式传 authored instruction，空值拒绝，不再依赖运行时补选。
6. **Review exact contract**：task_review_start 必须显式提供 reviewInstruction；入口、domain admission 和 runtime 校验；Review / Run instruction 必须相同。SQLite RecordPrompt 拒绝任何与 stored instruction 不同的字节。
7. **Implementation exact contract**：执行绑定的 immutable TaskDefinitionRevision.WorkSpec.CodexInstruction 是 authoritative instruction；handoff 不同则 CONTRACT_INCOMPLETE，发送 prompt 不含 metadata 或 suffix。
8. **Verification exact contract**：使用同一 authoritative revision 的 VerificationSpec.CodexInstruction；核对 frozen spec 和 instruction，checks / criteria / scope 不写入 prompt。
9. **Context Probe contract**：保持 caller instruction 原样存储、发送及审计；空 instruction 拒绝。新增完整生命周期字节/SHA 回归。
10. **Structured schema**：通过 Codex turn/start.outputSchema 传递。Review schema 允许 workSpec / verificationSpec 为 null，只返回 findings；原 latest-only Approval 仍独立校验完整 proposal。Verification v2 schema 保留。无 structured-output capability 的 backend 在 turn 前 BACKEND_CAPABILITY_INSUFFICIENT；不生成 prose fallback。
11. **Scope conflict**：Implementation 新增最小机器结果 envelope `{resultDisposition: COMPLETED | SCOPE_CONFLICT, summary: string}`。严格 JSON 解析；旧 magic prefix 不再作为控制信号。summary 内包含 SCOPE_CONFLICT: 不改变 disposition。结果 envelope 不改变 Task-owned ownership / phase / acceptance contract。
12. **Authorization**：Review / Verification 的 read-only 由 operation authorization ceiling、每次 turn/start 的 readOnly sandboxPolicy、permission broker / capability handler enforce；resume 重新绑定当前 operation ceiling。测试 instruction 无需含“do not modify”。沿用 filesystem、repository/workspace、Git 和 runtime 边界；不从自然语言推断权限。
13. **Audit invariant**：authored bytes == final_codex_prompt == StartTurn text bytes；SHA256 和 prompt_bytes 一致。保留 prompt_sent_at。它仍是发送前持久化时间，实际 turn acceptance / response loss 由原 conversation protocol 单独记录。
14. **Byte / SHA regression**：分别覆盖 leading whitespace、trailing whitespace、CR/LF、Unicode / combining character / emoji、symbols、约 63 KiB instruction；包括 durable Review、immutable revision 的 Implementation / Verification、Probe lifecycle、真实 JSON-RPC serializer（内存模拟对端）、resume、空值拒绝、schema capability 拒绝、revision substitution 拒绝和机械 read-only。
15. **Static injection search**：production 中 ImplementationResultInstruction、scopeConflictInstruction、SCOPE_CONFLICT: 和 prompt += 均零命中。其余赋值/调用沿数据流逐一归类；见审计矩阵及 [原始搜索](../validation/archive/prompt-neutrality.md#file-bc6f39af21695933)。
16. **Modified files**：见 [完整清单](../validation/archive/prompt-neutrality.md#file-85ddf6a192430e9a)。production 改动集中于 execution、review、scopereview、executionproposal、task validation、controlapi error mapping、MCP admission 和 Review storage；其余为回归夹具、测试与文档。
17. **Go tests**：12 个受影响包全部通过，focused tests 通过。结果记录见 [包回归](../validation/archive/prompt-neutrality.md#file-0b80a05f2d41adc4) 和 [字节一致性 focused](../validation/archive/prompt-neutrality.md#file-ca9c3f2e0107a9b1)。
18. **Race tests**：5 个目标包全部通过。见 [targeted race](../validation/archive/prompt-neutrality.md#file-11dba3ba2100b044)，覆盖 execution cancellation / finalization、Task-owned progression、Review admission / approval 和 prompt lifecycle。
19. **go vet**：全部受影响包通过，exit 0。见 [vet](../validation/archive/prompt-neutrality.md#file-105e2d45340a8e1e)。
20. **git diff --check**：通过，exit 0。见 [final audit](../validation/archive/prompt-neutrality.md#file-9577c8226e5b3296)。
21. **Final git status**：见 [status](../validation/archive/prompt-neutrality.md#file-cadb704656495bc3)；修改保留在当前工作区，未 commit。
22. **SCOPE_CONFLICT**：本任务无阻塞性的架构 SCOPE_CONFLICT；结构化运行时 disposition 保留，magic-string path 已移除。
23. **FAST/DEEP**：未实现。
24. **Retire**：未实施 Retire UI 或其他 Retire 待办。
25. **Production recall host**：仍为 HOST_INTEGRATION_PENDING，未进行 host integration 或 Card 视觉调整。
26. **交付边界**：明确未 push；未 commit、tag、release，未修改其他 worktree，未访问外网、运行真实 Codex 或操作 live DB。测试只使用临时目录、临时 Git/SQLite、模拟 backend；Go 依赖离线，MCP 使用临时 loopback server。

## Fresh audit：A / B / C 分类

| 路径 | A：exact authored input | B：独立机器协议 / metadata | C：处理 |
| --- | --- | --- | --- |
| Task Review | mcpserver.StartTopicReviewCore → controlapi.StartAdmittedReview → scopereview.StartAdmitted → review.AdmitStart → ReviewStore / linked Run → Coordinator.prepareProductReview / BuildPrompt → codex.StartTurn | ReviewPlan、module relevance、repository provenance、model/reasoning、read-only ceiling、outputSchema、审计 | 删除 Review result behavioral suffix；拒绝 EngineeringGoal fallback |
| Scope Review / 后续 Review | scopereview.Start → persisted Review.Instruction → reserved Run → 同一 Coordinator | initial grant、selected modules、authorization scope | 删除 Start fallback；不依据 ordinal / phase 生成指令 |
| Scope expansion | scopeexpansion.Service 只读写授权 scope / decision，不存在独立 prompt composer 或 Coding Agent dispatch；Review 后续仍走上述 exact path | scope grant、decision、relevance metadata | 不添加 scope prose，不生成 expansion instruction |
| Implementation | executionproposal Start / AuthorizeAndStart / ResumeReserved → immutable source → RuntimeSupervisor → PrepareReservedExecution / frozenTask → BuildRunPrompt → StartTurn | workspace、execution scope、model、reasoning、runtime / tools、ImplementationReport schema | 删除 scope suffix；删除 Goal / WorkSpec fallback；拒绝 revision prompt substitution |
| Verification | authoritative VerificationSpec → StartAuthorizedPlan 或 StartAuthorizedVerification → PrepareReservedExecution → BuildVerificationRunPrompt → StartTurn | checks / criteria 的结构化 evidence reconciliation、v2 schema、read-only ceiling | 无 checks/criteria/scope prompt 合成；空值拒绝 |
| Context Probe | API caller CodexInstruction → contextprobe.Create / durable store → probeexecution.Runner / BuildPrompt → client.StartTurn | scope、repo cwd、run ownership、read-only ceiling、审计 | 原 path 已 neutral，保留并加回归 |
| Recovery / continuation | executionproposal.ResumeReserved、RuntimeSupervisor 原授权 handoff；冻结 revision / Review instruction 不变 | lifecycle、epoch、fencing、replay、原 Run / Execution identity | 无 continuation 生成器；显式传 instruction，拒绝空值 |
| Codex resume / follow-up | Coordinator ResumeThread 后 StartTurn；codex.Client 直接将 Prompt 放入唯一 input[0].text | thread identity、cwd、dynamic tools、每 turn sandboxPolicy、schema、model / effort | 无 prepend / append / normalization；JSON 编解码后的 UTF-8 instruction 相同 |

搜索中 MCP tool descriptions / projection summaries、Thread name、error messages、Git assertion evidence matching、workflowtest fixture prose均不流入 Coding Agent instruction。workflowtest 只由测试导入；它不属于 production prompt authoring。运行时 backend tool descriptions 和 JSON schema 属于 B，不拼入 prompt。

## 验证命令

统一环境：`GOCACHE=/private/tmp/devbridge-task-owned-go-cache GOPROXY=off GOSUMDB=off`。

```sh
go test -p 1 ./internal/review ./internal/execution ./internal/probeexecution ./internal/task ./internal/controlapi ./internal/mcpserver ./internal/storage/sqlite ./internal/localmcp ./internal/scopereview ./internal/executionproposal ./internal/codex ./internal/contextprobe -count=1 -timeout=180s
go test -p 1 ./internal/review ./internal/execution ./internal/probeexecution ./internal/scopereview ./internal/codex ./internal/storage/sqlite ./internal/contextprobe ./internal/controlapi ./internal/mcpserver -run 'PromptNeutrality|ByteExact|ByteIdentity|EmptyInstruction|EmptyReviewInstruction|ReviewStorePersistsPrompt|IndependentProbeLifecycle|ReviewSchemaAllows|MissingAuthored' -count=1 -timeout=120s
go test -race -p 1 ./internal/execution ./internal/storage/sqlite ./internal/controlapi ./internal/review ./internal/probeexecution -run 'PromptNeutrality|ByteIdentity|ExecutionCancellation|TaskOwned|ReviewAdmission|ReviewApproval|ReviewStorePersistsPrompt|IndependentProbeLifecycle|Finalization|ResumeReserved|EmptyInstruction|MissingAuthored' -count=1 -timeout=180s
go vet -p 1 ./internal/review ./internal/execution ./internal/probeexecution ./internal/task ./internal/controlapi ./internal/mcpserver ./internal/storage/sqlite ./internal/localmcp ./internal/scopereview ./internal/executionproposal ./internal/codex ./internal/contextprobe
git diff --check
```

早期验证暴露的注入预期、测试 backend schema capability / callback、fixture instruction 不一致和 SQLite 长 instruction 上限均已针对性修复。第一次受沙箱限制的 MCP bind 失败不计为通过；获准临时 loopback 后重新运行。额外探索性 reviewdrift 包存在 DEVELOPMENT_BRANCH_NOT_SELECTED fixture 失败，未作为本任务验证通过项；该包无保留改动。
