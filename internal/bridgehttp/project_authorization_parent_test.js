"use strict";
const test=require("node:test"),assert=require("node:assert/strict"),fs=require("node:fs"),vm=require("node:vm");
const source=fs.readFileSync(`${__dirname}/ui/product-app.js`,"utf8");
const policy=(project="A",revision=0,trusted=false,network=false)=>({project_id:project,head_revision:revision,trusted_execution:trusted,network_access:network});
function deferred(){let resolve,reject;const promise=new Promise((a,b)=>{resolve=a;reject=b;});return {promise,resolve,reject};}
function fixture(){
 const sent=[],state={selectedProjectID:"A",projectDataErrors:{},executionAuthorization:null,executionAuthorizationDraft:null,executionAuthorizationGeneration:0,executionAuthorizationSequence:0,executionAuthorizationSaving:false,executionAuthorizationReading:false,executionAuthorizationConflict:false,executionAuthorizationError:"",executionAuthorizationNotice:"",executionAuthorizationRequest:null,developmentProjectVersion:0,developmentReadVersion:0,taskFilters:{area:"",branch:""},workItemBranchFilter:"",projectTab:"overview"};
 const c={state,C:{esc:v=>String(v??"")},t:k=>k,render:()=>{},api:async(path,options)=>{sent.push({path,options});return policy("A",1,true,true);},AbortController,workspaceReadVersion:0,projectReadController:null,worktreeReadVersion:0,taskListGeneration:0,taskListController:null,areaTaskControllers:new Map(),sessionWorkbench:{stop(){}},workRefresh:{stop(){}},detailRoute:{close(){}},qs:()=>"",W:{normalizeTaskFilters:()=>({area:"",branch:""})},persistWorkItemFilters:()=>{},workItems:()=>[]};
 vm.createContext(c);
 vm.runInContext(source.slice(source.indexOf("  function resetExecutionAuthorization("),source.indexOf("  function renderActiveAgentOperations(")),c);
 vm.runInContext(source.slice(source.indexOf("  function clearProjectData("),source.indexOf("  async function readHealth(")),c);
 vm.runInContext(source.slice(source.indexOf("  async function loadProjectData("),source.indexOf("  const LOG_QUERY_FIELDS")),c);
 const load=value=>c.applyExecutionAuthorizationRead({status:"fulfilled",value},state.selectedProjectID);
 const form=(trusted=true,network=true)=>({isConnected:true,dataset:{authorizationProject:state.selectedProjectID,authorizationGeneration:String(state.executionAuthorizationGeneration)},elements:{expected_revision:{value:String(state.executionAuthorization?.head_revision||0)},trusted_execution:{checked:trusted},network_access:{checked:network}}});
 const switchTo=project=>{state.selectedProjectID=project;c.clearProjectData();};
 return {c,state,sent,load,form,switchTo};
}
test("authorization cannot be saved before its exact project settings were read",async()=>{
 const f=fixture();assert.equal(f.c.executionAuthorizationReady(),false);
 assert.match(f.c.renderExecutionAuthorization(),/type="submit" disabled/);
 await f.c.saveExecutionAuthorization(f.form());assert.equal(f.sent.length,0);
 f.load(policy("B"));assert.equal(f.c.executionAuthorizationReady(),false);
 assert.match(f.c.renderExecutionAuthorization(),/authorization.readFailed/);
});
test("project change clears old authority, pending save and draft; stale form cannot grant another project",async()=>{
 const f=fixture();f.load(policy());const old=f.form();f.c.rememberExecutionAuthorizationDraft(old);
 f.state.executionAuthorizationSaving=true;f.state.executionAuthorizationNotice="old";
 f.switchTo("B");
 assert.equal(f.state.executionAuthorization,null);assert.equal(f.state.executionAuthorizationDraft,null);
 assert.equal(f.state.executionAuthorizationSaving,false);assert.equal(f.state.executionAuthorizationNotice,"");
 f.load(policy("B"));await f.c.saveExecutionAuthorization(old);assert.equal(f.sent.length,0);
});
test("one explicit save sends both selected capabilities and exact revision once",async()=>{
 const f=fixture(),pending=deferred();f.load(policy());f.c.api=async(path,options)=>{f.sent.push({path,options});return pending.promise;};
 const form=f.form(true,false);f.c.rememberExecutionAuthorizationDraft(form);
 assert.match(f.c.renderExecutionAuthorization(),/name="trusted_execution" checked/);
 const first=f.c.saveExecutionAuthorization(form);await f.c.saveExecutionAuthorization(form);
 assert.equal(f.sent.length,1);assert.deepEqual(JSON.parse(f.sent[0].options.body),{trusted_execution:true,network_access:false,expected_revision:0});
 assert.match(f.c.renderExecutionAuthorization(),/type="submit" disabled/);
 pending.resolve(policy("A",1,true,false));await first;
 assert.equal(f.state.executionAuthorization.head_revision,1);assert.equal(f.c.executionAuthorizationReady(),true);
 assert.equal(f.state.executionAuthorizationSaving,false);
});
test("late save response cannot overwrite a newer A after A-B-A navigation",async()=>{
 const f=fixture(),pending=deferred();f.load(policy());f.c.api=()=>pending.promise;
 const operation=f.c.saveExecutionAuthorization(f.form());
 f.switchTo("B");f.load(policy("B",1));f.switchTo("A");f.load(policy("A",2,false,true));
 pending.resolve(policy("A",1,true,true));await operation;
 assert.equal(f.state.executionAuthorization.head_revision,2);assert.equal(f.state.executionAuthorization.trusted_execution,false);
 assert.equal(f.state.executionAuthorizationSaving,false);
});
test("read failure preserves the last display but disables writes until a successful read",async()=>{
 const f=fixture();f.load(policy("A",1,true,false));
 f.c.applyExecutionAuthorizationRead({status:"rejected",reason:new Error("offline")},"A");
 assert.equal(f.state.executionAuthorization.trusted_execution,true);assert.equal(f.c.executionAuthorizationReady(),false);
 await f.c.saveExecutionAuthorization(f.form());assert.equal(f.sent.length,0);
 f.c.api=async()=>policy("A",1,true,false);await f.c.refreshExecutionAuthorization(true);
 assert.equal(f.c.executionAuthorizationReady(),true);
});
test("background refresh preserves an unsaved draft and requires re-read on revision conflict",async()=>{
 const f=fixture();f.load(policy());f.c.rememberExecutionAuthorizationDraft(f.form(true,false));
 f.load(policy());assert.equal(f.state.executionAuthorizationDraft.trusted_execution,true);
 f.load(policy("A",1,false,true));assert.equal(f.c.executionAuthorizationReady(),false);
 assert.equal(f.state.executionAuthorizationDraft.trusted_execution,true);
 assert.equal(f.state.executionAuthorizationConflict,true);
 f.c.api=async()=>policy("A",1,false,true);await f.c.refreshExecutionAuthorization(true);
 assert.equal(f.c.executionAuthorizationReady(),true);assert.equal(f.state.executionAuthorizationDraft.base_revision,1);
 assert.equal(f.state.executionAuthorizationDraft.network_access,true);
});
test("failed or foreign save acknowledgements do not permit a blind retry",async()=>{
 const f=fixture();f.load(policy());f.c.api=async()=>policy("B",1,true,true);
 await f.c.saveExecutionAuthorization(f.form());
 assert.equal(f.state.executionAuthorization.head_revision,0);assert.equal(f.c.executionAuthorizationReady(),false);
 assert.match(f.c.renderExecutionAuthorization(),/authorization.unknown/);
});
test("a stale form revision explains the conflict instead of silently returning",async()=>{
 const f=fixture();f.load(policy("A",1));const form=f.form();form.elements.expected_revision.value="0";
 await f.c.saveExecutionAuthorization(form);
 assert.equal(f.sent.length,0);assert.equal(f.c.executionAuthorizationReady(),false);
 assert.match(f.c.renderExecutionAuthorization(),/authorization.changed/);
});
test("an older project-wide read cannot overwrite a successful save",async()=>{
 const f=fixture(),old=deferred();f.load(policy());
 f.c.api=async(path,options)=>options?.method==="PUT"?policy("A",1,true,true):path.includes("execution-authorization")?old.promise:{items:[]};
 const read=f.c.loadProjectData();await f.c.saveExecutionAuthorization(f.form());
 old.resolve(policy());await read;
 assert.equal(f.state.executionAuthorization.head_revision,1);assert.equal(f.state.executionAuthorization.trusted_execution,true);
});
test("a project-wide read superseding an explicit read clears the loading state",async()=>{
 const f=fixture(),old=deferred(),fresh=deferred();f.load(policy());let reads=0;
 f.c.api=async path=>path.includes("execution-authorization")?(++reads===1?old.promise:fresh.promise):{items:[]};
 const first=f.c.refreshExecutionAuthorization(),second=f.c.loadProjectData();
 fresh.resolve(policy("A",1,true,false));await second;
 old.resolve(policy());await first;
 assert.equal(f.state.executionAuthorizationReading,false);assert.equal(f.state.executionAuthorization.head_revision,1);
 assert.equal(f.c.executionAuthorizationReady(),true);
});

test("authorization revision and timestamp interpolate in both locales",()=>{
 const I=require("./ui/product-i18n.js");
 for(const locale of ["en","zh-CN"]){
  const {t}=I.createI18n({navigator:{languages:[locale]}});
  const text=t("authorization.meta",{revision:3,updated:"2026-10-26T00:00:00Z"});
  assert.match(text,/3/);assert.match(text,/2026-10-26T00:00:00Z/);assert.doesNotMatch(text,/[{}]/);
 }
});
