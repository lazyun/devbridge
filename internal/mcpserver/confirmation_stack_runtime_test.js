'use strict';
const test=require('node:test'),assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const source=fs.readFileSync(`${__dirname}/confirmation_stack_runtime.js`,'utf8');
function fixture(){
 const posts=[],handlers=new Set(),leaves=[],root={append(){},querySelector(){return null;}};
 const document={createElement:()=>({append(){},className:'',innerHTML:''})};
 const parent={postMessage:m=>posts.push(m)},window={leaves,addEventListener:(type,fn)=>handlers.add(fn),removeEventListener:(type,fn)=>handlers.delete(fn)};
 const context={window,document,parent,crypto:require('node:crypto').webcrypto,queueMicrotask,CSS:{escape:v=>v}};vm.createContext(context);
 const main="window.leaves.push(parent);addEventListener('message',event=>{window.lastMessage=event.data;});";
 const script=source.replaceAll('__CONFIRMATION_CSS__',"''").replaceAll('__CONFIRMATION_BODY__',"''").replaceAll('__CONFIRMATION_SOURCE__',"''").replace('__CONFIRMATION_MAIN__',main);
 vm.runInContext(script,context);
 const mount=(id,hooks={})=>{const reference={aiSessionId:'session',intentId:id,version:1,purpose:'EXECUTION',contentHash:'a'.repeat(64)};const card=window.DevBridgeConfirmationStack.mount({attachShadow:()=>root},reference,hooks);return {reference,card,leaf:leaves.at(-1)};};
 return{mount,posts,window,reply:(id,result)=>{for(const fn of [...handlers])fn({source:parent,data:{jsonrpc:'2.0',id,result}});}};
}
test('isolated formal children bootstrap by read only; same numeric RPC ids cannot collide',()=>{
 const h=fixture(),a=h.mount('intent-a'),b=h.mount('intent-b');assert.equal(h.posts.length,2);assert.ok(h.posts.every(m=>m.params.name==='confirmation_get'));
 for(const child of [a,b])child.leaf.postMessage({jsonrpc:'2.0',id:1,method:'tools/call',params:{name:'confirmation_options_get',arguments:child.reference}});
 assert.notEqual(h.posts[2].id,h.posts[3].id);assert.equal(h.posts[2].params.arguments.intentId,'intent-a');assert.equal(h.posts[3].params.arguments.intentId,'intent-b');
 a.card.dispose();b.card.dispose();
});
test('a formal child cannot change identity or invoke arbitrary tools',async()=>{
 const h=fixture(),a=h.mount('intent-a'),before=h.posts.length;
 for(const [name,args]of [['confirmation_commit',{...a.reference,intentId:'intent-b'}],['confirmation_commit',{...a.reference,contentHash:'b'.repeat(64)}],['execution_prepare',a.reference]])a.leaf.postMessage({jsonrpc:'2.0',id:2,method:'tools/call',params:{name,arguments:args}});
 await Promise.resolve();assert.equal(h.posts.length,before);
 a.leaf.postMessage({jsonrpc:'2.0',id:3,method:'tools/call',params:{name:'confirmation_commit',arguments:{...a.reference,action:'CONTINUE_TASK'}}});assert.equal(h.posts.length,before+1);assert.equal(h.posts.at(-1).params.name,'confirmation_commit');
 a.card.dispose();a.leaf.postMessage({jsonrpc:'2.0',id:4,method:'tools/call',params:{name:'confirmation_commit',arguments:a.reference}});assert.equal(h.posts.length,before+1);
});

test('history permits exact reads but blocks commits even from retained child handlers',async()=>{
 const h=fixture(),a=h.mount('pending-history',{readOnly:true});
 for(const name of ['confirmation_get','confirmation_options_get','confirmation_commit'])a.leaf.postMessage({jsonrpc:'2.0',id:name,method:'tools/call',params:{name,arguments:a.reference}});
 await Promise.resolve();assert.deepEqual(h.posts.map(p=>p.params.name),['confirmation_get','confirmation_get','confirmation_options_get']);
 assert.match(h.window.lastMessage.error.message,/read-only/);a.card.dispose();
});
test('a current card loses commit authority immediately when it moves out of next operations',()=>{
 let current=true;const h=fixture(),a=h.mount('current',{canInteract:()=>current});
 const commit=()=>a.leaf.postMessage({jsonrpc:'2.0',id:1,method:'tools/call',params:{name:'confirmation_commit',arguments:a.reference}});
 commit();assert.equal(h.posts.filter(p=>p.params.name==='confirmation_commit').length,1);
 current=false;commit();assert.equal(h.posts.filter(p=>p.params.name==='confirmation_commit').length,1);a.card.dispose();
});
test('only an actual commit response carries its exact reference and unmodified result into the panel continuation hook',async()=>{
 const h=fixture(),events=[],a=h.mount('review',{onChange:event=>events.push(event)});
 a.leaf.postMessage({jsonrpc:'2.0',id:5,method:'tools/call',params:{name:'confirmation_get',arguments:a.reference}});h.reply(h.posts.at(-1).id,{structuredContent:{intent:{state:'COMPLETED'}}});await Promise.resolve();assert.equal(events.length,0);
 a.leaf.postMessage({jsonrpc:'2.0',id:6,method:'tools/call',params:{name:'confirmation_commit',arguments:a.reference}});const result={structuredContent:{intent:{...a.reference,state:'COMPLETED'}}};h.reply(h.posts.at(-1).id,result);await Promise.resolve();assert.equal(events.length,1);assert.deepEqual(JSON.parse(JSON.stringify(events[0].reference)),a.reference);assert.equal(events[0].result,result);a.card.dispose();
});

test('withdrawal is exact, current-only and never triggers the confirmation continuation hook',async()=>{
 for(const blocked of [false,true]){
  const h=fixture(),events=[],a=h.mount('uncertain',{readOnly:blocked,onChange:event=>events.push(event)}),before=h.posts.length;
  a.leaf.postMessage({jsonrpc:'2.0',id:8,method:'tools/call',params:{name:'confirmation_submission_withdraw',arguments:a.reference}});
  await Promise.resolve();assert.equal(h.posts.length,before+(blocked?0:1));
  if(!blocked){h.reply(h.posts.at(-1).id,{structuredContent:{intent:{state:'PENDING'}}});await Promise.resolve();assert.equal(events.length,0);}
  a.card.dispose();
 }
});
