# Validation manifest

> 历史验收记录：以下结论仅对应报告当时的版本、环境和范围，不代表当前工作区已重新验收。参见[历史验收索引](../README.md)。 本报告整理前的原文及同期证据保存在[批次归档](../archive/post-orphan-recovery-closure.md)中。

Baseline HEAD: `398a6d2bb71afcef2871a03b605ecca5eb13fdde`; tree: `4b43487af7d55d42dcc2808da488308a068b25f2`; schema: v46; Desktop config: v2.

All final commands exited 0. Go environment: `GOCACHE=/private/tmp/devbridge-post-orphan-go-cache GOPROXY=off GOSUMDB=off`. No module downloads, real Codex or live database use. Main-module full tests and cross-adapter tests used user-authorized local loopback fixtures outside the binding-restricted sandbox. Remaining tests ran with ordinary workspace permissions. Node tests use local test fixtures.

Each command's combined output is in the named file. Top-level and subtest counts must not be added together. The separate Desktop module is reported separately. The initial failed sandbox invocation is diagnostic history only.

**go-tests.log**

```sh
go test -count=1 -v ./internal/storage/sqlite ./internal/workflowprogression ./internal/controlapi ./internal/git ./internal/agentconversation ./internal/executioncancel ./internal/execution ./internal/recovery ./internal/review ./internal/workflowpresentation ./internal/localmcp ./internal/mcpserver ./internal/consoleapi ./internal/bridgehttp ./internal/codexruntime ./internal/codex ./internal/controlplane ./internal/retirement ./internal/directfile ./internal/task ./internal/workspace ./internal/probeexecution
```

**focused-recovery-acceptance.log**

```sh
go test -count=1 -v ./internal/storage/sqlite -run 'TestPostOrphan|TestRuntimeAuthority|TestInitialOrphanRecovery|TestUnresolvedAuthority'
```

**race-tests.log**

```sh
go test -race -count=1 -v ./internal/storage/sqlite -run 'TestPostOrphan|TestRuntimeAuthority|TestInitialOrphanRecovery|TestUnresolvedAuthority|TestUnknownRecoveryRollsBack|TestUnifiedOrphanRollback|TestFinalAcceptanceConcurrentReplay'
```

**migration-tests.log**

```sh
go test -count=1 -v ./internal/storage/sqlite -run 'Migration|TestFinalV42|TestFinalSchema|FreshTaskOwned|TestTaskOwnedSchema|TestReviewDepthV45|TestV42ToV43'
```

**runtime-config-regression.log**

```sh
go test -count=1 -v ./internal/storage/sqlite ./internal/codexruntime ./internal/codex ./internal/execution -run 'RuntimeConfig|FrozenModel|FrozenRuntime|DefaultAllowlist|ModelConfigs|RuntimeModels|AuthoritySnapshots|ParseStrict|ValidateRejectsDisabled|ReviewDepth'
```

**prompt-byte-identity.log**

```sh
go test -count=1 -v ./internal/execution ./internal/review ./internal/probeexecution ./internal/codex -run 'PromptNeutrality|BuildPrompt|RunPromptBuilders|StartTurnSendsOutputSchema'
```

**git-delivery.log**

```sh
go test -count=1 -v ./internal/storage/sqlite ./internal/git -run 'TestPostOrphanGit|TestInitialOrphanRecovery|TestUnresolvedAuthority|TestTaskAutoCommit|TestTaskCommitHistory|TestTwoFeatureAreasSerialize|Finalize|Submit'
```

**acceptance-parity.log**

```sh
go test -count=1 -v ./internal/localmcp -run 'TestFinalAcceptanceCrossAdapterParity|TestTaskOwnedCrossAdapterIntentReviewExecutionAcceptance|TestTaskReviewGateCrossAdapterReceiptAndNoSecondAuthorization|TestAgentOperationRecoveryIsDurableAndEquivalentAcrossAdapters|TestReviewApprovalV2CrossAdapterExecutionWithoutSecondApproval'
```

**desktop-config-regression.log**

```sh
(cwd: desktop) go test -count=1 -v ./internal/settings
```

**node-tests.log**

```sh
node --test internal/mcpserver/*_test.js internal/bridgehttp/*_test.js desktop/frontend/test/*.test.js
```

**go-vet.txt**

```sh
go vet ./internal/storage/sqlite ./internal/workflowprogression ./internal/controlapi ./internal/git ./internal/agentconversation ./internal/executioncancel ./internal/execution ./internal/recovery ./internal/review ./internal/workflowpresentation ./internal/localmcp ./internal/mcpserver ./internal/consoleapi ./internal/bridgehttp ./internal/codexruntime ./internal/codex ./internal/controlplane ./internal/retirement ./internal/directfile ./internal/task ./internal/workspace ./internal/probeexecution
```

Final outputs are held as working-tree evidence; this task did not commit or stage them. See test-summary.json for exact counts and opt-in skips.
