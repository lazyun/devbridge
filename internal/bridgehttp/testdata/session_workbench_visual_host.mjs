// Read-only loopback preview of the shipped session UI. Synthetic observations;
// no Core database, Codex process, native history backend or business writes.
import {createFixtureServer} from './task_owned_visual_host.mjs';
import {pathToFileURL} from 'node:url';
import {resolve} from 'node:path';

export function createSessionPreview(){
 const fixture=createFixtureServer({workbench:true}),base=fixture.server.listeners('request')[0];fixture.server.removeAllListeners('request');
 const now=Date.now(),iso=ms=>new Date(now-ms).toISOString();
 const runs=[{id:'running',task_title:'重构 Progressive Discovery 为节点驱动流程',purpose:'IMPLEMENTATION',status:'RUNNING',thread_id:'fixture-thread',turn_id:'fixture-turn',pending_request_count:0},{id:'upper',task_title:'审查节点确认与历史迁移方案',purpose:'SCOPE_REVIEW',status:'RUNNING',executor_kind:'UPPER_LLM'},{id:'done',task_title:'统一 Core 与 pkg/flow 依赖边界',purpose:'VERIFICATION',status:'SUCCEEDED',thread_id:'history-thread',turn_id:'history-turn',analysis_state:'RECORDED',duration_ms:84200}].map(run=>({project_id:'project',task_id:'task-1',feature_area_id:'area',created_at:iso(660000),started_at:iso(660000),pending_request_count:0,pending_approval_count:0,...run}));
 function snapshot(id){const run=runs.find(run=>run.id===id),upper=id==='upper';
  const items=[{id:'update',type:'agentMessage',status:'completed',source:'APP_SERVER',revision:2,text:'节点确认与迁移逻辑已实现，正在运行回归测试。接下来检查旧版数据、并发冲突和跨库恢复路径。'},{id:'cmd',type:'commandExecution',status:id==='done'?'completed':'inProgress',source:'APP_SERVER',revision:3,metadata:{command:'go test ./internal/core/... ./internal/application/... ./internal/adapters/...'},outputTail:'ok   internal/core  0.312s\nok   internal/adapters/sqlite  0.884s'}];
  return {detail:{project_id:'project',run:{...run,token_usage:upper?{status:'UNAVAILABLE'}:{status:'AVAILABLE',counts:{total_tokens:15482}}},elapsed_ms:660000,frozen_config:upper?{}:{model:'gpt-6.1-sol',reasoning_effort:'low'},review_evidence_status:upper?'AVAILABLE':'NOT_APPLICABLE',review_evidence:upper?{successful_operations:12,failed_operations:0,forbidden_operations:0}:null,native_output:id==='done'?'两项必需验证命令退出码均为 0。':'',analysis:id==='done'?{state:'RECORDED',report:{summary:'必需检查已复核，等待任务验收与合入。'},recordedAt:iso(40000)}:null},session:{runId:id,generation:'preview',cursor:3,coverage:{state:'AVAILABLE'},runtime:upper?{availability:'UNAVAILABLE',sequence:0}:{availability:'AVAILABLE',sequence:3,connectionId:'fixture-connection',connectionState:'CONNECTED',threadId:run.thread_id,turnId:run.turn_id,turnStatus:id==='done'?'completed':'inProgress',lastActivityAt:iso(8000),nativeThreadStatus:{type:id==='done'?'idle':'active',activeFlags:[]},currentItem:items[1],items,requests:[],latestPlan:{steps:[{step:'核对 Core 决策与现有节点边界',status:'completed'},{step:'实现节点级确认与来源保护',status:'completed'},{step:'运行迁移、并发与恢复测试',status:id==='done'?'completed':'inProgress'},{step:'检查 Desktop 交互与协议兼容',status:id==='done'?'completed':'pending'}]}}},items:{runId:id,items:upper?[]:items.map((item,i)=>({ordinal:i+1,revision:item.revision,item})),hasMore:false,nextBefore:null}};
 }
 fixture.server.on('request',(req,res)=>{
  const url=new URL(req.url,'http://127.0.0.1');
  if(req.method!=='GET'){res.writeHead(405);res.end('Preview is read-only');return;}
  const json=value=>{res.writeHead(200,{'Content-Type':'application/json','Cache-Control':'no-store'});res.end(JSON.stringify(value));};
  if(url.pathname==='/v1/runs'){const scope=url.searchParams.get('scope');return json({items:runs.filter(run=>scope==='current'?run.status==='RUNNING':scope==='history'?run.status!=='RUNNING':true),has_more:false,limit:20,offset:0});}
  const match=url.pathname.match(/^\/ui\/api\/runs\/(running|upper|done)\/session(?:\/(items|events|instructions|native-history))?$/);
  if(match){const [,id,kind]=match,value=snapshot(id);if(!kind)return json(value);if(kind==='items')return json(value.items);if(kind==='instructions')return json({items:[{source:'RECORDED_DISPATCH_INPUT',available:true,dispatched:true,text:'预览指令：实现节点确认，运行回归；此夹具不执行。'}]});if(kind==='native-history')return json({runId:id,threadId:value.detail.run.thread_id,turnId:value.detail.run.turn_id,availability:'AVAILABLE',items:[],nextCursor:null});if(kind==='events'){res.writeHead(200,{'Content-Type':'text/event-stream','Cache-Control':'no-store'});res.write('event: heartbeat\ndata: {}\n\n');const timer=setInterval(()=>res.write('event: heartbeat\ndata: {}\n\n'),10000);req.on('close',()=>clearInterval(timer));return;}}
  return base(req,res);
 });
 return fixture;
}
if(process.argv[1]&&pathToFileURL(resolve(process.argv[1])).href===import.meta.url){const {server}=createSessionPreview();server.listen(0,'127.0.0.1',()=>console.log(`Session preview: http://127.0.0.1:${server.address().port}/ui/#project=project&tab=sessions`));}
