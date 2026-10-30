"use strict";
const test=require("node:test"),assert=require("node:assert/strict"),fs=require("node:fs"),vm=require("node:vm");
const html=fs.readFileSync(process.env.DEVBRIDGE_CONFIRMATION_UI_HTML||`${__dirname}/confirmation_ui.html`,"utf8");
const script=html.match(/<script id="confirmation-main">([\s\S]*?)<\/script>/)[1];

// This is a Card choice/serialization boundary test. It supplies read-only Core
// options, then evaluates the shipped functions; it does not claim to produce
// confirmation authority or exercise a backend runtime.
function card(mode){
 const state={initialized:true,fatal:false,error:"",editableRetry:false,lastFailure:null,
  entry:{purpose:"EXECUTION"},intent:{action:"START_APPROVED",expiresAt:"2099-01-01T00:00:00Z",content:{execution:{action:"START_APPROVED",executionId:""}}},
  draft:{variantId:"start",model:"new-model",reasoningEffort:"high",codexFastMode:false,backendConfigId:"new-backend",backendConfigFingerprint:"b".repeat(64)},
  options:{runtimeSelectionMode:mode,runtimeOptions:[{modelId:"new-model",enabled:true,supportedReasoningEfforts:["high"],codexFastModeSupported:false}],backendInventoryComplete:true,backendCandidates:[{configId:"new-backend",configFingerprint:"b".repeat(64)}]},
  selectedVariant:()=>({id:"start",implementationMode:"CODING_AGENT"}),runtimeNeeded:()=>true,variantReason:()=>"",str:v=>typeof v==="string",structuredClone};
 vm.createContext(state);
 const start=script.indexOf("function choiceError("),end=script.indexOf("function validateAccess(",start);
 assert.ok(start>0&&end>start);vm.runInContext(script.slice(start,end),state);
 return state;
}

test("definition-only start permits explicit selection and serializes only user selection fields",()=>{
 const state=card("EXPLICIT_SELECTION");assert.equal(state.choiceError(),"");
 assert.deepEqual(JSON.parse(JSON.stringify(state.selection())),{runtimeConfig:{model:"new-model",reasoningEffort:"high",codexFastMode:false},backend:{configId:"new-backend",configFingerprint:"b".repeat(64)}});
 state.intent.content.execution.executionId="canceled-previous-revision";
 assert.equal(state.choiceError(),"","old revision pointer must not override Core's exact selection mode");
});
test("new runtime preserves a domain-continuity backend lock",()=>{
 const state=card("EXPLICIT_SELECTION");state.options.lockedBackend={configId:"continuity-backend",configFingerprint:"c".repeat(64)};
 assert.equal(state.choiceError(),"");assert.equal(state.selection().backend.configId,"continuity-backend");
 state.options.lockedRuntimeConfig={model:"new-model",reasoningEffort:"high",codexFastMode:false};assert.notEqual(state.choiceError(),"","contradictory unlocked mode and frozen runtime must block");
});
test("frozen start cannot substitute draft selection for missing frozen evidence",()=>{
 const state=card("FROZEN_EXECUTION");assert.notEqual(state.choiceError(),"");
 state.options.lockedRuntimeConfig={model:"new-model",reasoningEffort:"high",codexFastMode:false,serviceTier:""};assert.notEqual(state.choiceError(),"");
 state.options.lockedBackend={configId:"frozen-backend",configFingerprint:"f".repeat(64)};assert.equal(state.choiceError(),"");
 assert.equal(state.selection().backend.configId,"frozen-backend");
 delete state.options.lockedRuntimeConfig;assert.notEqual(state.choiceError(),"");
});
test("missing or unknown selection mode and incomplete explicit inventory fail closed",()=>{
 for(const mode of [undefined,"", "INHERIT_REVIEW"]){const state=card(mode);assert.notEqual(state.choiceError(),"");}
 const state=card("EXPLICIT_SELECTION");state.options.backendInventoryComplete=false;assert.notEqual(state.choiceError(),"");state.options.backendInventoryComplete=true;state.options.backendCandidates=[];assert.notEqual(state.choiceError(),"");
});

test("GPT file implementation asks for Codex verification selection at file start",()=>{
 const state=card("EXPLICIT_SELECTION"),variant={id:"files",implementationMode:"DIRECT_FILE",repositoryTargets:[{repositoryId:"app"}],verificationSpec:{mode:"RUN"}};
 state.selectedVariant=()=>variant;state.draft.verificationWaived=false;
 const begin=script.indexOf("function runtimeNeeded("),end=script.indexOf("\n",begin);
 vm.runInContext(script.slice(begin,end),state);
 state.intent.action="APPROVE";assert.equal(state.runtimeNeeded(),false,"definition-only approval starts no runtime");
 state.intent.action="START_DIRECT_FILE";state.intent.content.execution.action="START_DIRECT_FILE";
 assert.equal(state.runtimeNeeded(),true);assert.equal(state.choiceError(),"");assert.equal(state.selection().backend.configId,"new-backend");
 state.draft.model="";assert.notEqual(state.choiceError(),"");
 state.intent.action="RECOVER_DIRECT_FILE";assert.equal(state.runtimeNeeded(),false,"recovery retains original verification selection");
 variant.verificationSpec.mode="NONE";state.intent.action="START_DIRECT_FILE";assert.equal(state.runtimeNeeded(),false,"historical explicit waiver does not fabricate a run");
});
