'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {boot,settle}=require('./dev_bridge_ui_test.js');
const {fixture,branches}=require('./testdata/card_visual_fixture.cjs');

function binding(v,display='EXPANDED'){return {presentationMode:'WORKSPACE',aiSessionId:'session',projectId:'project',featureAreaId:v.selectedFeatureAreaId,taskId:v.selectedTaskId,presentationRequestId:'readonly-host',entryIdentity:'readonly-host',display};}
async function mount(capabilities={}){const v=fixture('REVIEW_APPROVAL_REQUIRED'),h=boot({manualIdentity:true,hostCapabilities:capabilities});h.init();const id=binding(v);h.result(id);await settle();h.reply(h.calls()[0],v,{_meta:{decisionStatus:'PENDING'}});h.reply(h.calls()[1],{result:{branches}});await settle();return h;}
function writes(h){return h.calls().filter(c=>!['dev_bridge_card_get','workspace_get'].includes(c.params.name));}

test('host capability declarations do not grant Card mutation authority',async()=>{
 for(const caps of [{},{messageText:true,toolsCall:true,setWidgetState:true},{extensions:{messageText:true,toolsCall:true}}]){const h=await mount(caps);assert.deepEqual(writes(h),[]);assert.equal(h.root.querySelectorAll('button').some(b=>/确认|拒绝|批准|允许本次/.test(b.textContent)),false);h.dispose();}
});

test('host diagnostics remain bounded to settings and cause no RPC',async()=>{
 const h=await mount({messageText:true,toolsCall:true});const before=h.calls().length;const settings=h.root.querySelectorAll('details').find(n=>n.querySelector('summary')?.textContent==='设置');if(settings){settings.open=true;settings.ontoggle?.();}await settle();assert.equal(h.calls().length,before);assert.doesNotMatch(h.root.textContent,/dev_bridge_review_wakeup_claim|recordWakeDiagnostic/);h.dispose();
});
