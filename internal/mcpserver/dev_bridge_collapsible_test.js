'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {boot,settle}=require('./dev_bridge_ui_test.js');
const {fixture,branches}=require('./testdata/card_visual_fixture.cjs');
function bind(v,d='EXPANDED'){return {presentationMode:'WORKSPACE',aiSessionId:'session',projectId:'project',featureAreaId:v.selectedFeatureAreaId,taskId:v.selectedTaskId,presentationRequestId:'readonly-collapse',entryIdentity:'readonly-collapse',display:d};}
async function mount(d='EXPANDED'){const v=fixture('REVIEW_APPROVAL_REQUIRED'),h=boot({manualIdentity:true});h.init();h.result(bind(v,d));await settle();if(d==='COLLAPSED')return h;h.reply(h.calls()[0],v);h.reply(h.calls()[1],{result:{branches}});await settle();return h;}
test('collapsed entry performs only passive reads and exposes no action',async()=>{const h=await mount('COLLAPSED');assert.equal(h.calls().every(c=>['dev_bridge_card_get','workspace_get'].includes(c.params.name)),true);assert.equal(h.root.querySelectorAll('button').some(b=>/确认|拒绝|批准/.test(b.textContent)),false);h.dispose();});
test('expanded Card reads the exact bound projection and never exposes mutation controls',async()=>{const h=await mount();assert.equal(h.calls().every(c=>['dev_bridge_card_get','workspace_get'].includes(c.params.name)),true);assert.equal(h.root.querySelectorAll('button').some(b=>/确认|拒绝|批准|允许本次/.test(b.textContent)),false);assert.match(h.root.textContent,/只读浏览/);h.dispose();});
