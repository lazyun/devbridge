'use strict';
const {test}=require('node:test'),assert=require('node:assert/strict');
const {readFileSync}=require('node:fs');
const productionHTML=readFileSync(`${__dirname}/ui/index.html`,'utf8');
async function fixture(t,scenario='workbench') {
  const {createFixtureServer}=await import('./testdata/task_owned_visual_host.mjs');
  const f=createFixtureServer({workbench:true,scenario});
  // Exercise the real HTTP handler without a listening socket (sandbox portable).
  f.fetch=async(path,options={})=>{
    let status=200,body=''; const headers={};
    const req={url:path,method:options.method||'GET'};
    const res={setHeader:(k,v)=>headers[k]=v,writeHead:(code,h={})=>{status=code;Object.assign(headers,h);},end:value=>{body=String(value||'');}};
    await f.server.listeners('request')[0](req,res);
    return {status,text:async()=>body,json:async()=>JSON.parse(body)};
  };
  f.get=async path=>{const response=await f.fetch(path);return {status:response.status,body:await response.json()};};
  return f;
}
test('isolated preview serves production modules and distinguishes current Task, Run transport, acceptance and Git',async t=>{
  const f=await fixture(t);
  const page=await (await f.fetch('/ui/')).text();
  assert.match(page,/FIXTURE TRANSPORT/);
  for(const [,name] of productionHTML.matchAll(/<script src="([^"]+)"/g)) {
    const response=await f.fetch('/ui/'+name);assert.equal(response.status,200,name);
    assert.equal(await response.text(),readFileSync(`${__dirname}/ui/${name}`,'utf8'),name);
  }
  const {body:w}=await f.get('/ui/api/project-workspace?project_id=project');
  assert.equal(w.tasks.length,5);
  assert.ok(w.tasks.some(t=>t.phase==='REVIEW_APPROVAL_PENDING'));
  assert.ok(w.tasks.some(t=>t.phase==='ACCEPTANCE_PENDING'));
  const failed=w.tasks.find(t=>t.id==='task-review-failed');assert.equal(failed.status,'ACTIVE');assert.equal(failed.phase,'REVIEWING');
  assert.equal(failed.presentation.taskView.currentReview.run.transportStatus,'INFRASTRUCTURE_FAILED');
  const multi=w.tasks.find(t=>t.id==='task-2').presentation.taskView;
  const current=multi.executionHistory.find(h=>h.current);assert.equal(current.execution.id,multi.currentExecution.id);assert.equal(current.implementationRun,undefined);
  const git=w.tasks.find(t=>t.id==='task-accepted-git-failed');assert.equal(git.status,'SUCCEEDED');assert.equal(git.commitActivity,'FAILED');
  const read=await f.get('/v1/runs/run-verification-only?project_id=project&task_id=task-2');assert.equal(read.status,200);assert.equal(read.body.run.status,'SUCCEEDED');assert.equal(read.body.verification_report.verdict,'FAIL');
  assert.equal((await f.get('/v1/runs/run-verification-only?project_id=project&task_id=task-1')).status,404);
  assert.equal((await f.get('/v1/runs/absent?project_id=project')).status,404);
  const mutation=await f.fetch('/ui/api/tasks/task-2/action',{method:'POST',body:'{}'});assert.equal(mutation.status,405);assert.equal(f.metrics.mutations.length,0);
});
test('preview exposes bounded task/run pages and separate read failure, partial history, empty state',async t=>{
  const f=await fixture(t),taskPage=await f.get('/ui/api/work-tabs/tasks?project_id=project&limit=1&offset=1');
  assert.equal(taskPage.body.items.length,1);assert.equal(taskPage.body.total,5);
  const runPage=await f.get('/ui/api/tasks/task-2/runs?project_id=project&limit=1&offset=1');assert.equal(runPage.body.items.length,1);assert.ok(runPage.body.total>1);
  const empty=await fixture(t,'empty');assert.equal((await empty.get('/ui/api/project-workspace?project_id=project')).body.tasks.length,0);
  const error=await fixture(t,'read-error');assert.equal((await error.get('/ui/api/project-workspace?project_id=project')).status,503);
  const partial=await fixture(t,'partial-history');assert.equal((await partial.get('/ui/api/tasks/task-2?project_id=project')).status,200);assert.equal((await partial.get('/ui/api/tasks/task-2/runs?project_id=project')).status,503);
});
