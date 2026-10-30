'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {boot,settle}=require('./dev_bridge_ui_test.js');
const {fixture,branches}=require('./testdata/card_visual_fixture.cjs');
function bind(v){return {presentationMode:'WORKSPACE',aiSessionId:'session',projectId:'project',featureAreaId:v.selectedFeatureAreaId,taskId:v.selectedTaskId,presentationRequestId:'readonly-task',entryIdentity:'readonly-task',display:'EXPANDED'};}
async function mount(v=fixture('REVIEW_APPROVAL_REQUIRED')){const h=boot({manualIdentity:true});h.init();h.result(bind(v));await settle();h.reply(h.calls()[0],v);h.reply(h.calls()[1],{result:{branches}});await settle();return h;}
test('Task projection is canonical, compact and read-only',async()=>{const h=await mount();assert.match(h.root.textContent,/审查已完成，等待审批/);assert.equal(h.root.querySelectorAll('select').some(s=>/Coding Agent model|Reasoning effort/.test(s.attributes['aria-label']||'')),false);assert.equal(h.calls().some(c=>c.params.name==='decision_respond'),false);h.dispose();});
test('an unavailable exact Task target never falls back to a fake Task or action',async()=>{const v=fixture('REVIEW_APPROVAL_REQUIRED');v.selectedTaskId='';v.presentation=null;v.selectedWork=null;const h=await mount(v);assert.equal(h.root.querySelectorAll('button').some(b=>/确认|拒绝|批准/.test(b.textContent)),false);assert.match(h.root.textContent,/准确目标暂不可用/);h.dispose();});
test('completed native Review asks GPT for analysis without offering approval',async()=>{
 const v=fixture('REVIEW_RUNNING'),p=v.presentation;
 p.workflowGuidance={...p.workflowGuidance,runId:'review-run',nextAction:{code:'ANALYZE_RUN_RESULT'}};
 p.taskView.phaseDescription='Codex 已完成，等待 GPT 分析';
 const h=await mount(v);
 assert.match(h.root.textContent,/等待 GPT 分析/);
 assert.ok(h.root.querySelectorAll('button').some(b=>b.textContent==='让 GPT 分析执行结果'));
 assert.equal(h.root.querySelectorAll('button').some(b=>/审批确认|批准并/.test(b.textContent)),false);
 assert.equal(h.calls().some(c=>c.params.name==='run_analysis_submit'||c.params.name==='execution_start'),false);
 h.dispose();
});
