"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const I18n = require("./ui/product-i18n.js");
global.DevBridgeStatus = require("./ui/product-status.js");
const Components = require("./ui/product-components.js");
const LogFormat = require("./ui/product-logformat.js");
const LogView = require("./ui/product-logview.js");

function memoryStorage(initial = {}) {
  const values = new Map(Object.entries(initial));
  return {getItem: (key) => values.has(key) ? values.get(key) : null, setItem: (key, value) => values.set(key, String(value)), value: (key) => values.get(key)};
}

test("i18n resolves system, explicit English and Simplified Chinese", () => {
  assert.equal(I18n.createI18n({navigator: {languages: ["zh-Hans-CN"]}}).locale, "zh-CN");
  assert.equal(I18n.createI18n({navigator: {languages: ["fr-FR"]}}).locale, "en");
  assert.equal(I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["zh-CN"]}}).locale, "en");
  assert.equal(I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "zh-CN"}), navigator: {languages: ["en"]}}).locale, "zh-CN");
});

test("i18n falls back, interpolates and exposes missing keys deterministically", () => {
  const i18n = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "zh-CN"}), navigator: {languages: ["en"]}});
  assert.equal(i18n.t("projects.repositories", {count: 3}), "3 个仓库");
  assert.equal(i18n.t("definitely.missing"), "[definitely.missing]");
  delete I18n.messages["zh-CN"]["action.refresh"];
  assert.equal(i18n.t("action.refresh"), "Refresh");
});

test("work kind labels and task filters are localized in English and Simplified Chinese", () => {
  const en = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const zh = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "zh-CN"}), navigator: {languages: ["en"]}});
  for (const kind of ["FEATURE", "BUG_FIX", "REFACTOR", "MAINTENANCE", "TEST_CHANGE", "DOCUMENTATION", "OTHER", "UNCLASSIFIED"]) {
    assert.doesNotMatch(en.t(`workKind.${kind}`), /^\[/);
    assert.doesNotMatch(zh.t(`workKind.${kind}`), /^\[/);
  }
  assert.equal(en.t("tasks.kindFilter"), "Work kind");
  assert.equal(zh.t("tasks.kindFilter"), "工作类型");
  assert.doesNotMatch(en.t("tasks.kindFilterAll"), /^\[/);
  assert.doesNotMatch(zh.t("tasks.kindFilterAll"), /^\[/);
});

test("work kind badge renders only known classifications and keeps unclassified empty", () => {
  const i18n = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  assert.equal(Components.workKindBadge("", i18n.t), "");
  assert.equal(Components.workKindBadge(null, i18n.t), "");
  const html = Components.workKindBadge("BUG_FIX", i18n.t);
  assert.match(html, /work-kind-badge bug_fix/);
  assert.match(html, /Bug fix/);
  assert.doesNotMatch(html, /Unclassified/);
  assert.equal(Components.workKindBadge("UNKNOWN", i18n.t), "");
});

test("language preference is presentation-only local state", () => {
  const storage = memoryStorage();
  const i18n = I18n.createI18n({storage, navigator: {languages: ["en"]}});
  assert.equal(i18n.setLanguage("zh-CN"), "zh-CN");
  assert.equal(storage.value(I18n.STORAGE_KEY), "zh-CN");
  assert.equal(i18n.setLanguage("unsupported"), "en");
  assert.equal(storage.value(I18n.STORAGE_KEY), "system");
});

test("status presentation replaces raw readiness enums with product semantics", () => {
  const i18n = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const presented = global.DevBridgeStatus.presentStatus("project", "NO_GUIDANCE", i18n.t);
  assert.equal(presented.label, "Ready");
  assert.equal(presented.category, "success");
  assert.equal(presented.raw, "NO_GUIDANCE");
  assert.doesNotMatch(presented.label, /NO_GUIDANCE/);
});

test("workspace product states use user-facing lifecycle language", () => {
  const en = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const zh = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "zh-CN"}), navigator: {languages: ["en"]}});
  assert.equal(global.DevBridgeStatus.presentStatus("product", "AWAITING_CONFIRMATION", en.t).label, "Awaiting confirmation");
  assert.equal(global.DevBridgeStatus.presentStatus("product", "PREPARING", en.t).label, "Starting");
  assert.equal(global.DevBridgeStatus.presentStatus("product", "CONFLICT", en.t).label, "Conflict");
  assert.equal(global.DevBridgeStatus.presentStatus("product", "FAILED", zh.t).label, "失败");
  assert.equal(global.DevBridgeStatus.presentStatus("product", "READY_TO_EXECUTE", en.t).label, "Ready to execute");
  assert.match(global.DevBridgeStatus.presentStatus("product", "READY_TO_EXECUTE", en.t).explanation, /Execution authorization/);
  assert.equal(global.DevBridgeStatus.presentStatus("product", "AWAITING_AUTHORIZATION", zh.t).label, "等待授权");
  assert.equal(global.DevBridgeStatus.presentStatus("product", "VERIFYING", zh.t).label, "正在验证");
  assert.doesNotMatch(global.DevBridgeStatus.presentStatus("product", "EXECUTING", en.t).label, /RUNNING|COLLECTING/);
});

test("shared projection guidance states carry product semantics on every surface", () => {
  const en = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const zh = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "zh-CN"}), navigator: {languages: ["en"]}});
  const expectations = {TASK_DELIVERING: "waiting", TASK_RECOVERY_REQUIRED: "warning", REVIEW_MATERIALIZING: "active", FEATURE_AREA_BUSY: "active", GIT_FINALIZATION_PENDING: "waiting"};
  for (const [state, category] of Object.entries(expectations)) {
    for (const i18n of [en, zh]) {
      const shown = global.DevBridgeStatus.presentStatus("product", state, i18n.t);
      assert.equal(shown.category, category, state);
      assert.equal(shown.raw, state, state);
      assert.notEqual(shown.label, i18n.t("status.unknown.label"), state);
      assert.doesNotMatch(shown.label, /^\[/, `${state} label`);
      assert.doesNotMatch(shown.explanation, /^\[/, `${state} explanation`);
    }
  }
  assert.equal(global.DevBridgeStatus.presentStatus("product", "TASK_DELIVERING", en.t).label, "Accepted · awaiting commit");
  assert.equal(global.DevBridgeStatus.presentStatus("product", "TASK_RECOVERY_REQUIRED", zh.t).label, "需要恢复");
  const W = require("./ui/product-workbench.js");
  const rendered = W.renderTasks({C: {...Components}, t: en.t, i18n: en, state: {projectWorkspace: {tasks: [], featureAreas: []}, taskFilters: {}, taskPage: 0}});
  assert.match(rendered, /<option value="DELIVERING"/);
  assert.match(rendered, /<option value="RECOVERY_REQUIRED"/);
});

test("Feature Area product status accepts only the Core two-state projection", () => {
  const en = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  assert.deepEqual(Object.keys(global.DevBridgeStatus.statusMaps.featureAreaProduct), ["ACTIVE", "CLOSED"]);
  for (const internalState of ["DISCOVERING", "ACTIVE", "RETIRING", "RETIRED", "DECOMPOSED"]) {
    assert.equal(global.DevBridgeStatus.statusMaps.product[internalState], undefined, internalState);
  }
  for (const workflowState of ["DISCOVERING", "RETIRED", "FAILED", "REVIEW_RUNNING"]) {
    const presented = global.DevBridgeStatus.presentStatus("featureAreaProduct", workflowState, en.t);
    assert.equal(presented.category, "neutral", workflowState);
    assert.equal(presented.label, "Status unavailable", workflowState);
  }
  assert.equal(global.DevBridgeStatus.presentStatus("featureAreaProduct", "ACTIVE", en.t).label, "Active");
  assert.equal(global.DevBridgeStatus.presentStatus("featureAreaProduct", "CLOSED", en.t).label, "Closed");
  assert.equal(Components.featureAreaProductStatus({status:"DISCOVERING",product:{domainStatus:"ACTIVE"}}), "ACTIVE");
  assert.equal(Components.featureAreaProductStatus({status:"ACTIVE"}), "");
  assert.equal(en.t("topics.lifecycle"), "Feature Area status");
  assert.equal(en.t("topics.statusFilter"), "Feature Area status");
});

test("project workspace navigation and compact authorization copy are localized", () => {
  const en = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const zh = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "zh-CN"}), navigator: {languages: ["en"]}});
  for (const key of ["project.overview", "project.featureAreas", "project.tasks", "project.executions", "project.modules", "project.workspaces"]) {
    assert.doesNotMatch(en.t(key), /^\[/);
    assert.doesNotMatch(zh.t(key), /^\[/);
  }
  assert.match(en.t("projects.selectionNote"), /Console selection only/);
  assert.match(zh.t("projects.selectionNote"), /ChatGPT/);
});

test("Project tab toolbar keeps descriptions and actions without repeating tab headings", () => {
  const html = Components.projectTabToolbar("Useful description", '<button type="button">Filter</button>');
  assert.match(html, /class="project-tab-toolbar"/);
  assert.match(html, /Useful description/);
  assert.match(html, /Filter/);
  assert.doesNotMatch(html, /<h[1-6]/);

  const fs = require("node:fs");
  const source = fs.readFileSync(`${__dirname}/ui/product-app.js`, "utf8");
  const tabs = [
    ["renderFeatureAreas", "renderDocuments", "topics"],
    ["renderDocuments", "renderDocumentRows", "documents"],
    ["renderTasks", "renderExecutions", "tasks"],
    ["renderExecutions", "renderCompactSection", "executions"],
    ["renderModules", "renderWorkspaces", "modules"],
    ["renderWorkspaces", "knownLogObjects", "workspaces"],
  ];
  for (const [name, next, key] of tabs) {
    const body = source.slice(source.indexOf(`function ${name}(`), source.indexOf(`function ${next}(`));
    assert.ok(body.includes(`C.projectTabToolbar(t("${key}.subtitle")`), `${name} keeps its useful description`);
    assert.ok(!body.includes(`t("${key}.title")`), `${name} must not repeat the active tab name`);
  }
  const overview = source.slice(source.indexOf("function renderProjectOverview("), source.indexOf("function renderActiveAgentOperations("));
  assert.doesNotMatch(overview, /overview\.title|project\.overview/);
  const W=require("./ui/product-workbench.js");
  const rendered=W.renderOverview({C:Components,t:key=>key,locale:"en",state:{projectWorkspace:{tasks:[],featureAreas:[]},approvals:[],runs:[]}});
  for(const semanticHeading of ["workbench.needsAttention","workbench.businessDecisions","workbench.runtimeRequests","workbench.blockers","workbench.activeTasks","workbench.delivered"])assert.ok(rendered.includes(semanticHeading),semanticHeading);
  assert.match(overview,/W\.renderOverview\(workbenchContext\(\)\)/);
  assert.doesNotMatch(rendered,/data-task-action|data-execution-cancel|data-close-feature-area/);

  const css = fs.readFileSync(`${__dirname}/ui/product.css`, "utf8");
  assert.match(css, /\.project-tab-toolbar\s*\{[^}]*margin:\s*4px 0 14px/);
  assert.match(css, /@media \(max-width: 760px\)[\s\S]*\.project-tab-toolbar\s*\{[^}]*flex-direction:\s*column/);
});

test("Feature Area cards keep authored content and hide AI-session and planning identifiers", () => {
  const i18n = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const html = Components.topicCard({id: "feature-area-1", title: "用户标题", summary: "Keep this authored content", status: "DISCOVERING", product:{domainStatus:"ACTIVE"}, updated_at: "2026-09-05T01:00:00Z", created_by_ai_session_id: "secret-session", planning_task_id: "secret-task"}, i18n.t, i18n.locale);
  assert.match(html, /用户标题/);
  assert.match(html, /Keep this authored content/);
  assert.match(html, /Active/);
  assert.doesNotMatch(html, /Discovering/);
  assert.doesNotMatch(html, /secret-session|secret-task|created_by_ai_session_id|planning_task_id/);
});

test("Module cards show product facts and omit internal identity and provenance", () => {
  const i18n = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const html = Components.moduleCard({id: "module-secret", provenance: "GUIDANCE", name: "Console", description: "Product UI", facts: [{repository_name: "primary", responsibility: "Presents work", production_roots: ["internal/ui"], entrypoints: ["internal/ui/index.html"], related_areas: ["bridge"]}]}, i18n.t);
  for (const expected of ["Console", "Presents work", "primary", "internal/ui", "internal/ui/index.html", "bridge"]) assert.match(html, new RegExp(expected));
  assert.doesNotMatch(html, /module-secret|GUIDANCE|provenance/);
});

test("known log events are localized while canonical events remain a detail concern", () => {
  const en = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const zh = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "zh-CN"}), navigator: {languages: ["en"]}});
  const entry = {event: "featurearea.review.start.accepted", component: "review"};
  assert.equal(Components.eventSummary(entry, en.t), "Task Review started");
  assert.equal(Components.eventSummary(entry, zh.t), "任务审查已开始");
  assert.equal(Components.eventSummary({event: "custom.event", component: "custom"}, en.t), "Bridge event: custom");
});

test("Review prompt diagnostics are localized in English and Simplified Chinese", () => {
  const en = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const zh = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "zh-CN"}), navigator: {languages: ["en"]}});
  assert.equal(en.t("action.viewPrompt"), "View prompt");
  assert.equal(zh.t("action.viewPrompt"), "查看提示词");
});

test("module rows are collapsed by default and expose only high-value summary", () => {
  const i18n = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const html = Components.moduleCard({name: "review", description: "Review lifecycle and product projection", facts: [{repository_name: "app", responsibility: "Review lifecycle", production_roots: ["internal/review"], entrypoints: ["internal/review/service.go"], related_areas: ["topic"]}]}, i18n.t);
  assert.ok(html.includes('class="module-details" hidden'));
  assert.match(html, /review/);
  assert.match(html, /app/);
  const details = html.slice(html.indexOf('class="module-details"'), html.indexOf("</div>", html.indexOf('class="module-details"')));
  assert.ok(!details.includes("service.go"));
});

test("module facts remain available when expanded", () => {
  const i18n = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const html = Components.moduleCard({name: "console", facts: [{repository_name: "primary", responsibility: "Presents work", production_roots: ["internal/ui"], entrypoints: ["internal/ui/index.html"], related_areas: ["bridge"]}]}, i18n.t);
  const details = html.split("module-details")[1];
  assert.match(details, /internal\/ui/);
  assert.match(details, /internal\/ui\/index\.html/);
  assert.match(details, /bridge/);
});

test("module search matches name, responsibility, repository, root and entrypoint", () => {
  const module = {name: "review", description: "Review lifecycle", facts: [{repository_name: "app", responsibility: "Oversee production", production_roots: ["internal/review"], entrypoints: ["internal/review/service.go"]}]};
  for (const needle of ["review", "Oversee", "app", "internal/review", "service.go", "oversee production"]) {
    assert.equal(Components.moduleMatches(module, needle.toLocaleLowerCase()), true, `should match ${needle}`);
  }
  assert.equal(Components.moduleMatches(module, "docs"), false);
  assert.equal(Components.moduleMatches(module, ""), true);
});

test("project search matches by name and empty needle shows every project", () => {
  assert.equal(Components.projectMatches({name: "Alpha Bridge"}, "alpha"), true);
  assert.equal(Components.projectMatches({name: "Alpha Bridge"}, "beta"), false);
  assert.equal(Components.projectMatches({name: "Alpha Bridge"}, ""), true);
});

test("review usage renders token accounting status, exact zero, and unknown fields distinctly", () => {
  const en = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const html = Components.reviewListUsage({usage: {status: "AVAILABLE", usage: {total_tokens: 42659}, duration_ms: 37000}}, en.t, en.locale);
  assert.match(html, /42\.7k/);
  assert.match(html, /Available/);
  assert.match(html, /Duration/);
  assert.match(Components.reviewListUsage({usage: {status: "AVAILABLE", usage: {input_tokens: 0, output_tokens: 0, total_tokens: 0}}}, en.t, en.locale), /Available · 0/);
  assert.match(Components.reviewListUsage({usage: {status: "PENDING"}}, en.t, en.locale), /Pending · —/);
  assert.match(Components.reviewListUsage({usage: {status: "UNAVAILABLE"}}, en.t, en.locale), /Unavailable · —/);
  assert.match(Components.reviewListUsage({usage: {status: "PENDING", usage: {input_tokens: 9, output_tokens: 1, total_tokens: 10}}}, en.t, en.locale), /Pending · —/);
  assert.match(Components.reviewListUsage({usage: {status: "UNAVAILABLE", usage: {input_tokens: 9, output_tokens: 1, total_tokens: 10}}}, en.t, en.locale), /Unavailable · —/);
  assert.ok(Components.reviewListUsage({usage: {status: "AVAILABLE", usage: {total_tokens: null}}}, en.t, en.locale).includes("Available · —"));
  assert.ok(Components.reviewListUsage({}, en.t, en.locale).includes("usage-muted"));
});

test("review usage breakdown maps all token categories and keeps unknown values as em dash", () => {
  const en = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const facts = Components.buildUsageFacts({status: "AVAILABLE", usage: {input_tokens: 39842, cached_input_tokens: null, output_tokens: 2817, reasoning_output_tokens: 82, total_tokens: null}}, en.t, "en");
  assert.deepEqual(facts, [["Status", "Available"], ["Input", "39,842"], ["Cached Input", "—"], ["Non-cached Input", "—"], ["Output", "2,817"], ["Reasoning Output", "82"], ["Total", "—"]]);
  assert.equal(Components.buildUsageFacts(null, en.t, "en").length, 0);
  assert.deepEqual(Components.buildUsageFacts({status: "PENDING", usage: {input_tokens: 8, output_tokens: 3, total_tokens: 11}}, en.t, "en"), [["Status", "Pending"], ["Input", "—"], ["Cached Input", "—"], ["Non-cached Input", "—"], ["Output", "—"], ["Reasoning Output", "—"], ["Total", "—"]]);
});

test("non-cached input tokens are UI-derived, clamped and unknown when components are missing", () => {
  const en = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const facts = Components.buildUsageFacts({status: "AVAILABLE", usage: {input_tokens: 39842, cached_input_tokens: 12000, output_tokens: 2817, reasoning_output_tokens: null, total_tokens: 52659}}, en.t, "en");
  assert.deepEqual(facts, [["Status", "Available"], ["Input", "39,842"], ["Cached Input", "12,000"], ["Non-cached Input", "27,842"], ["Output", "2,817"], ["Reasoning Output", "—"], ["Total", "52,659"]]);
  assert.equal(Components.nonCachedInputTokens({input_tokens: 8000, cached_input_tokens: 3000}), 5000);
  assert.equal(Components.nonCachedInputTokens({input_tokens: 3000, cached_input_tokens: 8000}), 0);
  assert.equal(Components.nonCachedInputTokens({input_tokens: 8000, cached_input_tokens: null}), null);
  assert.equal(Components.nonCachedInputTokens({input_tokens: null, cached_input_tokens: 3000}), null);
  assert.equal(Components.nonCachedInputTokens(null), null);
});

test("module search and review usage labels are localized", () => {
  const en = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const zh = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "zh-CN"}), navigator: {languages: ["en"]}});
  assert.equal(en.t("modules.search"), "Search Modules");
  assert.equal(zh.t("modules.search"), "搜索模块");
  assert.equal(zh.t("modules.searchEmpty"), "没有匹配此搜索的模块。");
  assert.equal(en.t("usage.input"), "Input");
  assert.equal(en.t("usage.status.PENDING"), "Pending");
  assert.equal(zh.t("usage.status.UNAVAILABLE"), "不可用");
  assert.equal(zh.t("usage.cachedInput"), "缓存输入");
  assert.equal(en.t("usage.reasoningOutput"), "Reasoning Output");
  assert.equal(zh.t("usage.reasoningOutput"), "推理输出");
  assert.equal(zh.t("usage.total"), "总计");
  assert.equal(zh.t("usage.duration"), "耗时");
});

test("frozen execution selection labels preserve dynamic model IDs and reject unknown reasoning", () => {
  const en = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const zh = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "zh-CN"}), navigator: {languages: ["en"]}});
  assert.equal(Components.modelLabel("gpt-5.6-terra", en.t), "GPT-5.6 Terra");
  assert.equal(Components.modelLabel("gpt-5.6-terra", zh.t), "GPT-5.6 Terra");
  assert.equal(Components.modelLabel("", en.t), "");
  assert.equal(Components.modelLabel("gpt-ancient", en.t), "gpt-ancient");
  assert.equal(Components.reasoningLabel("high", en.t), "High");
  assert.equal(Components.reasoningLabel("high", zh.t), "高");
  assert.equal(Components.reasoningLabel("extreme", en.t), "");
  assert.equal(Components.reasoningLabel(null, en.t), "");
});

test("Agent operation recovery projection renders distinct states without runtime internals", () => {
  const en = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const zh = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "zh-CN"}), navigator: {languages: ["en"]}});
  const recovery = Components.agentOperationStateBadge("RECOVERY_REQUIRED", en.t);
  assert.match(recovery, /Recovery required/);
  assert.match(recovery, /unknown backend state/);
  const busy = Components.agentOperationStateBadge("BUSY", en.t);
  assert.match(busy, /Agent operation in progress/);
  assert.equal(Components.agentOperationStateBadge("READY", en.t), "");
  assert.equal(Components.agentOperationStateBadge("", en.t), "");
  assert.equal(Components.agentOperationStateBadge("runtimeEpoch=42", en.t), "");
  assert.match(Components.agentOperationStateBadge("RECOVERY_REQUIRED", zh.t), /需要恢复/);
  const guidance = en.t("agentOperation.recoveryRequired.guidance");
  assert.match(guidance, /cannot be safely resumed/);
  assert.match(guidance, /abandoned/);
  assert.match(guidance, /history is retained/);
  assert.match(guidance, /fresh context/);
  assert.match(guidance, /agent operation recovery command/);
  assert.match(zh.t("agentOperation.recoveryRequired.guidance"), /无法安全续接/);
  assert.doesNotMatch(guidance, /resume the operation|retry|force unlock/);
});


test("blocked and failed reviews surface the durable blocker reason verbatim", () => {
  const i18n = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const reason = `start codex app-server: exec: "codex": executable file not found in $PATH`;
  const blocked = Components.reviewListCell({state: "BLOCKED", blocker_reason: reason}, i18n.t);
  assert.match(blocked, /status-badge error/);
  assert.ok(blocked.includes("executable file not found in $PATH"));
  assert.ok(blocked.includes("result-preview"));
  const failed = Components.reviewListCell({state: "FAILED", blocker_reason: "verification failed: 2 tests"}, i18n.t);
  assert.ok(failed.includes("verification failed: 2 tests"));
  assert.ok(!Components.reviewListCell({state: "COMPLETE", blocker_reason: reason}, i18n.t).includes("result-preview"));
  assert.ok(!Components.reviewListCell({state: "BLOCKED"}, i18n.t).includes("result-preview"));
  assert.ok(!Components.reviewListCell({state: "BLOCKED", blocker_reason: "  "}, i18n.t).includes("result-preview"));
});

test("log line formatting renders real method, path, tool and status data", () => {
  const i18n = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const t = i18n.t;
  const localmcpCompleted = LogFormat.formatLogLine({component: "localmcp", event: "localmcp.tool.completed", message: "Local MCP tool call completed", data: {method: "tools/call"}}, t);
  assert.ok(localmcpCompleted.includes("tools/call"), localmcpCompleted);
  assert.ok(localmcpCompleted.includes(t("logs.line.completed")));
  const mcpSucceeded = LogFormat.formatLogLine({component: "mcp", event: "mcp.tool.succeeded", data: {tool: "read_file", durationMillis: 370}}, t);
  assert.ok(mcpSucceeded.includes("read_file") && mcpSucceeded.includes("370 ms"), mcpSucceeded);
  const http = LogFormat.formatLogLine({component: "http", event: "local.request.completed", data: {method: "GET", path: "/v1/tasks", status: 200, durationMillis: 12}}, t);
  assert.ok(http.includes("GET /v1/tasks") && http.includes("200") && http.includes("12 ms"), http);
  const httpFailed = LogFormat.formatLogLine({component: "http", event: "local.request.failed", errorCode: "LIMIT", data: {method: "POST", path: "/v1/runs"}}, t);
  assert.ok(httpFailed.includes("POST /v1/runs") && httpFailed.includes(t("logs.line.failed")) && httpFailed.includes("LIMIT"), httpFailed);
  const fallback = LogFormat.formatLogLine({component: "review", event: "review.created", message: "Review recorded"}, t);
  assert.equal(fallback, "Review recorded");
  const empty = LogFormat.formatLogLine({component: "review", event: "review.created"}, t);
  assert.ok(!empty.includes("["), empty);
});

test("protocol noise classifier hides handshakes and receive halves but keeps errors", () => {
  const entry = (overrides) => ({level: "INFO", component: "localmcp", event: "localmcp.tool.completed", data: {method: "tools/call"}, ...overrides});
  assert.equal(LogFormat.isProtocolNoise(entry({data: {method: "tools/list"}})), true);
  assert.equal(LogFormat.isProtocolNoise(entry({data: {method: "ping"}})), true);
  assert.equal(LogFormat.isProtocolNoise(entry({data: {method: "notifications/initialized"}})), true);
  assert.equal(LogFormat.isProtocolNoise(entry({data: {method: "tools/call"}})), false);
  assert.equal(LogFormat.isProtocolNoise(entry({component: "http", event: "local.request.requested", data: {}})), true);
  assert.equal(LogFormat.isProtocolNoise(entry({component: "http", event: "local.request.completed", data: {method: "GET", status: 200}})), true);
  assert.equal(LogFormat.isProtocolNoise(entry({component: "mcp", event: "mcp.tool.requested", data: {tool: "read_file"}})), true);
  assert.equal(LogFormat.isProtocolNoise(entry({component: "mcp", event: "mcp.tool.succeeded", data: {tool: "tools/list"}})), true);
  assert.equal(LogFormat.isProtocolNoise(entry({component: "mcp", event: "mcp.tool.succeeded", data: {tool: "read_file"}})), false);
  assert.equal(LogFormat.isProtocolNoise(entry({component: "mcp", event: "mcp.tool.failed", data: {method: "tools/list"}})), false);
  assert.equal(LogFormat.isProtocolNoise(entry({level: "ERROR", data: {method: "ping"}})), false);
  assert.equal(LogFormat.isProtocolNoise(entry({level: "WARN", data: {method: "tools/list"}})), false);
  assert.equal(LogFormat.isProtocolNoise(entry({component: "review", event: "featurearea.review.start.accepted", data: {}})), false);
});

test("structured log filters match exactly and text searches event, message and data", () => {
  const entries = [
    {level: "INFO", component: "mcp", event: "mcp.tool.succeeded", message: "done", data: {tool: "read_file"}},
    {level: "ERROR", component: "http", event: "local.request.failed", message: "boom", errorCode: "BAD", data: {path: "/v1/runs"}},
    {level: "INFO", component: "localmcp", event: "localmcp.tool.completed", message: "done", data: {method: "tools/call"}}
  ];
  assert.equal(LogFormat.filterLogs(entries, {}).length, 3);
  assert.equal(LogFormat.filterLogs(entries, {level: "ERROR"}).length, 1);
  assert.equal(LogFormat.filterLogs(entries, {component: "mcp"}).length, 1);
  assert.equal(LogFormat.filterLogs(entries, {event: "localmcp.tool.completed"}).length, 1);
  assert.equal(LogFormat.filterLogs(entries, {text: "read_file"}).length, 1);
  assert.equal(LogFormat.filterLogs(entries, {text: "BOOM"}).length, 1);
  assert.equal(LogFormat.filterLogs(entries, {text: "missing"}).length, 0);
  assert.equal(LogFormat.filterLogs(entries, {}).length, 3);
  const noisy = [{level: "INFO", component: "http", event: "local.request.requested", data: {}}, {level: "ERROR", component: "http", event: "local.request.failed", data: {}}];
  assert.equal(LogFormat.filterLogs(noisy, {}).length, 1);
  assert.equal(LogFormat.filterLogs(noisy, {verbose: true}).length, 2);
  const facets = LogFormat.logFacets(entries);
  assert.deepEqual(facets.levels, ["ERROR", "INFO"]);
  assert.deepEqual(facets.components, ["http", "localmcp", "mcp"]);
});

test("log view state machine counts pending entries while paused and clears on resume", () => {
  const first = [{timestamp: "2026-09-07T10:03:00Z"}, {timestamp: "2026-09-07T10:02:00Z"}];
  let view = LogView.nextView(LogView.initialState(), first);
  assert.equal(view.pendingCount, 0);
  assert.equal(view.lastTimestamp, "2026-09-07T10:03:00Z");
  view = LogView.togglePause(view);
  assert.equal(view.paused, true);
  assert.equal(view.follow, false);
  view = LogView.nextView(view, [{timestamp: "2026-09-07T10:05:00Z"}, {timestamp: "2026-09-07T10:04:00Z"}, {timestamp: "2026-09-07T10:01:00Z"}]);
  assert.equal(view.pendingCount, 2);
  assert.equal(view.lastTimestamp, "2026-09-07T10:05:00Z");
  view = LogView.jumpToLatest(view);
  assert.equal(view.paused, false);
  assert.equal(view.pendingCount, 0);
  view = LogView.nextView(view, [{timestamp: "2026-09-07T10:06:00Z"}]);
  assert.equal(view.pendingCount, 0);
  const pausedAgain = LogView.togglePause(view);
  assert.equal(pausedAgain.paused, true);
  assert.equal(LogView.togglePause(pausedAgain).paused, false);
});

test("Console refresh preserves selected project and active project tab", () => {
  const fs = require("node:fs");
  const app = fs.readFileSync(`${__dirname}/ui/product-app.js`, "utf8");
  assert.ok(app.includes('const PROJECT_KEY = "devbridge.console.selectedProject"'));
  assert.ok(app.includes('const TAB_KEY = "devbridge.console.projectTab"'));
  const loadAll = app.slice(app.indexOf("async function loadAll("), app.indexOf("function currentProject()"));
  assert.ok(loadAll.includes("state.projects.some((project) => project.id === state.selectedProjectID)"));
  assert.ok(loadAll.includes("persistProject(state.selectedProjectID)"));
  assert.ok(!loadAll.includes("state.selectedProjectID = selection.selected_project_id || \"\";\n      await"), "refresh must not unconditionally reset the selection");
  const selectProject = app.slice(app.indexOf("async function selectProject("), app.indexOf("async function refreshLogs("));
  assert.ok(selectProject.includes("setProjectTab(\"overview\")"), "explicit project switches still reset the tab");
  assert.ok(selectProject.includes("persistProject(projectID)"));
});


test("Tasks list preserves direct semantic phase/status and excludes child inventories", () => {
  const fs=require("node:fs"),vm=require("node:vm"),source=fs.readFileSync(`${__dirname}/ui/product-app.js`,"utf8");
  const i18n=I18n.createI18n({navigator:{languages:["en"]}}),context={C:Components,t:i18n.t,i18n};
  vm.createContext(context);vm.runInContext(source.slice(source.indexOf("  function workItems("),source.indexOf("  function renderTasks(")),context);
  const tasks=[{id:"task-reviewing",title:"Semantic engineering title",phase:"REVIEWING",phaseLabel:"Review",status:"ACTIVE",updated_at:"2026-09-12",commitSha:"abc12345678",mergedCommit:"def56789012",work:{phase:"SUCCEEDED",type:"REVIEW"}},{id:"task-done",title:"Completed Task",phase:"SUCCEEDED",phaseLabel:"Done",status:"SUCCEEDED",updated_at:"2026-09-11"}];
  const items=context.workItems({tasks,executions:[{id:"execution-op",task_id:"task-reviewing",state:"FAILED"}],featureAreas:[{reviews:[{id:"review-op",state:"SUCCEEDED"}]}]});
  assert.deepEqual([...items.map(v=>v.id)],["task-reviewing","task-done"]);
  assert.equal(items[0].phase,"REVIEWING");assert.equal(items[0].state,"ACTIVE");
  const html=context.renderWorkItemRows(items);
  assert.match(html,/<span>Task<\/span><span>Phase<\/span><span>Status<\/span><span>Code<\/span><span>Dev<\/span>/);
  assert.match(html,/data-task-detail="task-reviewing"/);assert.match(html,/Semantic engineering title/);assert.match(html,/abc1234/);assert.match(html,/def5678/);
  assert.doesNotMatch(html,/execution-op|review-op|run_id|data-review-detail/);
});

test("Task filters persist area and branch without retaining a Review pseudo-task filter", () => {
  const fs=require("node:fs"),vm=require("node:vm"),source=fs.readFileSync(`${__dirname}/ui/product-app.js`,"utf8");
  const context={};vm.createContext(context);vm.runInContext(source.slice(source.indexOf("  function parseWorkItemFilters("),source.indexOf("  function persistWorkItemFilters(")),context);
  const saved=context.parseWorkItemFilters(JSON.stringify({type:"review",area:"area",branch:"refs/heads/dev"}));
  assert.equal(saved.area,"area");assert.equal(saved.branch,"refs/heads/dev");assert.notEqual(saved.type,"review");
  assert.doesNotMatch(source,/work-item-type-filter/);
});

test("Development Branch filter composes with Feature Area, Task status and work kind", () => {
  const fs = require("node:fs"), vm = require("node:vm");
  const source = fs.readFileSync(`${__dirname}/ui/product-app.js`, "utf8");
  const start = source.indexOf("  function workItems("), end = source.indexOf("  function renderExecutions(", start);
  const i18n = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const state = {
    projectWorkspace: {
      featureAreas: [
        {id: "area-1", title: "Console", reviews: [{id: "review-a", feature_area_id: "area-1", state: "COMPLETE", repository_provenance: {branch_kind: "BRANCH", branch_ref: "refs/heads/dev/a"}}]},
        {id: "area-2", title: "Runtime", reviews: []}
      ],
      tasks: [
        {id: "task-a", title: "Active A", feature_area_id: "area-1", feature_area_title: "Console", status: "ACTIVE", phase:"IMPLEMENTING", work_kind: "FEATURE", development_branches: ["refs/heads/dev/a", "refs/heads/dev/b"]},
        {id: "task-b", title: "Complete B", feature_area_id: "area-1", feature_area_title: "Console", status: "SUCCEEDED", phase:"SUCCEEDED", work_kind: "BUG_FIX", development_branches: ["refs/heads/dev/b"]},
        {id: "task-unknown", title: "Historical", feature_area_id: "area-2", feature_area_title: "Runtime", status: "ACTIVE", phase:"IMPLEMENTING", work_kind: "FEATURE", development_branches: []}
      ],
      executions: []
    },
    workItemTypeFilter: "all", workItemAreaFilter: "area-1", workItemBranchFilter: "refs/heads/dev/a", taskFilter: "all", taskKindFilter: "all"
  };
  const context = {workRefreshControl:()=>"",C: {...Components}, t: i18n.t, i18n, state, renderCompactEmpty: (title, body) => `${title}|${body}`};
  vm.createContext(context); vm.runInContext(source.slice(start, end), context);
  let html = context.renderTasks();
  assert.match(html, /task-a/); assert.doesNotMatch(html, /review-a/); assert.doesNotMatch(html, /task-b|task-unknown/);
  assert.match(html, />dev\/a</); assert.match(html, />dev\/b</); assert.match(html, />Unknown \/ Historical</);

  state.workItemTypeFilter = "task"; state.workItemBranchFilter = "refs/heads/dev/b"; state.taskFilter = "completed"; state.taskKindFilter = "BUG_FIX";
  html = context.renderTasks();
  assert.match(html, /task-b/); assert.doesNotMatch(html, /task-a|review-a|task-unknown/);
  state.workItemAreaFilter = "area-2"; state.workItemBranchFilter = "__unknown__"; state.taskFilter = "active"; state.taskKindFilter = "FEATURE";
  html = context.renderTasks();
  assert.match(html, /task-unknown/); assert.doesNotMatch(html, /task-a|task-b|review-a/);
});

test("review detail shows verbatim result, blocker, provenance and full usage facts", () => {
  const fs = require("node:fs"), vm = require("node:vm");
  const source = fs.readFileSync(`${__dirname}/ui/product-app.js`, "utf8");
  const start = source.indexOf("  function usageFactsBlock("), end = source.indexOf("  function renderReviewDrift(", start);
  const i18n = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const context = {C: {...Components}, t: i18n.t, i18n};
  vm.createContext(context);
  vm.runInContext(source.slice(source.indexOf("  function relativeActivity("),source.indexOf("  async function answerAgentRequest(")),context);
  vm.runInContext(source.slice(start, end), context);
  const html = context.renderReviewDetail({
    state: "FAILED", blocker_reason: "start codex: not found", result: "Refactored the selector.", provenance: "abc123", model: "gpt-5.6-terra", reasoning_effort: "high",
    usage: {status: "AVAILABLE", usage: {input_tokens: 39842, cached_input_tokens: 12000, output_tokens: 2817, total_tokens: 52659}}, created_at: "2026-09-07T12:00:00Z", prompt_available: true, id: "review-1"
  }, "Console");
  assert.ok(html.includes("Refactored the selector."));
  assert.ok(html.includes("start codex: not found"));
  assert.ok(html.includes("abc123"));
  assert.ok(!html.includes("Non-cached Input"), "do not derive absent token breakdowns");
  assert.ok(html.includes("39,842"));
  const safe = context.renderReviewDetail({state: "COMPLETE", usage: null, created_at: "2026-09-07T12:00:00Z"}, "Console");
  assert.ok(!safe.includes("<pre>"), "empty reviews render no result or blocker blocks");
  const upper=context.renderReviewDetail({executorKind:"UPPER_LLM",state:"REVIEWING",run_id:"logical-run",activity:{executionMethod:"AI 读取文件并提交审查报告",summary:"等待发起对话中的 AI",inactive:true,notice:"已至少 5 分钟没有新的 Bridge 文件审查活动"},created_at:"2026-09-07T12:00:00Z"},"Console");
  assert.match(upper,/AI 读取文件并提交审查报告/);assert.match(upper,/5 分钟/);
  assert.doesNotMatch(upper,/data-open-run-session|Codex 尚未报告执行计划|<h3>Codex/);

});

test("review detail separates frozen focus, authorization, successful, failed and denied observations", () => {
  const fs = require("node:fs"), vm = require("node:vm");
  const source = fs.readFileSync(`${__dirname}/ui/product-app.js`, "utf8");
  const start = source.indexOf("  function usageFactsBlock("), end = source.indexOf("  function renderReviewDrift(", start);
  for (const locale of ["en", "zh-CN"]) {
    const i18n = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: locale}), navigator: {languages: [locale]}});
    const context = {C: {...Components}, t: i18n.t, i18n};
    vm.createContext(context);
    vm.runInContext(source.slice(source.indexOf("  function relativeActivity("),source.indexOf("  async function answerAgentRequest(")),context);
    vm.runInContext(source.slice(start, end), context);
    const detail = {
      state: "COMPLETE", created_at: "2026-09-07T12:00:00Z",
      focus: {mode: "FOCUSED", summary: "Frozen <focus>", primary_modules: ["original-primary"], supporting_modules: ["original-support"], production_roots: ["internal/original"]},
      observed_evidence: {authorization_status: "GRANTED", repository_inspection_observed: true, successful_host_tool_operations: 2, failed_host_tool_operations: 3, forbidden_host_tool_operations: 5, observation_boundary: "BRIDGE_HOST_TOOLS_ONLY"}
    };
    const html = context.renderReviewDetail(detail, "Review area");
    for (const value of ["Frozen &lt;focus&gt;", "original-primary", "original-support", "internal/original", i18n.t("review.frozenFocus"), i18n.t("review.focusMode.FOCUSED"), i18n.t("review.hostToolsOnly"), i18n.t("review.evidenceBoundary")]) assert.ok(html.includes(value), value);
    assert.ok(!html.includes("Frozen <focus>"));
    const fact = (label, value) => Components.keyValueDetails([[i18n.t(label), value]]).slice('<dl class="key-values">'.length, -'</dl>'.length);
    for (const [label, value] of [["review.successfulOperations", "2"], ["review.failedOperations", "3"], ["review.forbiddenOperations", "5"], ["review.authorization", i18n.t("review.authorizationGranted")], ["review.inspectionObserved", i18n.t("review.inspectionYes")]]) {
      assert.ok(html.includes(fact(label, value)), `${locale}: ${label} must show ${value}`);
    }
    detail.observed_evidence.successful_host_tool_operations = 0;
    detail.observed_evidence.repository_inspection_observed = false;
    const denied = context.renderReviewDetail(detail, "Review area");
    assert.ok(denied.includes(fact("review.successfulOperations", "0")));
    assert.ok(denied.includes(fact("review.inspectionObserved", i18n.t("review.inspectionNo"))));
    assert.ok(!denied.includes(fact("review.inspectionObserved", i18n.t("review.inspectionYes"))));
    detail.observed_evidence.authorization_status = "UNKNOWN";
    assert.ok(context.renderReviewDetail(detail, "Review area").includes(fact("review.authorization", i18n.t("common.unknown"))));
    for (const key of ["review.frozenFocus", "review.focusMode", "review.primaryModules", "review.supportingModules", "review.focusPaths", "review.observedEvidence", "review.authorization", "review.inspectionObserved", "review.successfulOperations", "review.failedOperations", "review.forbiddenOperations", "review.observationBoundary", "review.focusBoundary", "review.evidenceBoundary"]) {
      assert.ok(I18n.messages[locale][key], `${locale} missing ${key}`);
    }
  }
});


test("project selector mounts in the global topbar for every view", () => {
  const fs = require("node:fs");
  const app = fs.readFileSync(`${__dirname}/ui/product-app.js`, "utf8");
  const html = fs.readFileSync(`${__dirname}/ui/index.html`, "utf8");
  assert.ok(html.includes('<div class="topbar-project" id="project-selector-slot"></div>'), "index.html provides the topbar mount");
  const render = app.slice(app.indexOf("function render() {"), app.indexOf("async function selectProject("));
  assert.ok(render.includes('$("#project-selector-slot").innerHTML = renderProjectSelector();'));
  const overview = app.slice(app.indexOf("function renderOverview() {"), app.indexOf("function renderGoal("));
  assert.ok(!overview.includes("renderProjectSelector()"), "overview no longer embeds its own selector");
  const projects = app.slice(app.indexOf("function renderProjects() {"), app.indexOf("function renderProjectDetail("));
  assert.ok(!projects.includes("renderProjectSelector()"), "projects no longer embeds its own selector");
  const css = fs.readFileSync(`${__dirname}/ui/product.css`, "utf8");
  assert.match(css, /\.topbar-project \{[^}]*flex:\s*0 1 clamp\(/);
  assert.match(css, /\.topbar \.health \{ margin-left: 0;/);
});

test("drawer, exact Review modal, close action and refresh feedback obey the UX contract", () => {
  const fs = require("node:fs");
  const app = fs.readFileSync(`${__dirname}/ui/product-app.js`, "utf8");
  const html = fs.readFileSync(`${__dirname}/ui/index.html`, "utf8");
  const css = fs.readFileSync(`${__dirname}/ui/product.css`, "utf8");
  for (const marker of ['id="detail-layer"', 'id="detail-backdrop"', 'id="result-modal"', 'aria-modal="true"', 'id="result-modal-backdrop"']) assert.ok(html.includes(marker), marker);
  assert.match(css, /\.detail\s*\{[^}]*width:\s*min\(1060px, 88vw\)/);
  assert.match(css, /@media[^}]+[\s\S]*\.detail \{ width: 100vw/);
  assert.ok(app.includes('$("#detail-backdrop").addEventListener("click", closeDetail)'));
  assert.ok(app.includes('$("#result-modal-backdrop").addEventListener("click", (event) => { event.stopPropagation(); closeResultModal(); })'));
  const vm=require("node:vm"),actions=[];let listener,modal=false,popover=null,view="RUN";
  const context={document:{addEventListener:(_,fn)=>listener=fn},$:selector=>["#retire-dialog","#settings-dialog"].includes(selector)?{open:false}:selector==="#result-modal"?{classList:{contains:()=>!modal}}:selector.includes("data-action-popover")?popover:null,state:{projectDropdownOpen:false},detailRoute:{get:()=>({view})},closeResultModal:()=>actions.push("modal"),returnToTask:()=>actions.push("task"),closeDetail:()=>actions.push("close"),render:()=>{}};
  vm.createContext(context);vm.runInContext(app.split("\n").find(line=>line.includes('document.addEventListener("keydown"')),context);
  const escape=()=>listener({key:"Escape",preventDefault(){}});
  modal=true;escape();assert.equal(actions.pop(),"modal");modal=false;
  let focused=false;popover={open:true,querySelector:()=>({focus:()=>focused=true})};escape();assert.equal(popover.open,false);assert.equal(focused,true);assert.equal(actions.length,0);
  popover=null;escape();assert.equal(actions.pop(),"task");view="TASK";escape();assert.equal(actions.pop(),"close");
  assert.ok(app.includes('/ui/api/reviews/${encodeURIComponent(reviewID)}/result?'));
  assert.ok(app.includes('class="full-result-content"'));
  assert.doesNotMatch(app,/window\.confirm\(`\$\{t\("topics\.closeConfirm"/);
  assert.ok(app.includes('await loadProjectData();'), "successful close re-reads authoritative state");
  assert.ok(!app.includes('topic.status = "RETIRED"'), "close must not optimistically mutate lifecycle state");
  assert.ok(app.includes('$("#refresh").addEventListener("click", () => loadAll(true))'));
  assert.ok(app.includes('function pollLogs()') && app.includes('refreshLogs();'), "automatic polling remains silent");
  assert.ok(app.includes('if (manual) toast(t("refresh.success"))'));
  assert.ok(app.includes('if (manual) toast(t("refresh.failed"), true)'));
  assert.ok(app.includes('if (toastTimer) window.clearTimeout(toastTimer)'), "new feedback replaces the prior toast timer");
});

test("Project workspace uses one wide responsive boundary and value-weighted work item columns", () => {
  const fs = require("node:fs");
  const css = fs.readFileSync(`${__dirname}/ui/product.css`, "utf8");
  const app = fs.readFileSync(`${__dirname}/ui/product-app.js`, "utf8");
  const main = css.match(/#main-content\s*\{([^}]+)\}/)[1];
  assert.match(main, /width:\s*min\(100%,1600px\)/);
  assert.match(main, /max-width:\s*1600px/);
  assert.match(main, /padding:[^;]*clamp\(24px,2\.2vw,32px\)/);
  assert.match(css, /\.project-workspace\s*\{[^}]*width:\s*100%/);
  const columns = css.match(/\.work-item-table \.dense-head, \.work-item-table \.dense-row \{ grid-template-columns:([^}]+)\}/)[1];
  assert.match(columns, /64px/);assert.match(columns, /minmax\(180px,2\.15fr\)/);assert.match(columns, /minmax\(150px,1\.45fr\)/);
  assert.match(css, /@media \(max-width: 1500px\)[\s\S]*\[data-column="type"\]/);
  assert.match(css, /@media \(max-width: 1380px\)[\s\S]*\[data-column="type"\][\s\S]*\[data-column="model"\]/);
  assert.match(css, /@media \(max-width: 760px\)[\s\S]*\.work-item-table \.dense-row[\s\S]*grid-template-columns:\s*1fr/);
  for (const key of ["tasks.topic", "common.status", "review.model", "common.updated"]) assert.ok(app.includes(`t("${key}")`), key);
  assert.match(app, /class="dense-table task-first-table"/);
});

test("Feature Area lifecycle filter keeps closed history reachable", () => {
  const fs = require("node:fs"), vm = require("node:vm");
  const source = fs.readFileSync(`${__dirname}/ui/product-app.js`, "utf8");
  const start = source.indexOf("  function renderFeatureAreas("), end = source.indexOf("  function renderDocuments(", start);
  const i18n = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const state = {featureAreaStatusFilter: "active", projectWorkspace: {featureAreas: [
    {id: "discovering", status: "DISCOVERING", product:{domainStatus:"ACTIVE"}}, {id: "active", status: "ACTIVE", product:{domainStatus:"ACTIVE"}}, {id: "retiring", status: "RETIRING", product:{domainStatus:"ACTIVE"}}, {id: "retired", status: "RETIRED", product:{domainStatus:"CLOSED"}}, {id: "decomposed", status: "DECOMPOSED", product:{domainStatus:"CLOSED"}}, {id: "legacy", status: "ACTIVE", legacy_read_only: true, product:{domainStatus:"CLOSED"}}, {id: "unknown", status: "REVIEWING", product:{technicalDetails:{diagnostic:"UNKNOWN_FEATURE_AREA_INTERNAL_STATUS"}}}
  ]}};
  const context = {C: {...Components}, t: i18n.t, state, renderTopicRows: (items) => items.map((item) => `ROW:${item.id}`).join("|"), renderCompactEmpty: () => "empty"};
  vm.createContext(context); vm.runInContext(source.slice(start, end), context);
  let rendered = context.renderFeatureAreas();
  assert.match(rendered, /ROW:discovering\|ROW:active\|ROW:retiring/); assert.doesNotMatch(rendered, /ROW:(retired|decomposed|legacy|unknown)/);
  state.featureAreaStatusFilter = "closed"; rendered = context.renderFeatureAreas();
  assert.match(rendered, /ROW:retired\|ROW:decomposed\|ROW:legacy/); assert.doesNotMatch(rendered, /ROW:(discovering|active|retiring|unknown)/);
  state.featureAreaStatusFilter = "all"; rendered = context.renderFeatureAreas();
  for (const id of ["discovering", "active", "retiring", "retired", "decomposed", "legacy", "unknown"]) assert.ok(rendered.includes(`ROW:${id}`));
});

test("Feature lifecycle menu is capability driven and belongs to Feature header/row", () => {
  const fs=require("node:fs"),vm=require("node:vm"),source=fs.readFileSync(`${__dirname}/ui/product-app.js`,"utf8");
  const context={C:Components,t:key=>key};vm.createContext(context);vm.runInContext(source.slice(source.indexOf("  function featureAreaRetireControl("),source.indexOf("  function renderTopicRows(")),context);
  const allowed={id:"feature",status:"ACTIVE",can_retire:true};
  assert.match(context.featureAreaMenu(allowed),/<details class="feature-menu"/);assert.match(context.featureAreaMenu(allowed),/data-close-feature-area="feature"/);
  for(const status of ["RETIRING","RETIRED","DECOMPOSED"])assert.doesNotMatch(context.featureAreaMenu({...allowed,status}),/data-close-feature-area/);
  assert.doesNotMatch(context.featureAreaMenu({...allowed,can_retire:false}),/data-close-feature-area/);
  const drawer=source.slice(source.indexOf("  function openTopicDetail("),source.indexOf("  const gitAreaRequests="));
  assert.match(drawer,/W\.renderFeatureAreaDetail\(workbenchContext\(\),displayed\)/);assert.doesNotMatch(drawer,/reviewApprovalPanel|topic\.reviews|topic\.active_review|featureAreaRetireControl\(topic/);
  assert.match(drawer,/can_retire:false,presentation:\{\.\.\.topic.presentation,canCommit:false,capabilities:\[\]\}/);
  const W=require("./ui/product-workbench.js"),rendered=W.renderFeatureAreaDetail({C:Components,t:key=>key,state:{projectWorkspace:{tasks:[],documents:[]}},legacy:{featureAreaMenu:context.featureAreaMenu}},allowed);
  assert.match(rendered.split("</header>")[0],/data-close-feature-area="feature"/);assert.doesNotMatch(rendered,/data-task-action|data-execution-cancel/);
});

test("work item and review UX i18n keys exist in English and Simplified Chinese", () => {
  const en = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "en"}), navigator: {languages: ["en"]}});
  const zh = I18n.createI18n({storage: memoryStorage({[I18n.STORAGE_KEY]: "zh-CN"}), navigator: {languages: ["en"]}});
  for (const key of ["workItems.type", "workItems.typeAll", "workItems.typeTask", "workItems.typeReview", "workItems.areaAll", "workItems.columnName", "workItems.empty.title", "workItems.empty.body", "workItems.result", "workItems.failure", "workItems.blocker", "workItems.provenance", "usage.nonCachedInput", "usage.reasoningOutput", "branches.development", "branches.all", "branches.unknown", "review.viewFullResult", "review.fullResult", "topics.lifecycle", "topics.statusFilter", "topics.filterActive", "topics.filterClosed", "topics.filterAll", "topics.close", "topics.closed", "topics.closeUnavailable", "topics.closeUnavailableUnknown", "topics.closeConfirm", "topics.closeConsequences", "topics.closeSuccess", "topics.closeReadbackFailed", "refresh.success", "refresh.failed"]) {
    assert.doesNotMatch(en.t(key), /^\[/, key);
    assert.doesNotMatch(zh.t(key), /^\[/, key);
  }
  assert.doesNotMatch(en.t("tasks.subtitle"), /Feature Area Reviews/);
  assert.doesNotMatch(zh.t("tasks.subtitle"), /功能领域审查/);
});

test("Task phase/current-action is the only workflow action dispatch surface", () => {
  const fs=require("node:fs"),source=fs.readFileSync(`${__dirname}/ui/product-app.js`,"utf8");
  assert.doesNotMatch(source,/resolveReviewApproval|reviewApprovalPanel|featureAreas\/\$\{encodeURIComponent\(areaID\)\}\/review-approval|ui_task_decide|ui_execution_decide|ui_decision_get/);
  const dispatch=source.slice(source.indexOf("  async function invokeTaskAction("),source.indexOf("  async function openReviewPrompts("));
  assert.match(dispatch,/taskPresentationActions\(task\.presentation\)\.find/);assert.match(dispatch,/cap\.target/);assert.match(dispatch,/taskActionRequests/);
});

test("official Agent telemetry renders actual plan, network request and escaped output", () => {
  const fs=require("node:fs"), vm=require("node:vm");
  const source=fs.readFileSync(`${__dirname}/ui/product-app.js`,"utf8");
  const start=source.indexOf("  function relativeActivity("),end=source.indexOf("  function openTaskDetail(",start);
  const i18n=I18n.createI18n({storage:memoryStorage({[I18n.STORAGE_KEY]:"en"}),navigator:{languages:["en"]}});
  const context={C:Components,i18n};vm.createContext(context);vm.runInContext(source.slice(start,end),context);
  assert.match(context.renderAgentRuntime(null),/Codex 尚未报告执行计划/);
  const state={availability:"AVAILABLE",runId:"run",waitingState:"WAITING_FOR_COMMAND_APPROVAL",latestPlan:{completed:1,total:2,steps:[{step:"inspect",status:"completed"},{step:"<script>run</script>",status:"inProgress"}]},currentItem:{type:"commandExecution",status:"inProgress",metadata:{command:"echo <script>",cwd:"/tmp"},outputTail:"<img onerror=bad>"},requests:[{state:"PENDING",method:"item/commandExecution/requestApproval",payload:{networkApprovalContext:{host:"api.example.com",protocol:"https"}},availableResponses:[{decision:"decline"}]}]};
  const html=context.renderAgentRuntime(state);
  assert.match(html,/Codex 执行计划 · 1 \/ 2/);assert.match(html,/Network access request · api.example.com · https/);
  assert.ok(!html.includes("<script>"));assert.ok(!html.includes("<img onerror"));assert.match(html,/&lt;img/);
  assert.match(html,/运行时请求状态待确认/);assert.doesNotMatch(html,/data-agent-answer|Allow once|Decline/);
  state.requests[0].state="INTENDED";assert.ok(!context.renderAgentRuntime(state).includes("data-agent-answer"));
});

test("runtime user decision uses the canonical endpoint and preserves every answer", async () => {
  const fs=require("node:fs"), vm=require("node:vm");
  const source=fs.readFileSync(`${__dirname}/ui/product-app.js`,"utf8");
  const start=source.indexOf("  function relativeActivity("),end=source.indexOf("  function openTaskDetail(",start);
  let body, path, reads=0, opens=0;
  const question={value:"yes",reportValidity:()=>true,dataset:{agentQuestion:"0"}};
  const section={querySelectorAll:selector=>selector==="[data-agent-question]"?[question]:[],querySelector:()=>null};
  const context={C:Components,i18n:{locale:"en"},workflowDrawerVersion:1,state:{selectedProjectID:"project"},api:(url,options)=>{path=url;body=JSON.parse(options.body);return Promise.resolve({});},loadProjectData:async()=>{reads++;},openTaskDetail:()=>{opens++;},toast:()=>{},crypto:{randomUUID:()=>"submission"},window:{crypto:{randomUUID:()=>"submission"},DevBridgeSessionWorkbench:{decisionPayloadSHA256:async()=>"canonical-hash"}}};
  vm.createContext(context);vm.runInContext(source.slice(start,end),context);
  const waiting={projection:{lifecycleState:"WAITING_FOR_USER",responseDisposition:"NOT_DISPATCHED",availableActions:["RESPOND"]},kind:"CODEX_REQUEST",projectId:"project",featureAreaId:"area",taskId:"task",taskDefinitionRevisionId:"revision",operationKind:"EXECUTION",operationId:"execution",executionId:"execution",runId:"run",threadId:"thread",turnId:"turn",connectionId:"connection",runtimeEpoch:"epoch",runtimeRequestId:"request",state:"WAITING_FOR_USER",method:"item/tool/requestUserInput",availableActions:["RESPOND"],payload:{questions:[{id:"adaptation",question:"Continue?",options:[{id:"yes",label:"yes"}]}]}};
  const html=context.renderAgentRuntime({availability:"AVAILABLE",requests:[waiting]});
  assert.match(html,/<option value="" disabled selected>请选择<\/option>/);
  const button={dataset:{agentAnswer:"0",decisionAction:"RESPOND"},isConnected:true,closest:()=>section};
  await context.answerAgentRequest(button);
  assert.equal(path,"/ui/api/runs/run/agent-user-decisions");
  assert.deepEqual(body.answers,[{questionId:"adaptation",selectedOptionId:"yes",rawText:""}]);
  assert.equal(body.runtimeRequestId,"request");assert.equal(body.expectedState,"WAITING_FOR_USER");assert.equal(body.payloadSHA256,"canonical-hash");
  assert.equal(reads,1);assert.equal(opens,1);
  const unknown=context.renderAgentRuntime({availability:"AVAILABLE",requests:[{...waiting,state:"RESPONDING",projection:{lifecycleState:"RESPONDING",responseDisposition:"UNKNOWN",availableActions:["CONTINUE_WAITING","CANCEL_OPERATION","STOP_AND_REVISE"],title:"回答状态无法确认",summary:"系统不会自动重发。",userDecisionRecorded:true,userDecisionSummary:"yes"}}]});
  assert.doesNotMatch(unknown,/data-agent-answer|data-agent-question|data-decision-action="RESPOND"/);assert.match(unknown,/回答状态无法确认/);assert.match(unknown,/继续等待/);assert.match(unknown,/<pre>yes<\/pre>/);
  const unknownButton={dataset:{agentRecovery:"1",decisionAction:"CONTINUE_WAITING"},isConnected:true,closest:()=>section};
  const priorPath=path;await context.answerAgentRequest(unknownButton);assert.equal(path,priorPath,"wait performs no mutation");assert.equal(reads,2);
  unknownButton.dataset.decisionAction="CANCEL_OPERATION";await context.answerAgentRequest(unknownButton);assert.equal(path,"/ui/api/runs/run/agent-requests/recovery");assert.equal(body.action,"CANCEL_OPERATION");assert.equal(body.runtimeRequestId,"request");assert.equal(body.answers,undefined);assert.equal(body.rawText,undefined);
  const elicitation=context.renderAgentRuntime({availability:"AVAILABLE",requests:[{...waiting,runtimeRequestId:"elicitation",method:"mcpServer\/elicitation\/request",payload:{mode:"form",requestedSchema:{type:"object"}}}]});
  assert.match(elicitation,/data-agent-elicitation-content/);
});

test("ordinary navigation is Task-first and global Executions are read-only diagnostics",()=>{
 const fs=require("node:fs"),vm=require("node:vm"),source=fs.readFileSync(`${__dirname}/ui/product-app.js`,"utf8");
 const W=require("./ui/product-workbench.js");
 assert.deepEqual(W.projectTabs,["overview","featureAreas","tasks","sessions","resources","diagnostics"]);
 assert.deepEqual(W.resourceTabs,["documents","modules","repositories"]);assert.ok(W.diagnosticTabs.includes("runs"));
 const overview=source.slice(source.indexOf("  function renderProjectOverview("),source.indexOf("  function renderActiveAgentOperations("));
 assert.doesNotMatch(overview,/recentExecutions|renderExecutionRows|"executions"/);
 const diagnostic=source.slice(source.indexOf("  function renderDiagnostics("),source.indexOf("  function renderLoading("));
 assert.match(diagnostic,/Executions · runtime diagnostics/);assert.match(diagnostic,/C.detailDisclosure\("Runs"/);
 const global=W.renderDiagnostics({C:Components,t:key=>key,state:{diagnosticTab:"runs",runs:[{id:"exact-run",purpose:"INSPECTION",status:"RUNNING"}]}});assert.match(global,/data-diagnostic-run="exact-run"/);assert.doesNotMatch(global,/data-task-action|data-execution-cancel|data-close-feature-area/);
 const context={C:Components,t:k=>k,i18n:{locale:"en"},state:{projectWorkspace:{executions:[{id:"execution",task_id:"task",task_title:"Semantic Task",kind:"DEVELOPMENT",state:"RUNNING",cancellation_capability:{available:true,target:{executionId:"execution",runId:"run"}}}]}},workRefreshControl:()=>"",renderCompactEmpty:()=>""};
 vm.createContext(context);
 vm.runInContext(source.slice(source.indexOf("  function renderExecutionRows("),source.indexOf("  function renderContextSummary(")),context);
 const html=context.renderExecutionRows(context.state.projectWorkspace.executions);
 assert.match(html,/data-execution-detail="execution"/);assert.doesNotMatch(html,/data-execution-cancel|data-task-action|START_EXECUTION/);
});
