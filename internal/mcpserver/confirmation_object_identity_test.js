"use strict";
const test=require("node:test"),assert=require("node:assert/strict");
const {createCardInstance,renderedText,fixture}=require("./confirmation_card_instance_mount_test.js");
const clone=value=>JSON.parse(JSON.stringify(value));

// Exercise the shipped widget through Host entry/read notifications. These are
// synthetic presentation inputs, not business receipts or authorization tests.
async function cardFor(purpose,action,body,displayValues){
 const envelope=clone(fixture.replacement.entry),value=envelope.intent;
 delete envelope.display;
 value.purpose=purpose;value.action=action;
 value.content={aiSessionId:value.aiSessionId,purpose,action,variants:[{id:"show",label:"准备内容",summary:"确认操作范围"}],...body};
 if(displayValues)envelope.display={aiSessionId:value.aiSessionId,intentId:value.intentId,version:value.version,purpose,projectId:value.projectId,status:"AVAILABLE",objectNameSource:"CURRENT_AUTHORIZED_OBJECT",...displayValues};
 const frozen=JSON.stringify(value.content);
 const instance=createCardInstance("object-identity",async message=>{
  if(message.method==="ui/initialize")return {result:{protocolVersion:"2026-01-26",serverInfo:{name:"identity-test",version:"1"}}};
  if(message.method!=="tools/call")return {result:{}};
  const {name,arguments:request}=message.params;
  if(name==="confirmation_get")return {result:{structuredContent:envelope}};
  assert.equal(name,"confirmation_options_get","rendering must not issue a write");
  const options=clone(fixture.replacement.options.entry.options);
  options.purpose=purpose;options.variants=[{id:"show",label:"准备内容",available:true}];
  if(purpose==="TASK")options.startReview=request.startReview;
  return {result:{structuredContent:{options}}};
 });
 await instance.settle();
 await instance.pushToolInput({aiSessionId:value.aiSessionId,intentId:value.intentId,version:value.version,purpose,action,requestId:"show"});
 await instance.pushToolResult(envelope);
 assert.equal(instance.peek("renderFailed"),false);
 assert.equal(instance.peek("fatal"),false);
 assert.equal(instance.peek("JSON.stringify(intent.content)"),frozen,"labels must preserve frozen business content");
 return instance;
}
function find(instance,className){
 const stack=[instance.peek("root")];
 while(stack.length){const node=stack.pop();if(node.className===className)return node;stack.push(...(node.childNodes||[]));}
 return null;
}
const textOf=node=>renderedText(node).join(" ").trim();
const area=(extra={})=>({featureArea:{title:"相同的名称",problemStatement:"领域目标",...extra}});
const task=()=>({task:{title:"相同的名称",problemStatement:"任务目标",featureAreaId:"area",startReview:false}});

test("area and task creation have explicit badges and name labels even with identical titles",async()=>{
 for(const [purpose,action,body,badge,caption]of [
  ["FEATURE_AREA","CREATE_AREA",area(),"创建领域","领域名称"],
  ["TASK","CREATE_TASK",task(),"创建任务","任务名称"]
 ]){
  const instance=await cardFor(purpose,action,body),identity=find(instance,"dbc-purpose");
  assert.ok(identity,"the operation must have a prominent badge");
  assert.equal(textOf(identity),badge);
  assert.equal(identity.getAttribute("data-purpose"),purpose);
  assert.equal(identity.childNodes[0].tagName,"SVG","type also has an icon");
  assert.equal(textOf(find(instance,"dbc-eyebrow")),caption);
  assert.match(textOf(find(instance,"dbc-intro")),/相同的名称/);
  assert.equal(find(instance,"dbc-initial-task"),null);
 }
});

test("creating an area with an initial task names both objects in the introduction",async()=>{
 const instance=await cardFor("FEATURE_AREA","CREATE_AREA_AND_REVIEW",area({initialTask:{title:"检查权限边界",problemStatement:"任务目标",startReview:true}}));
 const intro=find(instance,"dbc-intro"),taskBlock=find(instance,"dbc-initial-task");
 assert.equal(textOf(find(instance,"dbc-purpose")),"创建领域");
 assert.equal(textOf(taskBlock),"同时创建初始任务 检查权限边界");
 assert.ok(intro.childNodes.includes(taskBlock));
});

test("a task title cannot override the operation type and is rendered as plain text",async()=>{
 const body=task();body.task.title="创建领域 <img src=x onerror=alert(1)>";
 const instance=await cardFor("TASK","CREATE_TASK",body);
 assert.equal(textOf(find(instance,"dbc-purpose")),"创建任务");
 const heading=find(instance,"dbc-intro").childNodes.find(n=>n.tagName==="H1");
 assert.equal(heading.textContent,body.task.title);
 assert.equal(heading.childNodes.length,0);
});

test("area merge uses its actual action and does not claim to create an initial task",async()=>{
 const instance=await cardFor("FEATURE_AREA","MERGE_FEATURE_AREA",area({initialTask:{title:"历史任务"}}));
 assert.equal(textOf(find(instance,"dbc-purpose")),"合入领域交付");
 assert.equal(textOf(find(instance,"dbc-eyebrow")),"领域名称");
 assert.equal(find(instance,"dbc-initial-task"),null);
});

test("existing area merge accepts its exact live name and never uses creation placeholders",async()=>{
 const body={featureArea:{featureAreaId:"area",title:"",problemStatement:""}};
 const instance=await cardFor("FEATURE_AREA","MERGE_FEATURE_AREA",body,{featureAreaId:"area",featureAreaName:"Core 依赖边界一致性",projectName:"skeleton"});
 assert.equal(instance.peek("display?.featureAreaId"),"area");
 assert.match(textOf(find(instance,"dbc-intro")),/Core 依赖边界一致性/);
 const all=textOf(instance.peek("root"));
 assert.match(all,/skeleton/);assert.match(all,/合入已验收提交/);
 assert.doesNotMatch(all,/待创建领域|创建内容/);
 const unavailable=await cardFor("FEATURE_AREA","MERGE_FEATURE_AREA",body);
 assert.match(textOf(find(unavailable,"dbc-intro")),/当前领域（名称暂不可读取）/);
 assert.doesNotMatch(textOf(find(unavailable,"dbc-intro")),/待创建领域/);
});

test("implementation definition confirmation is labelled before any runtime start",async()=>{
 const instance=await cardFor("EXECUTION","CONFIRM_IMPLEMENTATION",{execution:{action:"CONFIRM_IMPLEMENTATION",taskId:"task"}});
 assert.equal(textOf(find(instance,"dbc-purpose")),"确认实施方案");
 assert.equal(find(instance,"dbc-initial-task"),null);
});

test("skipped-review plan and runtime start share one explicit confirmation",async()=>{
 const action="CONFIRM_AND_START_IMPLEMENTATION";
 const instance=await cardFor("EXECUTION",action,{execution:{action,taskId:"task",repositoryId:"repo",baseCommit:"a".repeat(40)}});
 assert.equal(textOf(find(instance,"dbc-purpose")),"确认并开始实施");
 assert.equal(instance.peek("actionLabel()"),"确认并开始实施");
 assert.match(instance.peek("consequence()"),/运行配置.*立即开始/);
 assert.equal(instance.peek("runtimeNeeded({implementationMode:'CODING_AGENT'})"),true);
 assert.match(textOf(instance.peek("root")),/目标仓库.*repo/);
 assert.ok(instance.toolCalls.every(call=>call.name!=="confirmation_commit"));
});

test("readback and superseded states retain the object identity without claiming success",async()=>{
 const instance=await cardFor("TASK","CREATE_TASK",task());
 for(const state of ["unknown=true","unknown=false;intent.state='SUPERSEDED'"]){
  instance.peek(`${state};safeRender()`);
  assert.equal(instance.peek("renderFailed"),false);
  assert.equal(textOf(find(instance,"dbc-purpose")),"创建任务");
  assert.equal(textOf(find(instance,"dbc-eyebrow")),"任务名称");
  assert.doesNotMatch(textOf(find(instance,"dbc-purpose")),/已创建|成功/);
 }
 assert.ok(instance.toolCalls.every(call=>call.name!=="confirmation_commit"));
});
