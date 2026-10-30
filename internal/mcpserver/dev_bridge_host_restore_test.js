'use strict';
const test=require('node:test'),assert=require('node:assert/strict');
const {boot,settle}=require('./dev_bridge_ui_test.js');
const {fixture}=require('./testdata/card_visual_fixture.cjs');
const input={aiSessionId:'session',projectId:'project',featureAreaId:'area-a',taskId:'task-a',presentationRequestId:'host-restore',display:'EXPANDED'};
const output={result:{...input,presentationMode:'WORKSPACE',entryIdentity:'entry-host-restore',singlePanel:true},context:{aiSessionId:'session',projectId:'project',featureAreaId:'area-a',taskId:'task-a'},presentationHint:{tool:'panel_show'}};
const meta=()=>({workspace:{...fixture('REVIEW_RUNNING'),refreshedAt:new Date().toISOString()},singlePanel:true});
test('restored ChatGPT initial globals hydrate without requiring another tool-result notification',async()=>{
 const h=boot({manualIdentity:true,hostContext:{displayMode:'pip',availableDisplayModes:['inline','pip']},openai:{toolInput:input,toolOutput:output,toolResponseMetadata:{status:'success',mcp_tool_result:{structuredContent:output,_meta:meta()}}}});
 h.init();await settle();assert.equal(h.evaluate('bindingReady'),true);assert.equal(h.evaluate('taskID'),'task-a');assert.ok(h.root.querySelectorAll('button').some(b=>b.id==='db-operation-tab-results'));
 assert.equal(h.calls().some(c=>/prepare|commit|submit|recover|start/.test(c.params.name)),false);h.dispose();
});
test('late ChatGPT globals hydrate once and never reset explicit navigation on replay',async()=>{
 const h=boot({manualIdentity:true,hostContext:{displayMode:'pip',availableDisplayModes:['inline','pip']}});h.init();await settle();
 const globals={toolInput:input,toolOutput:output,toolResponseMetadata:{call_tool_result:{structuredContent:output,_meta:meta()}}};Object.assign(h.context.window.openai,globals);h.event('openai:set_globals',{detail:{globals}});await settle();
 assert.equal(h.evaluate('bindingReady'),true);h.evaluate("operationTab='history';render()");const count=h.calls().length;
 h.event('openai:set_globals',{detail:{globals}});await settle();assert.equal(h.evaluate('operationTab'),'history');assert.equal(h.calls().length,count);h.dispose();
});
test('global saved UI state cannot supply missing canonical tool identity',async()=>{
 const h=boot({manualIdentity:true,openai:{widgetState:{aiSessionId:'session',projectId:'project',taskId:'task-a'},toolInput:input}});h.init();await settle();assert.equal(h.evaluate('bindingReady'),false);assert.equal(h.calls().length,0);h.dispose();
});

test('a restored canonical envelope cannot replace a different visible tool identity',async()=>{
 const foreign=structuredClone(output);foreign.result.taskId='foreign';
 const h=boot({manualIdentity:true,openai:{toolInput:input,toolOutput:foreign,toolResponseMetadata:{mcp_tool_result:{structuredContent:output,_meta:meta()}}}});h.init();await settle();
 assert.equal(h.evaluate('bindingReady'),false);assert.match(h.root.textContent,/身份冲突/);assert.equal(h.calls().length,0);h.dispose();
});
test('restored input still must agree with the authoritative output target',async()=>{
 const h=boot({manualIdentity:true,openai:{toolInput:{...input,taskId:'foreign'},toolOutput:output,toolResponseMetadata:{mcp_tool_result:{structuredContent:output,_meta:meta()}}}});h.init();await settle();
 assert.equal(h.evaluate('bindingReady'),false);assert.match(h.root.textContent,/冲突/);assert.equal(h.calls().length,0);h.dispose();
});
test('unavailable optional ChatGPT globals cannot break ordinary protocol initialization',async()=>{
 const h=boot({manualIdentity:true});Object.defineProperty(h.context.window.openai,'toolResponseMetadata',{get(){throw new Error('not ready');}});h.init();await settle();h.input(input);h.result(output,meta());await settle();
 assert.equal(h.evaluate('bindingReady'),true);h.dispose();
});
