// Loopback-only frozen product DTO fixture. Never opens a database or runner.
import http from 'node:http';
import {extendWorkbenchFixture, scenarios} from './task_workbench_fixture.mjs';
import {readFile} from 'node:fs/promises';
import {basename, resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

const ui = fileURLToPath(new URL('../ui/', import.meta.url));
const updated = '2026-09-12T10:30:00Z';
const titles = ['修复 Card 布局并保持窄屏操作可达', 'Task-owned Workflow contract closure', '等待正式审批的语义 Task'];
function task(index) {
  const id = `task-${index + 1}`, status = 'ACTIVE';
  const phase = ['IMPLEMENTING', 'ACCEPTANCE_PENDING', 'REVIEW_APPROVAL_PENDING'][index];
  const phaseLabel = ['Implementation', 'Verification complete', 'Review complete'][index];
  const phaseDescription = ['执行中', '等待验收', '等待审批'][index];
  const run = {id: `review-run-${index}`, transportStatus: 'SUCCEEDED'};
  const review = {id: `review-${index}`, taskId: id, ordinal: 1, reviewDepth: 'DEEP', reviewDisposition: 'APPROVAL_READY', status: 'SUCCEEDED', run, resultAvailable: true, instruction: '只读 Review instruction，不可作为 Task title。', createdAt: updated, updatedAt: updated};
  const implementation = {id: `implementation-${index}`, transportStatus: index === 0 ? 'RUNNING' : 'SUCCEEDED'};
  const verification = {id: `verification-${index}`, transportStatus: 'SUCCEEDED'};
  const execution = {id: `execution-${index}`, state: index === 0 ? 'IMPLEMENTING' : 'SUCCEEDED', outcome: index === 0 ? 'PENDING' : 'SUCCEEDED', taskDefinitionRevisionId: `definition-${index}`, implementationRunId: implementation.id, verificationRunId: index === 1 ? verification.id : ''};
  const view = {id, requiredReviewDepth: null, allowedReviewDepths: [], title: titles[index], goal: '保持明确的 Task ownership；operation history 不覆盖当前阶段。', kind: 'DEVELOPMENT', phase, phaseLabel, phaseDescription, status, overallState: status, terminal: false, readOnly: false,
    commitStatus: 'UNCOMMITTED', commitSha: '', devIntegrationStatus: 'NOT_MERGED', mergedCommit: '', commitSummary: 'Code · 未提交', devSummary: 'Dev · —',
    progress: ['REVIEW', 'APPROVED', 'IMPLEMENTATION', 'VERIFICATION', 'DONE'].map((phase, step) => ({phase, status: step < (index === 0 ? 2 : index === 1 ? 4 : 1) ? 'COMPLETE' : index === 0 && step === 2 ? 'CURRENT' : 'PENDING'})),
    activity: {status: index === 0 ? 'RUNNING' : 'IDLE', summary: index === 0 ? 'Implementation Run 正在执行' : '等待 Task 的下一步正式操作', elapsedMs: 12000}, goals: [],
    usage: {status: 'AVAILABLE', durationMs: 12000, durationComplete: true, implementationMs: 10000, verificationMs: 2000, otherMs: 0, runCount: 2, tokens: {input: 900, cachedInput: 300, nonCachedInput: 600, output: 200, reasoningOutput: 100, total: 1100}},
    currentWorkKind: index === 2 ? 'REVIEW' : 'EXECUTION', currentReview: index === 2 ? review : null, reviewHistory: [review], currentExecution: index === 2 ? null : execution, implementationRun: index === 2 ? null : implementation, verificationRun: index === 1 ? verification : null,
    executionHistory: index === 2 ? [] : [{current: true, ordinal: 1, execution, implementationRun: implementation, verificationRun: index === 1 ? verification : null}],
    currentAction: {code: index === 2 ? 'APPROVE_REVIEW' : 'WAIT', message: index === 2 ? '等待审批' : '等待当前工作收口'}, actions: (index === 2 ? ['APPROVE', 'REVISE', 'REJECT'] : index === 1 ? ['ACCEPT', 'REQUEST_CHANGES', 'REQUEST_REVERIFICATION', 'REJECT'] : []).map(action => ({action, available: true, target: {featureAreaId: 'area', taskId: id, reviewId: review.id}})), details: {taskDefinitionRevisionId: `definition-${index}`, runs: []}, updatedAt: updated};
  return {id, title: view.title, feature_area_id: 'area', feature_area_title: 'WebUI Task-owned closure', phase, phaseLabel, phaseDescription, status, state: status, commitStatus: view.commitStatus, commitSha: view.commitSha, devIntegrationStatus: view.devIntegrationStatus, mergedCommit: view.mergedCommit, updated_at: updated, created_at: updated, development_branches: ['dev_fixture'], work: {type: 'DEVELOPMENT'}, executions: [], presentation: {featureArea: {id: 'area', name: 'WebUI Task-owned closure'}, details: {taskId: id}, taskView: view, capabilities: view.actions, blockers: []}};
}

export function createFixtureServer({workbench = false, scenario = "workbench"} = {}) {
  const tasks = [0, 1, 2].map(task), metrics = {mutations: [], requests: []};
  const area = {id: 'area', title: 'WebUI Task-owned closure · 功能领域', summary: '功能领域拥有 Tasks；Review 和 Execution 作为 Task 的操作历史。', status: 'ACTIVE', task_count: tasks.length, can_retire: true, updated_at: updated,
    presentation: {featureArea: {id: 'area', name: 'WebUI Task-owned closure'}, currentTask: {id: tasks[0].id, status: 'ACTIVE'}, taskView: tasks[0].presentation.taskView, latestTask: {id: tasks[0].id, status: 'ACTIVE'}, commitStatus: 'UNCOMMITTED', commit: {developmentBranch: 'dev_fixture'}, commitView: {summary: '提交：未提交', actionLabel: '提交到 dev_fixture'}, canCommit: true, capabilities: [{action: 'RETIRE', available: true}, {action: 'COMMIT_FEATURE_AREA', available: true}], details: {taskId: tasks[0].id}, agentOperation: {status: 'RUNNING'}}};
  const workspace = {project_id: 'project', project_context: {state: 'READY', module_count: 0}, featureAreas: [area], tasks, executions: [], documents: [], workspaces: [], active_agent_operations: []};
  const extension = workbench ? extendWorkbenchFixture(tasks, area, workspace, scenario) : null;
  const server = http.createServer(async (req, res) => {
    try {
      const url = new URL(req.url, 'http://127.0.0.1'), path = url.pathname;
      res.setHeader('Cache-Control', 'no-store');
      metrics.requests.push({method: req.method, path});
      if (req.method !== 'GET') {
        if (workbench) {res.writeHead(405); res.end(JSON.stringify({message:'FIXTURE: business mutations disabled'})); return;}
        if (req.method !== 'POST' || path !== '/ui/api/featureAreas/area/close') {res.writeHead(405); res.end('Fixture rejects all other mutations'); return;}
        let body = ''; for await (const chunk of req) body += chunk;
        const value = JSON.parse(body);
        if (value.project_id !== 'project' || !value.request_id) {res.writeHead(400); res.end('{}'); return;}
        metrics.mutations.push(value); area.status = 'RETIRING'; area.can_retire = false;
        area.presentation.capabilities = area.presentation.capabilities.filter(c => c.action !== 'RETIRE');
        res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify({id: area.id, status: area.status})); return;
      }
      if (path === '/__fixture/metrics') {res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(metrics)); return;}
      if (path === '/readyz' || path.startsWith('/v1/') || path.startsWith('/ui/api/')) {
        const custom = extension?.read(path, url.searchParams);
        if (custom) {res.writeHead(custom.status || 200, {'Content-Type':'application/json'}); res.end(JSON.stringify(custom.data)); return;}
        let data = {items: []};
        if (path === '/v1/project-selection') data = {selected_project_id: 'project'};
        if (path === '/v1/projects') data = {items: [{id: 'project', name: 'Dev Bridge', project_context_status: 'READY', repository_count: 1}]};
        if (path === '/v1/project-overview') data = {project: {id: 'project', name: 'Dev Bridge', project_context_status: 'READY'}};
        if (path === '/ui/api/project-workspace') data = workspace;
        if (path === '/ui/api/featureAreas') data = {items: [area]};
        if (path === '/ui/api/featureAreas/area/presentation') data = area.presentation;
        if (path === '/v1/development-context') data = {projectId: 'project', repositories: []};
        if (path === '/v1/repositories' && workbench) data = {items:[{id:'repository',project_id:'project',name:'fixture-repository',current_head:'fixture-checkout-head',head_status:'AVAILABLE'}]};
        if (path === '/v1/tasks' || path === '/ui/api/work-tabs/tasks') {
          const q=url.searchParams; let items=workspace.tasks.filter(t=>(!q.get('search')||t.title.toLowerCase().includes(q.get('search').toLowerCase()))&&(!q.get('feature_area_id')||t.feature_area_id===q.get('feature_area_id'))&&(!q.get('status')||t.status===q.get('status'))&&(!q.get('phase')||t.phase===q.get('phase')));
          const total=items.length,limit=Math.min(100,Number(q.get('limit')||25)),offset=Math.max(0,Number(q.get('offset')||0));data={items:items.slice(offset,offset+limit),total,limit,offset};
        }
        const taskRead = path.match(/^\/(?:v1|ui\/api)\/tasks\/([^/]+)$/);
        if (taskRead) {data = tasks.find(t => t.id === taskRead[1]); if (!data || url.searchParams.get('project_id') !== 'project') {res.writeHead(404); res.end('{}'); return;}}
        res.setHeader('Content-Type', 'application/json'); res.end(JSON.stringify(data)); return;
      }
      const name = (path === '/' || path === '/ui/') ? 'index.html' : basename(path);
      if (name !== 'index.html' && !/^product[-a-z0-9]*\.(js|css)$/.test(name)) {res.writeHead(404); res.end(); return;}
      res.setHeader('Content-Type', name.endsWith('.js') ? 'application/javascript' : name.endsWith('.css') ? 'text/css' : 'text/html');
      let content = await readFile(resolve(ui, name));
      if (workbench && name === 'index.html') content = content.toString().replace(/<body\b[^>]*>|<template id="console-shell">/g, match => `${match}<div role="status" style="position:fixed;bottom:0;left:0;z-index:5000;background:#fff0b3;color:#302400;padding:4px 12px;font:12px system-ui">FIXTURE TRANSPORT · ${scenario} · 无正式数据库 / Agent · 写入禁用</div>`);
      res.end(content);
    } catch (error) {res.writeHead(500); res.end(String(error));}
  });
  return {server, metrics, tasks, workspace, runDetails:extension?.runDetails};
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const scenarioArg = process.argv.indexOf('--scenario');
  const scenario = scenarioArg < 0 ? 'workbench' : process.argv[scenarioArg + 1];
  if (!scenarios.includes(scenario)) throw new Error('Unknown scenario: ' + scenario);
  const portArg = process.argv.indexOf('--port');
  const port = portArg < 0 ? 0 : Number(process.argv[portArg + 1]);
  const {server} = createFixtureServer({workbench: true, scenario});
  server.listen(port, '127.0.0.1', () => console.log(`Web fixture: http://127.0.0.1:${server.address().port}`));
}
