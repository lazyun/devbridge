'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {boot,settle}=require('./dev_bridge_ui_test.js');
const {fixture,branches}=require('./testdata/card_visual_fixture.cjs');
function bind(v){return {presentationMode:'WORKSPACE',aiSessionId:'session',projectId:'project',featureAreaId:v.selectedFeatureAreaId,taskId:v.selectedTaskId,presentationRequestId:'readonly-review',entryIdentity:'readonly-review',display:'EXPANDED'};}
async function mount(v){const h=boot({manualIdentity:true});h.init();h.result(bind(v));await settle();h.reply(h.calls()[0],v);h.reply(h.calls()[1],{result:{branches}});await settle();return h;}
test('review approval is presented as an instruction to continue in chat, without automatic wakeup',async()=>{const h=await mount(fixture('REVIEW_APPROVAL_REQUIRED'));assert.match(h.root.textContent,/请在对话中明确决定/);assert.equal(h.posts.some(p=>p.method==='ui/message'),false);assert.equal(h.calls().some(c=>/wakeup|claim|decision_respond/.test(c.params.name)),false);h.dispose();});
test('refresh never converts a durable review state into a Card-side side effect',async()=>{const v=fixture('REVIEW_APPROVAL_REQUIRED'),h=await mount(v),before=h.calls().length;h.evaluate('refreshCard()');await settle();assert.equal(h.calls().slice(before).every(c=>c.params.name==='dev_bridge_card_get'||c.params.name==='workspace_get'),true);h.dispose();});
