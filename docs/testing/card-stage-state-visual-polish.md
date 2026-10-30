> Historical validation record. Its checkpoint and screenshots are not the current architecture or interface contract; see [final v0.1](../architecture/task-execution-v0.1.md).

# DEV_BRIDGE_CARD_STAGE_AND_STATE_VISUAL_POLISH

Current Task/Execution authority and automatic approved Implementation follow the [final v0.1 model](../architecture/task-execution-v0.1.md).

Starting branch: `dev_20260908_1913`; starting HEAD: `cf22a2df592960b5930609a96ed1054bf52b6c9d` (clean).
Implementation branch: `codex/card-stage-state-polish`, isolated worktree. The final commit is the commit containing this report; its full hash and fast-forward result are recorded in the delivery response.
Database schema remains **v41**. The existing presentation envelope version remains `1`.

## Before / after

| Surface | Before | After |
| --- | --- | --- |
| Product Progress | Four phases; unsupported statuses could become `? Run` | Review → Approval → Task → Run; normalized completion, activity, failure, recovery and cancellation |
| Recovery | Repeated raw guidance, generic marker | Localized focal status block; exact capability action or explicit Core restart prerequisite |
| Titles / selectors | Unbounded title height, short bare selectors | Two-line clamping with full accessible text; full-width lightweight selector rows |
| Codex | Small unstructured inline summary | Separate compact plan/current-item, waiting, health and activity summary |
| Actions | Cancel could float to the far right | Cancel near current execution; approval retains primary / secondary / subtle destructive hierarchy |

[Baseline recovery](card-stage-state-visual/before-recovery.png) · [Final recovery](card-stage-state-visual/recovery-required.png) · [320px](card-stage-state-visual/narrow-layout.png)

## Read-only mapping contract

- `productProgress` and `taskStages` are additive client-neutral presentation fields, hashed with the rest of the captured projection. No workflow progression calculation is changed.
- The exact Task's durable ReviewApproval link proves entry through Review / Approval / Task. Without this proof, the client does not invent missing approval facts. Contradictory phase/progress snapshots retain the previous consistent snapshot or show a projection diagnostic.
- Implementation and Verification require the selected Execution's exact Run IDs, purpose, Task, Project and FeatureArea. Older attempts cannot overwrite the stages. Explicit verification waiver remains a waiver, not canceled or verified work.
- Completed Implementation with pending Verification keeps Run entered. Failure, cancellation and recovery are not overwritten by later pending stages.
- Task navigation uses its linked source Review, excluding unrelated newer FeatureArea Reviews. Telemetry and recovery require the selected Task's exact Run. Historical engineering actions and foreign current-workflow diagnostics are hidden; RETIRING / RETIRED remain readable without reopening work.
- Core's existing current-runtime recovery rejection now exposes only a read-only `CORE_RESTART` prerequisite. Eligibility guards, recovery commands and runtime fencing are unchanged.

Current screenshot regression:

```text
✓ Review → ✓ Approval → ✓ Task → ↻ Run
Codex · Agent 状态未知
需要恢复 · [恢复运行] OR 等待 Core 重启
```

The Go regression starts with missing legacy Review progress plus the durable approval link and an infrastructure-failed Implementation. DOM regressions assert all seven markers and the absence of `? Run`.

## Telemetry, consistency and accessibility

Codex plan completion never completes a Task stage. Summary adds only current item type, without item metadata or output. Card does not display PID, launch ID, Thread ID or Turn ID. Waiting takes precedence over stalled presentation; health classification is unchanged. Missing activity timestamps do not fabricate an age.

Project / FeatureArea / selected Task / generation / sourceSequence checks apply before accepting a snapshot. Tests cover late Task responses, mismatched telemetry, invalid stage structure, regressing sequence, contradictory product progress and detached action closures. Visible Feature and Development Task title/status prefer the accepted projection over separately listed rows.

Markers have readable accessible labels; recovery uses a status region; full clamped titles retain `title` and `aria-label`; actions retain labels. Browser keyboard checks exercised Branch Space/Enter expansion and selector focus. Native selectors remain keyboard controls. Branch remains collapsed by default and the header stays compact. No empty Decision panel is rendered.

## Browser acceptance

Production self-contained HTML, isolated HTTP fixture, no Core mutations. Saved screenshots and DOM snapshots:

- [Normal Implementation](card-stage-state-visual/normal-implementation.png)
- [Review Approval](card-stage-state-visual/review-approval.png)
- [Failed Implementation](card-stage-state-visual/failed-implementation.png)
- [Recovery](card-stage-state-visual/recovery-required.png)
- [Core restart prerequisite](card-stage-state-visual/recovery-restart.png)
- [Possibly stalled](card-stage-state-visual/stalled.png)
- [Waiting for permission](card-stage-state-visual/waiting-permission.png)
- [Retired](card-stage-state-visual/retired.png)
- [Long Task, 560px](card-stage-state-visual/long-task.png)
- [Narrow, 320px](card-stage-state-visual/narrow-layout.png)
- [Wide, 850px](card-stage-state-visual/wide-layout.png)

Measured document width equals scroll width at **320 / 560 / 850px**; no horizontal overflow. Mixed Chinese/English and unbroken long titles remain two lines. Main step count remains four, Development stage count three.

[Browser DOM/geometry evidence](card-stage-state-visual/browser-checks.json).
All requested A–O states have DOM coverage, plus runtime approval and unavailable recovery.
**REAL_CHATGPT_HOST_NOT_RUN**: this verifies the production resource in an isolated browser fixture, not the real ChatGPT host.

## Gates

- 139 Card / transport / Details / decision / relevant Web JS tests passed.
- Go tests passed: controlapi, workflowpresentation, agentruntime, mcpserver, task, storage/sqlite. Includes Task.Work, exact recovery/cancellation capabilities and v41 regressions.
- Full affected race passed: controlapi, workflowpresentation, agentruntime, mcpserver, storage/sqlite; final Go presentation changes were rerun through controlapi race.
- `go vet ./...` passed.
- Production Core and GPT MCP builds passed; production ChatGPT UI build passed for both embedded resources.
- Fresh final Core/GPT on isolated data-root/random loopback ports successfully served `resources/read ui://devbridge/card`; bytes and SHA-256 match the source and UI manifest. `schema_migrations` maximum is 41.
- Changed/new Go `gofmt -l` and `git diff --check` clean.

[Gate results](card-stage-state-visual/gates.txt) · [Fresh resource evidence](card-stage-state-visual/resource-read.json).

Final Card SHA-256: `9d3874b1689aacc16a9a2d4f62ad4ab6cf3f52f3c11b332519b70e1bbc595e1d`.

## Whole-diff review

Resolved during review: newer FeatureArea Review masking a historical Task, inherited foreign Run identity, completed-stage handoff looking pending, zero activity timestamp producing an invented age, and foreign guidance leaking into historical diagnostics. No remaining P1/P2 findings identified.

Delivery is a local commit and fast-forward into `dev_20260908_1913`, followed by task worktree/branch/build/cache cleanup. No push, Release, formal 8081 runtime or live database changes.
