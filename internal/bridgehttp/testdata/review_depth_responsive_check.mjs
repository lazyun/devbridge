// Isolated loopback/frozen DTO test. Chromium always uses a fresh temporary profile.
import assert from 'node:assert/strict';
import {createRequire} from 'node:module';
import {mkdir,writeFile} from 'node:fs/promises';
import {resolve} from 'node:path';
import {createFixtureServer} from './task_owned_visual_host.mjs';
const require=createRequire(import.meta.url),{chromium}=require('playwright');
const output=resolve(process.env.WEBUI_SCREENSHOT_DIR||'/private/tmp/review-depth-responsive');
await mkdir(output,{recursive:true});
const browser=await chromium.launch({headless:true,...(process.env.CHROME_EXECUTABLE?{executablePath:process.env.CHROME_EXECUTABLE}:{})});
const results=[];
try {
 for(const width of [320,560,850,1440]) for(const colorScheme of ['light','dark']) {
  const {server,tasks,metrics}=createFixtureServer();
  for(const [index,floor] of ['FAST','DEEP',null].entries()) {
   const owner=tasks[index],view=owner.presentation.taskView;
   view.phase=owner.phase=floor?'REVIEW_REQUIRED':'REVIEW_APPROVAL_PENDING';
   view.requiredReviewDepth=floor;view.allowedReviewDepths=floor==='FAST'?['FAST','DEEP']:floor?['DEEP']:[];
   view.phaseLabel='Review';view.phaseDescription=floor==='DEEP'?'需要深度审查':floor?'需要审查':'等待审批';
   view.currentWorkKind='REVIEW';view.currentExecution=null;view.executionHistory=[];
   view.reviewHistory=[{id:'fast-'+index,taskId:owner.id,ordinal:1,reviewDepth:'FAST',reviewDisposition:'FURTHER_REVIEW_REQUIRED',status:'SUCCEEDED',run:{id:'run-fast-'+index,transportStatus:'SUCCEEDED'},instruction:'Review instruction must not replace semantic title'}, {id:'deep-'+index,taskId:owner.id,ordinal:2,reviewDepth:'DEEP',reviewDisposition:floor?'FURTHER_REVIEW_REQUIRED':'APPROVAL_READY',status:'SUCCEEDED',run:{id:'run-deep-'+index,transportStatus:'SUCCEEDED'}}];
   view.currentReview=floor==='FAST'?null:view.reviewHistory[1];
   view.actions=(floor?['START_TASK_REVIEW']:['APPROVE','REVISE','REJECT']).map(action=>({action,available:true,target:{featureAreaId:'area',taskId:owner.id,reviewId:view.reviewHistory[1].id}}));
   view.currentAction={code:floor?'START_TASK_REVIEW':'RESOLVE_REVIEW_APPROVAL',message:view.phaseDescription};
  }
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
  const url=`http://127.0.0.1:${server.address().port}`;
  const context=await browser.newContext({viewport:{width,height:1000},colorScheme,locale:'zh-CN'});
  const errors=[],blocked=[];
  await context.route('**/*',route=>route.request().url().startsWith(url+'/')?route.continue():(blocked.push(route.request().url()),route.abort()));
  await context.addInitScript(()=>{localStorage.setItem('devbridge.console.navigation','projects');localStorage.setItem('devbridge.console.projectTab','tasks');});
  const page=await context.newPage();page.on('pageerror',e=>errors.push(e.message));
  try {
   await page.goto(url);
   for(const [index,owner] of tasks.entries()) {
    await page.locator(`#page [data-task-detail="${owner.id}"]`).click();
    await page.locator(`.task-dashboard[data-task-id="${owner.id}"]`).waitFor();
    const detail=page.locator('#detail-body');
    assert.equal(await page.locator('#detail-title').textContent(),owner.title);
    assert.equal(await detail.locator('button[value="FAST"]').count(),index===0?1:0);
    assert.equal(await detail.locator('button[value="DEEP"]').count(),index<2?1:0);
    assert.equal(await detail.locator('[data-task-action="APPROVE"]').count(),index===2?1:0);
    if(index===1) {assert.ok((await detail.textContent()).includes('需要深度审查'));assert.doesNotMatch(await detail.textContent(),/Failed|FAILED|审查失败/);}
    if(index===2) assert.ok((await detail.textContent()).includes('等待审批'));
    await detail.locator('[data-review-history] summary').click();
    assert.equal(await detail.locator('[data-review-history] [data-review-depth="FAST"]').count(),1);
    assert.equal(await detail.locator('[data-review-history] [data-review-depth="DEEP"]').count(),1);
    assert.equal(await detail.locator('[data-review-history] [data-task-action]').count(),0);
    for(const selector of ['body','#detail','#detail-body']) {
     const size=await page.locator(selector).evaluate(n=>({scroll:n.scrollWidth,client:n.clientWidth}));
     assert.ok(size.scroll<=size.client+1,`${width}/${colorScheme}/${index}/${selector} overflow ${JSON.stringify(size)}`);
    }
    await page.screenshot({path:`${output}/${width}-${colorScheme}-${index===0?'initial':index===1?'deep-required':'approval'}.png`,fullPage:true});
    await page.locator('#detail-close').click();
   }
   assert.deepEqual(errors,[]);assert.deepEqual(blocked,[]);assert.deepEqual(metrics.mutations,[]);
   results.push({width,colorScheme,states:3,errors,blocked,mutations:metrics.mutations.length});
  } finally {await context.close();await new Promise(resolve=>server.close(resolve));}
 }
} finally {await browser.close();}
await writeFile(`${output}/results.json`,JSON.stringify(results,null,2)+'\n');
console.log(`Passed ${results.length*3} depth states across ${results.length} viewport/theme combinations`);
