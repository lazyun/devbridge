import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import vm from 'node:vm';

const html = fs.readFileSync(new URL('./feature_area_details_ui.html', import.meta.url), 'utf8');
const script = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].map(match => match[1]).join('\n');

class Element {
  constructor(tag) {
    this.tag = tag;
    this.children = [];
    this._text = '';
    this.disabled = false;
  }
  append(...nodes) { this.children.push(...nodes); }
  set textContent(value) {
    this._text = String(value ?? '');
    if (this._text === '') this.children = [];
  }
  get textContent() { return this._text + this.children.map(child => child.textContent).join(''); }
}

function boot(toolInput = {}, close = async () => {}) {
  const root = new Element('main');
  const parent = { postMessage(value) { posts.push(structuredClone(value)); } };
  const posts = [];
  const listeners = {};
  const window = { openai: { toolInput, requestClose: close } };
  const context = {
    window,
    parent,
    document: {
      getElementById: () => root,
      createElement: tag => new Element(tag),
      documentElement: { style: {} }
    },
    addEventListener: (name, listener) => { listeners[name] = listener; },
    setTimeout,
    clearTimeout,
    Error,
    Math,
    Number,
    Map,
    Promise,
    String,
    JSON,
    Array
  };
  vm.runInNewContext(script, context);
  const send = (data, source = parent) => listeners.message({ data, source });
  const initialize = posts.find(value => value.method === 'ui/initialize');
  return {
    root,
    posts,
    window,
    send,
    initialize,
    reply(call, result = {}) { send({ jsonrpc: '2.0', id: call.id, result }); },
    reject(call, message = 'backend lost') { send({ jsonrpc: '2.0', id: call.id, error: { message } }); },
    calls() { return posts.filter(value => value.method === 'tools/call'); },
    buttons() {
      const found = [];
      const visit = node => {
        if (node.tag === 'button') found.push(node);
        for (const child of node.children) visit(child);
      };
      visit(root);
      return found;
    }
  };
}

const tick = () => new Promise(resolve => setImmediate(resolve));
const page = (overrides = {}) => ({
  schemaVersion: '1',
  featureArea: { ID: 'area', Title: 'Durable area' },
  reviews: [{ ID: 'review-original', FinalCodexPrompt: 'exact review prompt' }],
  tasks: [{ ID: 'task-original', CurrentDefinitionRevisionID: 'revision-original' }],
  taskPlans: [{ ID: 'plan-original' }],
  taskDefinitionRevisions: [{ ID: 'revision-original', PayloadSHA256: 'sha' }],
  executions: [{ ID: 'execution-original' }],
  runs: [{ ID: 'run-original', WorkspaceID: 'workspace-original', Result: { GitEvidence: { ChangedFiles: ['a.go'] } } }],
  workspaces: [{ ID: 'workspace-original', Status: 'CONTINUITY_UNAVAILABLE', RuntimeQuarantined: true }],
  approvals: [{ ID: 'approval-original' }],
  taskDecisionReceipts: [{ DecisionID: 'TASK_CONFIRMATION:plan-original' }],
  cancellationReceipts: [{ requestId: 'cancel-original' }],
  recoveryConversations: [{ ID: 'conversation-original' }],
  recoveryReceipts: [{ RequestID: 'recover-original' }],
  scopeExpansions: [{ ID: 'scope-original' }],
  sectionTotals: { tasks: 3, workspaces: 1, recoveryConversations: 1, recoveryReceipts: 1 },
  sectionHasMore: { tasks: true },
  limit: 2,
  offset: 0,
  hasPrevious: false,
  hasNext: true,
  ...overrides
});

test('window.openai.toolInput hydrates exact modal params and renders every camelCase section', async () => {
  let closed = 0;
  const app = boot({ aiSessionId: 'session', featureAreaId: 'area' }, async () => { closed++; });
  app.reply(app.initialize, { hostContext: { theme: 'dark' } });
  await tick();
  const call = app.calls()[0];
  assert.deepEqual(call.params.arguments, { aiSessionId: 'session', featureAreaId: 'area', limit: 20, offset: 0 });
  app.reply(call, { structuredContent: page() });
  await tick();
  for (const id of ['review-original', 'task-original', 'plan-original', 'revision-original', 'execution-original', 'run-original', 'workspace-original', 'approval-original', 'cancel-original', 'conversation-original', 'recover-original', 'scope-original']) {
    assert.match(app.root.textContent, new RegExp(id));
  }
  assert.match(app.root.textContent, /RuntimeQuarantined/);
  assert.match(app.root.textContent, /CONTINUITY_UNAVAILABLE/);
  const back = app.buttons().find(button => button.textContent === 'Back');
  await back.onclick();
  assert.equal(closed, 1);
});

test('MCP tool-input metadata and initialize hostContext both hydrate identity with parent source enforcement', async () => {
  const fromMeta = boot();
  fromMeta.reply(fromMeta.initialize, {});
  await tick();
  fromMeta.send({ jsonrpc: '2.0', method: 'ui/notifications/tool-input', params: { arguments: {}, _meta: { aiSessionId: 'session-meta', featureAreaId: 'area-meta' } } }, {});
  await tick();
  assert.equal(fromMeta.calls().length, 0);
  fromMeta.send({ jsonrpc: '2.0', method: 'ui/notifications/tool-input', params: { arguments: {}, _meta: { aiSessionId: 'session-meta', featureAreaId: 'area-meta' } } });
  await tick();
  assert.deepEqual(fromMeta.calls()[0].params.arguments, { aiSessionId: 'session-meta', featureAreaId: 'area-meta', limit: 20, offset: 0 });
  fromMeta.reply(fromMeta.calls()[0], { structuredContent: page() });

  const fromContext = boot();
  fromContext.reply(fromContext.initialize, { hostContext: { toolInput: { aiSessionId: 'session-context', featureAreaId: 'area-context' } } });
  await tick();
  assert.deepEqual(fromContext.calls()[0].params.arguments, { aiSessionId: 'session-context', featureAreaId: 'area-context', limit: 20, offset: 0 });
  fromContext.reply(fromContext.calls()[0], { structuredContent: page() });
});

test('minimal details show result uses result metadata to load the silent authoritative DetailsPage', async () => {
  const app = boot();
  app.reply(app.initialize, {});
  await tick();
  assert.equal(app.calls().length, 0);

  app.send({ jsonrpc: '2.0', method: 'ui/notifications/tool-result', params: {
    _meta: { aiSessionId: 'show-session', featureAreaId: 'show-area' },
    structuredContent: { schemaVersion: '1' }
  } });
  await tick();
  const read = app.calls()[0];
  assert.deepEqual(read.params, { name: 'feature_area_details_get', arguments: { aiSessionId: 'show-session', featureAreaId: 'show-area', limit: 20, offset: 0 } });
  app.reply(read, { structuredContent: page({ featureArea: { ID: 'show-area', Title: 'Shown area' } }) });
  await tick();
  assert.match(app.root.textContent, /Shown area/);
  assert.match(app.root.textContent, /task-original/);

  app.send({ jsonrpc: '2.0', method: 'ui/notifications/tool-result', params: {
    _meta: { aiSessionId: 'show-session', featureAreaId: 'show-area' },
    structuredContent: page({ featureArea: { ID: 'show-area', Title: 'Complete result' } })
  } });
  await tick();
  assert.match(app.root.textContent, /Complete result/);
  assert.equal(app.calls().length, 1);
});

test('a later identity read replaces a deferred earlier read without rendering stale details', async () => {
  const app = boot();
  app.reply(app.initialize, {});
  await tick();
  app.send({ jsonrpc: '2.0', method: 'ui/notifications/tool-input', params: { aiSessionId: 'session-a', featureAreaId: 'area-a' } });
  await tick();
  const readA = app.calls()[0];
  app.send({ jsonrpc: '2.0', method: 'ui/notifications/tool-input', params: { aiSessionId: 'session-b', featureAreaId: 'area-b' } });
  await tick();
  const readB = app.calls()[1];
  assert.deepEqual(readA.params.arguments, { aiSessionId: 'session-a', featureAreaId: 'area-a', limit: 20, offset: 0 });
  assert.deepEqual(readB.params.arguments, { aiSessionId: 'session-b', featureAreaId: 'area-b', limit: 20, offset: 0 });
  app.reply(readB, { structuredContent: page({ featureArea: { ID: 'area-b', Title: 'Current B' }, tasks: [{ ID: 'task-b' }] }) });
  await tick();
  app.reply(readA, { structuredContent: page({ featureArea: { ID: 'area-a', Title: 'Stale A' }, tasks: [{ ID: 'task-a' }] }) });
  await tick();
  assert.match(app.root.textContent, /Current B/);
  assert.match(app.root.textContent, /task-b/);
  assert.doesNotMatch(app.root.textContent, /Stale A|task-a/);
});

test('Next and Previous send offsets that page every history section', async () => {
  const app = boot({ aiSessionId: 'session', featureAreaId: 'area' });
  app.reply(app.initialize, {});
  await tick();
  app.reply(app.calls()[0], { structuredContent: page() });
  await tick();
  const next = app.buttons().find(button => button.textContent === 'Next');
  next.onclick();
  await tick();
  assert.equal(app.calls()[1].params.arguments.offset, 2);
  app.reply(app.calls()[1], { structuredContent: page({ offset: 2, hasPrevious: true, hasNext: false, tasks: [{ ID: 'task-third' }] }) });
  await tick();
  assert.match(app.root.textContent, /task-third/);
  const previous = app.buttons().find(button => button.textContent === 'Previous');
  previous.onclick();
  await tick();
  assert.equal(app.calls()[2].params.arguments.offset, 0);
  app.reply(app.calls()[2], { structuredContent: page() });
});

test('MCP errors are visible and requests have timeout cleanup', async () => {
  assert.match(html, /setTimeout\(\(\) =>/);
  assert.match(html, /clearTimeout\(requestState\.timer\)/);
  assert.match(html, /event\.source !== parent/);
  assert.doesNotMatch(html, /innerHTML|ObserveTerminal|ClaimContinuation|execution_cancel|ui_.*decide/);
  const app = boot({ aiSessionId: 'session', featureAreaId: 'area' });
  app.reply(app.initialize, {});
  await tick();
  app.reject(app.calls()[0], 'read failed');
  await tick();
  assert.match(app.root.textContent, /Details are unavailable: read failed/);
});
