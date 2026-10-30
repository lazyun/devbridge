'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {boot,settle}=require('./dev_bridge_ui_test.js');
const {fixture,branches}=require('./testdata/card_visual_fixture.cjs');
function bind(v){return {presentationMode:'WORKSPACE',aiSessionId:'session',projectId:'project',featureAreaId:v.selectedFeatureAreaId,taskId:v.selectedTaskId,presentationRequestId:'readonly-scope',entryIdentity:'readonly-scope',display:'EXPANDED'};}
async function mount(v){const h=boot({manualIdentity:true});h.init();h.result(bind(v));await settle();h.reply(h.calls()[0],v);h.reply(h.calls()[1],{result:{branches}});await settle();return h;}
test('foreign pending decisions never become a Card mutation target',async()=>{const v=fixture('REVIEW_APPROVAL_REQUIRED');v.pendingDecisions=[{kind:'REVIEW_APPROVAL',taskId:'foreign',featureAreaId:'other',decision:{id:'foreign'}}];const h=await mount(v);assert.equal(h.root.querySelectorAll('button').some(b=>/确认|拒绝|批准/.test(b.textContent)),false);assert.equal(h.calls().some(c=>c.params.name==='decision_respond'),false);h.dispose();});
test('canonical current projection remains read-only even when it includes a pending decision',async()=>{const v=fixture('REVIEW_APPROVAL_REQUIRED'),h=await mount(v);assert.match(h.root.textContent,/审查已完成，等待审批/);assert.equal(h.calls().every(c=>['dev_bridge_card_get','workspace_get'].includes(c.params.name)),true);h.dispose();});
