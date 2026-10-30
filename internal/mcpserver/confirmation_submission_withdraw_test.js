'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs');
const {createCardInstance,renderedText}=require('./confirmation_card_instance_mount_test.js');
const fixture=JSON.parse(fs.readFileSync(`${__dirname}/testdata/confirmation_rejected_retry_v5.json`,'utf8'));
const clone=v=>JSON.parse(JSON.stringify(v));
async function mount({withdrawFailure=false,readFailure=false,snapshot=null}={}){
 const pending=clone(fixture.pending.entry),options=clone(fixture.pending.options.entry),calls=[];let held=snapshot?.pendingRequest,instance;
 const answer=async post=>{
  if(post.method==='ui/initialize')return{result:{}};
  if(post.method!=='tools/call')return{result:{}};
  const {name,arguments:args}=post.params;calls.push({name,args:clone(args)});
  if(name==='confirmation_options_get')return{result:{structuredContent:clone(options)}};
  if(name==='confirmation_commit'){held=clone(args);return{error:{code:-32603,message:'transport failed'}};}
  if(name==='confirmation_get')return readFailure&&held?{error:{code:-32603,message:'read failed'}}:{result:{structuredContent:clone(pending)}};
  if(name==='confirmation_submission_withdraw'){
   assert.deepEqual(clone(args),held,'withdrawal must fence the complete original payload');
   if(withdrawFailure)return{error:{code:-32603,message:'withdraw response lost'}};
   const result=clone(fixture.rejection.entry),sub=result.intent.submission;
   sub.request=clone(args);sub.payloadSHA256=instance.peek('pendingHash');
   sub.failure.code='CONFIRMATION_SUBMISSION_WITHDRAWN';sub.failure.message='Original request sealed without consumption.';
   delete result.failure;
   return{result:{structuredContent:result}};
  }
  assert.fail(name);
 };
 instance=createCardInstance('withdrawal',answer);
 if(snapshot)instance.peek(`window.openai.widgetState=${JSON.stringify(snapshot)}`);
 const i=pending.intent;instance.peek(`Date.now=()=>${Date.parse(i.createdAt)+1000}`);
 await instance.pushToolInput({aiSessionId:i.aiSessionId,intentId:i.intentId,version:i.version,purpose:i.purpose,contentHash:i.contentHash});
 await instance.pushToolResult(pending);
 if(!snapshot){const send=instance.peek('commit()');await instance.settle();await send;}
 assert.equal(instance.peek('unknown'),true);assert.equal(calls.filter(v=>v.name==='confirmation_submission_withdraw').length,0);
 return{instance,calls};
}
test('only explicit withdrawal seals an uncertain request, then permits a fresh user confirmation',async()=>{
 const {instance,calls}=await mount();
 assert.ok(renderedText(instance.peek("document.getElementById('root')")).join(' ').includes('核对并撤回原请求'));
 const action=instance.peek('withdrawUnknownSubmission()');await instance.settle();await action;
 assert.equal(calls.filter(v=>v.name==='confirmation_commit').length,1);
 assert.equal(calls.filter(v=>v.name==='confirmation_submission_withdraw').length,1);
 assert.equal(instance.peek('unknown'),false,JSON.stringify(instance.peek('technicalFacts()')));assert.equal(instance.peek('pendingCommit'),null);assert.equal(instance.peek('editableRetry'),true);
 assert.match(instance.peek('notice'),/已封存且未消费/);
});
test('failed original read never sends withdrawal or unlocks the confirmation',async()=>{
 const {instance,calls}=await mount({readFailure:true});const action=instance.peek('withdrawUnknownSubmission()');await instance.settle();await action;
 assert.equal(calls.filter(v=>v.name==='confirmation_submission_withdraw').length,0);assert.equal(instance.peek('unknown'),true);assert.equal(instance.peek('editableRetry'),false);
});
test('lost withdrawal response retains the exact unknown request without automatically confirming',async()=>{
 const {instance,calls}=await mount({withdrawFailure:true}),request=clone(instance.peek('pendingCommit'));
 const action=instance.peek('withdrawUnknownSubmission()');await instance.settle();await action;
 assert.equal(instance.peek('unknown'),true);assert.deepEqual(clone(instance.peek('pendingCommit')),request);assert.equal(calls.filter(v=>v.name==='confirmation_commit').length,1);
});

for(const version of ['PURPOSE_CONFIRMATION_V13','PURPOSE_CONFIRMATION_V12','PURPOSE_CONFIRMATION_V11'])test(`${version} unknown snapshot restores its complete request without sending it and supports explicit fencing`,async()=>{
 const old=await mount(),snapshot=clone(old.instance.widgetStates.at(-1));snapshot.componentVersion=version;
 const {instance,calls}=await mount({snapshot});assert.deepEqual(clone(instance.peek('pendingCommit')),snapshot.pendingRequest);
 assert.equal(calls.some(v=>v.name==='confirmation_commit'||v.name==='confirmation_submission_withdraw'),false);
 const action=instance.peek('withdrawUnknownSubmission()');await instance.settle();await action;
 assert.equal(instance.peek('unknown'),false);assert.equal(instance.peek('editableRetry'),true);assert.equal(calls.some(v=>v.name==='confirmation_commit'),false);
});
