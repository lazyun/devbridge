'use strict';
// Static composition adapter. The formal renderer below is the unchanged
// production confirmation main script, with a private DOM and RPC channel per
// immutable intent. No eval, nested iframe, or generic business dispatcher.
window.DevBridgeConfirmationStack=Object.freeze({mount(host,reference,hooks={}){
 const realWindow=window,realDocument=document,realParent=parent;
 const fixed=Object.freeze({...reference});
 const exact=args=>args&&['aiSessionId','intentId','version','purpose','contentHash'].every(k=>args[k]===fixed[k]);
 if(!host?.attachShadow||!fixed.intentId||!fixed.contentHash)throw new Error('Formal Card identity is unavailable');
 const shadow=host.attachShadow({mode:'open'}),style=realDocument.createElement('style'),body=realDocument.createElement('div');
 style.textContent=__CONFIRMATION_CSS__;
 body.className='dbc-surface';body.innerHTML=__CONFIRMATION_BODY__;shadow.append(style,body);
 const listeners=new Map(),pending=new Map(),prefix='db-confirmation-'+crypto.randomUUID()+':';let closed=false;
 const emit=data=>{if(!closed)for(const fn of [...(listeners.get('message')||[])])fn({source:virtualParent,data});};
 const add=(name,fn)=>{if(!listeners.has(name))listeners.set(name,new Set());listeners.get(name).add(fn);};
 const remove=(name,fn)=>listeners.get(name)?.delete(fn);
 const virtualParent={postMessage(message){
  if(closed)return;
  if(message.method==='ui/initialize'){queueMicrotask(()=>emit({jsonrpc:'2.0',id:message.id,result:{}}));return;}
  if(message.method==='ui/notifications/initialized'||message.method==='ui/notifications/size-changed')return;
  const name=message.params?.name,args=message.params?.arguments;
  if(message.method!=='tools/call'||!['confirmation_get','confirmation_options_get','confirmation_commit','confirmation_submission_withdraw','confirmation_discard'].includes(name)||!exact(args)){
   if(message.id!==undefined)queueMicrotask(()=>emit({jsonrpc:'2.0',id:message.id,error:{code:-32602,message:'Exact formal confirmation required'}}));return;
  }
  if(['confirmation_commit','confirmation_submission_withdraw','confirmation_discard'].includes(name)&&(hooks.readOnly===true||hooks.canInteract?.()===false)){
   if(message.id!==undefined)queueMicrotask(()=>emit({jsonrpc:'2.0',id:message.id,error:{code:-32602,message:'Historical operations are read-only'}}));return;
  }
  const id=prefix+String(message.id);pending.set(id,{id:message.id,name});
  realParent.postMessage({...message,id},'*');
 }};
 const receive=event=>{
  if(closed||event.source!==realParent||!event.data||event.data.jsonrpc!=='2.0')return;
  const original=pending.get(event.data.id);if(!original)return;pending.delete(event.data.id);
  emit({...event.data,id:original.id});
  if(original.name==='confirmation_commit')queueMicrotask(()=>hooks.onChange?.({reference:fixed,result:event.data.result,error:event.data.error}));
 };
 realWindow.addEventListener('message',receive);
 const scopedDocument=new Proxy(realDocument,{get(target,key){
  if(key==='body'||key==='documentElement')return body;
  if(key==='activeElement')return shadow.activeElement;
  if(key==='currentScript')return {textContent:__CONFIRMATION_SOURCE__};
  if(key==='getElementById')return id=>shadow.querySelector('#'+CSS.escape(id));
  if(key==='querySelector'||key==='querySelectorAll')return shadow[key].bind(shadow);
  const value=Reflect.get(target,key,target);return typeof value==='function'?value.bind(target):value;
 }});
 const localOpenAI={get widgetState(){return hooks.getState?.();},setWidgetState:value=>hooks.saveState?.(value)};
 const scopedWindow=new Proxy(realWindow,{get(target,key){
  if(key==='devBridgeHistoryReadOnly')return hooks.readOnly===true;
  if(key==='devBridgeTaskPanel')return true;
  if(key==='devBridgeKeepObjectName')return hooks.keepObjectName===true;
  if(key==='openai')return localOpenAI;
  const value=Reflect.get(target,key,target);return typeof value==='function'?value.bind(target):value;
 }});
 function run(document,window,parent,addEventListener,removeEventListener){
 __CONFIRMATION_MAIN__
 }
 run(scopedDocument,scopedWindow,virtualParent,add,remove);
 emit({jsonrpc:'2.0',method:'ui/notifications/tool-input',params:{arguments:fixed}});
 // Read through the formal renderer's own host protocol; no synthetic intent or
 // inferred eligibility is supplied by the list. It verifies its exact identity.
 const bootstrapId=prefix+'bootstrap';pending.set(bootstrapId,{id:'bootstrap',name:'bootstrap'});
 const bootstrap=event=>{
  if(closed||event.source!==realParent||event.data?.id!==bootstrapId)return;
  realWindow.removeEventListener('message',bootstrap);pending.delete(bootstrapId);
  if(event.data.error){body.textContent='正式确认暂时不可读取，请刷新操作列表。';return;}
  emit({jsonrpc:'2.0',method:'ui/notifications/tool-result',params:event.data.result});
 };
 // Bootstrap has its own receiver, not a formal RPC pending entry.
 pending.delete(bootstrapId);realWindow.addEventListener('message',bootstrap);
 realParent.postMessage({jsonrpc:'2.0',id:bootstrapId,method:'tools/call',params:{name:'confirmation_get',arguments:fixed}},'*');
 return Object.freeze({dispose(){if(closed)return;emit({jsonrpc:'2.0',id:'dispose',method:'ui/resource-teardown',params:{}});closed=true;realWindow.removeEventListener('message',receive);realWindow.removeEventListener('message',bootstrap);pending.clear();listeners.clear();}});
}});
