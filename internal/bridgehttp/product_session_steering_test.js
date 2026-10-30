"use strict";
const test=require("node:test"),assert=require("node:assert/strict"),fs=require("node:fs"),vm=require("node:vm"),{webcrypto}=require("node:crypto");
function harness(answer){
 const source=fs.readFileSync(`${__dirname}/ui/product-session-workbench.js`,"utf8").replace(
  "return {mount, stop, refresh, refreshList, select, selectExactRun: select};",
  `return {submit:submitSteering,panel:steering,status:nativeStatus,setup(){project='project';runID='run';host={isConnected:true,querySelector:()=>({value:'  exact <script>text</script>\\n'})};snapshot={detail:{run:{status:'RUNNING'}},session:{runtime:{availability:'AVAILABLE',sequence:1,connectionState:'CONNECTED',connectionId:'connection',threadId:'thread',turnId:'turn'}},steering:{capability:{allowed:true,target:{projectId:'project',runId:'run',threadId:'thread',turnId:'turn',connectionId:'connection'}},receipts:[]}};online=true;streamLive=true;paint=()=>{};paintNotice=()=>{};readSnapshot=async()=>{};},switchRun(){generation++;runID='other';notice='';},receipts(values){snapshot.steering.receipts=values;},notice:()=>notice};`);
 const esc=v=>String(v??"").replaceAll("&","&amp;").replaceAll("<","&lt;").replaceAll(">","&gt;").replaceAll('"',"&quot;");
 const context={window:{DevBridgeComponents:{esc},DevBridgeStatus:{},DevBridgeLogFormat:{}},crypto:webcrypto,TextEncoder,Uint8Array,AbortController,URLSearchParams,console};vm.createContext(context);vm.runInContext(source,context);
 const calls=[];const controller=context.window.DevBridgeSessionWorkbench.create({api:async(url,options)=>{const body=JSON.parse(options.body);calls.push({url,body});return answer(body);},t:v=>v});controller.setup();return {controller,calls};
}
test("steering sends one explicit exact-turn request, retains literal input, and never auto-starts a turn",async()=>{
 let release;const {controller,calls}=harness(body=>new Promise(resolve=>release=()=>resolve({...body,state:"ACCEPTED"})));
 assert.equal(calls.length,0);const pending=controller.submit();await controller.submit();assert.equal(calls.length,1);
 const {url,body}=calls[0];assert.equal(url,"/ui/api/runs/run/steering");assert.equal(body.turnId,"turn");assert.equal(body.text,"  exact <script>text</script>\n");assert.ok(body.submissionRequestId);assert.equal(body.submittedBy,undefined);
 release();await pending;assert.match(controller.notice(),/已接收/);assert.doesNotMatch(controller.panel(),/<script>/);
});
test("lost steering reply blocks new sends and refresh only reads the original receipt",async()=>{
 const {controller,calls}=harness(()=>{throw new Error("lost reply")});await controller.submit();await controller.submit();assert.equal(calls.length,1);assert.match(controller.panel(),/发送结果待核对/);
 controller.receipts([{...calls[0].body,state:"UNKNOWN"}]);assert.match(controller.panel(),/发送结果未知/);await controller.submit();assert.equal(calls.length,1);
 controller.receipts([{...calls[0].body,state:"ACCEPTED"}]);assert.match(controller.panel(),/等待后续执行/);assert.doesNotMatch(controller.panel(),/<script>/);
});
test("late steering receipt cannot publish status into another viewed run",async()=>{
 let release;const {controller}=harness(body=>new Promise(resolve=>release=()=>resolve({...body,state:"ACCEPTED"})));
 const pending=controller.submit();controller.switchRun();release();await pending;assert.equal(controller.notice(),"");
});
test("native runtime flags remain distinct from workflow success and show disconnected evidence",()=>{
 const {controller}=harness(()=>{});const before={nativeThreadStatus:{type:"active",activeFlags:["waitingOnApproval","waitingOnUserInput"]},connectionState:"DISCONNECTED"};
 const html=controller.status(before);assert.match(html,/等待运行授权/);assert.match(html,/等待用户输入/);assert.match(html,/最后记录/);assert.match(html,/不代表任务已验收/);
 assert.equal(controller.status({}),"");
});
