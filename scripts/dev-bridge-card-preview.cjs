#!/usr/bin/env node
'use strict';
// Isolated fixture transport. Serves the exact production component, never a business API.
const fs=require('node:fs'),path=require('node:path'),http=require('node:http');
const {fixture,branches}=require('../internal/mcpserver/testdata/card_visual_fixture.cjs');
const componentPath=path.resolve(__dirname,'../internal/mcpserver/dev_bridge_ui.html');
function scenarios(){
 const make=(state,display)=>{const workspace=fixture(state);return{display,workspace};};
 return{
  'collapsed-idle':make('FEATURE_AREA_IDLE','COLLAPSED'),
  'collapsed-approval':make('REVIEW_APPROVAL_REQUIRED','COLLAPSED'),
  'expanded-running':make('IMPLEMENTATION_RUNNING','EXPANDED'),
  'expanded-approval':make('REVIEW_APPROVAL_REQUIRED','EXPANDED'),
  'refresh-failure':{...make('REVIEW_APPROVAL_REQUIRED','EXPANDED'),failRefresh:true},
  'invalid-session':{...make('FEATURE_AREA_IDLE','COLLAPSED'),invalidSession:true},
 };
}
function fixtureHostScript(){return `
'use strict';
const scenes=${JSON.stringify(scenarios())},branches=${JSON.stringify(branches)};
let selected='collapsed-idle',currentFrame=null,readCount=0,shownInput=false;
const scenePicker=document.getElementById('scene'),log=document.getElementById('transport-log'),frameHost=document.getElementById('frame-host');
for(const name of Object.keys(scenes)){const option=document.createElement('option');option.value=name;option.textContent=name;scenePicker.append(option);}
function record(text){log.textContent=(text+'\\n'+log.textContent).slice(0,5000);}
function mount(){selected=scenePicker.value;readCount=0;shownInput=false;log.textContent='FIXTURE TRANSPORT — no Core, runtime, SQLite, or Coding Agent connection.';const frame=document.createElement('iframe');frame.title='Production Dev Bridge component with fixture transport';frame.src='/component';frame.style.height='44px';frameHost.replaceChildren(frame);currentFrame=frame;}
scenePicker.onchange=mount;document.getElementById('remount').onclick=mount;
function send(message){currentFrame.contentWindow.postMessage({jsonrpc:'2.0',...message},location.origin);}
function binding(){const scene=scenes[selected],workspace=scene.workspace;return{presentationMode:'WORKSPACE',aiSessionId:scene.invalidSession?'invalid-fixture-session':'fixture-session',projectId:workspace.developmentContext.project.id,featureAreaId:workspace.selectedFeatureAreaId||'',taskId:workspace.selectedTaskId||'',presentationRequestId:'fixture-'+selected,workspaceIdentity:'fixture-workspace',entryIdentity:'fixture-entry-'+selected,display:scene.display};}
window.addEventListener('message',event=>{
 if(event.source!==currentFrame?.contentWindow||event.origin!==location.origin)return;const message=event.data;if(message?.jsonrpc!=='2.0')return;const scene=scenes[selected];
 const reply=result=>send({id:message.id,result}),fail=text=>send({id:message.id,error:{code:-32000,message:text}});
 if(message.method==='ui/initialize'){reply({hostCapabilities:{}});if(!shownInput){shownInput=true;const snapshot=structuredClone(scene.workspace);snapshot.refreshedAt=new Date().toISOString();send({method:'ui/notifications/tool-input',params:{arguments:binding()}});send({method:'ui/notifications/tool-result',params:{structuredContent:binding(),_meta:{workspace:snapshot}}});}return;}
 if(message.method==='ui/notifications/size-changed'){currentFrame.style.height=Math.max(40,Math.ceil(message.params.height))+'px';return;}
 if(message.method==='tools/call'){
  const name=message.params.name,args=message.params.arguments;record(name+' '+JSON.stringify(args));
  if(name==='dev_bridge_card_get'){
   readCount++;if(scene.invalidSession)return fail('FIXTURE: Session does not exist.');if(scene.failRefresh)return fail('FIXTURE: authoritative refresh unavailable. Retry is safe.');
   if(args.aiSessionId!=='fixture-session'||args.projectId&&args.projectId!==scene.workspace.developmentContext.project.id)return fail('FIXTURE: binding conflict.');
   if(args.featureAreaId!==(scene.workspace.selectedFeatureAreaId||'')||args.taskId!==(scene.workspace.selectedTaskId||''))return fail('FIXTURE: this scene has only its exact target; choose a different scene to navigate.');
   setTimeout(()=>reply({structuredContent:structuredClone(scene.workspace)}),300);return;
  }
  if(name==='workspace_get'){setTimeout(()=>reply({structuredContent:{result:{branches}}}),200);return;}
  if(name==='dev_bridge_review_wakeup_claim'){reply({structuredContent:{status:'ALREADY_ATTEMPTED'}});return;}
  fail('FIXTURE: business mutations are disabled. This preview only validates component transport and interaction.');return;
 }
 if(message.id!=null){record(message.method+' (fixture acknowledges transport only)');reply({});}
});mount();
`;}
function hostHTML(){return `<!doctype html><html lang="zh"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Dev Bridge fixture preview</title><style>body{font:14px system-ui;background:#f3f5f8;color:#172033;margin:0;padding:24px}main{max-width:900px;margin:auto}h1{font-size:20px}.note{padding:12px;background:#fff6d5;border:1px solid #e9c970;border-radius:8px}label{display:block;margin:18px 0}select,button{font:inherit;padding:7px}iframe{width:100%;border:0;display:block;background:transparent}pre{white-space:pre-wrap;overflow-wrap:anywhere;font:12px monospace;max-height:200px;overflow:auto}#frame-host{margin:16px 0}</style><main><h1>Dev Bridge · Fixture preview</h1><p class="note">明确标识的隔离 fixture transport。复用未复制的生产 HTML；不连接正式服务、数据库或 Coding Agent。自动化通过不代表真实 ChatGPT 宿主验收。</p><label>场景 <select id="scene"></select> <button id="remount">重新挂载</button></label><p>点击轻量入口可原位展开。刷新失败场景先展示成功快照，点击刷新触发错误。操作按钮仅记录请求并返回 fixture 错误。</p><div id="frame-host"></div><details><summary>Fixture transport 请求记录</summary><pre id="transport-log"></pre></details></main><script src="/fixture-host.js"></script></html>`;}
function createPreviewServer(){return http.createServer((req,res)=>{
 const pathname=new URL(req.url,'http://127.0.0.1').pathname;
 if(req.method!=='GET'){res.writeHead(405);res.end('Read-only fixture preview');return;}
 let body,type;
 if(pathname==='/'){body=hostHTML();type='text/html; charset=utf-8';}
 else if(pathname==='/component'){body=fs.readFileSync(componentPath);type='text/html; charset=utf-8';}
 else if(pathname==='/fixture-host.js'){body=fixtureHostScript();type='text/javascript; charset=utf-8';}
 else{res.writeHead(404);res.end('Not found');return;}
 res.writeHead(200,{'Content-Type':type,'Cache-Control':'no-store','X-Content-Type-Options':'nosniff'});res.end(body);
});}
if(require.main===module){
 if(process.argv.includes('--check')){
  const assert=require('node:assert/strict'),vm=require('node:vm');assert.equal(Object.keys(scenarios()).length,6);new vm.Script(fixtureHostScript());assert.match(fs.readFileSync(componentPath,'utf8'),/function toggleExpanded/);console.log('Six fixture scenes and exact production component transport validated; no server started.');
 }else{
  const index=process.argv.indexOf('--port'),port=index<0?0:Number(process.argv[index+1]);if(!Number.isInteger(port)||port<0||port>65535)throw new Error('Expected --port 0..65535');
  const server=createPreviewServer();server.listen(port,'127.0.0.1',()=>console.log('FIXTURE ONLY: open http://127.0.0.1:'+server.address().port+' manually. Stop with Ctrl+C.'));
 }
}
module.exports={scenarios,hostHTML,fixtureHostScript,createPreviewServer,componentPath};
