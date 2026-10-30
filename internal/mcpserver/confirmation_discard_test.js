'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs');
const {createCardInstance}=require('./confirmation_card_instance_mount_test.js');
const fixture=JSON.parse(fs.readFileSync(`${__dirname}/testdata/confirmation_rejected_retry_v5.json`,'utf8'));
const clone=v=>JSON.parse(JSON.stringify(v));
async function proposal(){
 const envelope=clone(fixture.pending.entry),options=clone(fixture.pending.options.entry),calls=[];
 const instance=createCardInstance('discard',async post=>{
  if(post.method!=='tools/call')return{result:{}};
  const {name,arguments:args}=post.params;calls.push({name,args:clone(args)});
  if(name==='confirmation_get')return{result:{structuredContent:envelope}};
  if(name==='confirmation_options_get')return{result:{structuredContent:options}};
  assert.equal(name,'confirmation_discard');
  const result=clone(envelope);result.intent.state='SUPERSEDED';
  return{result:{structuredContent:result}};
 });
 const i=envelope.intent;instance.peek(`Date.now=()=>${Date.parse(i.createdAt)+1000}`);
 await instance.pushToolInput({aiSessionId:i.aiSessionId,intentId:i.intentId,version:i.version,purpose:i.purpose,contentHash:i.contentHash});
 await instance.pushToolResult(envelope);
 return{instance,calls,i};
}
test('discarding a proposal sends its exact identity once and never confirms or starts it',async()=>{
 const {instance,calls,i}=await proposal();assert.equal(calls.some(v=>v.name==='confirmation_discard'),false);
 const action=instance.peek('discardProposal()');await instance.settle();await action;
 const writes=calls.filter(v=>v.name==='confirmation_discard');assert.equal(writes.length,1);
 assert.deepEqual(writes[0].args,{aiSessionId:i.aiSessionId,intentId:i.intentId,version:i.version,purpose:i.purpose,contentHash:i.contentHash});
 assert.equal(instance.peek('intent.state'),'SUPERSEDED');assert.match(instance.peek('notice'),/此未提交方案已放弃/);
 await instance.peek('discardProposal()');assert.equal(calls.filter(v=>v.name==='confirmation_discard').length,1);
});
test('history and uncertain proposals cannot use the discard path',async()=>{
 for(const flag of ['window.devBridgeHistoryReadOnly=true','unknown=true','fatal=true']){
  const {instance,calls}=await proposal();instance.peek(flag);await instance.peek('discardProposal()');
  assert.equal(calls.some(v=>v.name==='confirmation_discard'),false,flag);
 }
});
