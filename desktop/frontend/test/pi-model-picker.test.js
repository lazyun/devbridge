import {strict as assert} from 'node:assert';
import test from 'node:test';
import fs from 'node:fs';
import vm from 'node:vm';
import {normalizePiCatalog,filterPiCatalog,addPiCatalogModel,piModelPickerHTML,piPickerOptionsHTML} from '../src/pi-model-picker.js';
import {buildSettingsPayload} from '../src/settings-payload.js';
import {settingsHTML} from '../src/views.js';
import {translate} from '../src/i18n.js';
const t=(k,v)=>translate('zh-CN',k,v);
const catalog=[{modelId:'openai/gpt-search',provider:'openai',enabled:true,supportedReasoningEfforts:['low','high'],defaultReasoningEffort:'high',supportsThinking:true,supportsImages:true,contextWindow:'200k',maxOutput:'32k'},{modelId:'anthropic/sonnet',provider:'anthropic',defaultReasoningEffort:'medium'}];
const state=()=>({settings:{piExecutable:'pi',piAgentModels:[{...catalog[0],enabled:false,defaultReasoningEffort:'low'}]},piModelCatalog:catalog,piModelCatalogExecutable:'pi',piSelectedModel:'',piPickerOpen:true,piModelSearch:'',saveStates:{}});
test('Pi catalog search matches provider and model case-insensitively with multiple terms',()=>{assert.deepEqual(filterPiCatalog(catalog,' OPENAI GPT '),[catalog[0]]);assert.deepEqual(filterPiCatalog(catalog,'sonnet'),[catalog[1]]);assert.deepEqual(filterPiCatalog(catalog,'missing'),[]);});
test('adding one selected model preserves existing choices and does not enable duplicates',()=>{const s=state(),existing=s.settings.piAgentModels,next=addPiCatalogModel(existing,catalog,'anthropic/sonnet');assert.equal(next.length,2);assert.deepEqual(next[0],existing[0]);assert.equal(next[1].enabled,true);assert.equal(existing.length,1);const restored=addPiCatalogModel(existing,catalog,'openai/gpt-search');assert.equal(restored[0].enabled,true);assert.equal(restored[0].defaultReasoningEffort,'low');assert.equal(restored.length,1);assert.throws(()=>addPiCatalogModel(existing,catalog,'unknown'));});
test('Pi capability metadata survives the settings save together with custom defaults',()=>{const old=state().settings.piAgentModels,payload=buildSettingsPayload({piAgentModels:old});assert.deepEqual(payload.piAgentModels[0],old[0]);});
test('searchable picker marks added models, escapes catalog text and disables add until selection',()=>{const s=state();s.settings.piAgentModels[0].enabled=true;assert.match(piModelPickerHTML(t,s),/role="combobox"/);assert.match(piModelPickerHTML(t,s),/data-action="add-pi-model" disabled/);assert.match(piPickerOptionsHTML(t,s),/已添加/);s.piModelSearch='absent';assert.match(piPickerOptionsHTML(t,s),/没有匹配/);s.piModelSearch='';s.piModelCatalog=[{modelId:'<script>',provider:'<b>'}];assert.doesNotMatch(piPickerOptionsHTML(t,s),/<script>|<b>/);s.piModelCatalogExecutable='old-pi';assert.match(piModelPickerHTML(t,s),/data-pi-picker-toggle[^>]*disabled/);});
test('configured Pi models display independently of the loaded candidate catalog',()=>{const s=state();s.settingsSection='pi';s.piModelCandidates=[];s.settings.piAgentModels[0].enabled=true;assert.match(settingsHTML(t,s),/data-pi-agent-model="openai\/gpt-search"/);});
test('loading Pi only populates the picker; read failure keeps existing configuration and catalog',async()=>{const source=fs.readFileSync(new URL('../src/main.js',import.meta.url),'utf8');const fn=source.slice(source.indexOf('async function readPiModels()'),source.indexOf('async function invokeAction('));const s={...state(),piModelCatalogLoading:false};let saves=0,fail=false;const context={state:s,normalizePiCatalog,t,setSaveState(){},renderContent(){},onSettingsPage:()=>false,backend:()=>({ReadPiModels:async()=>{if(fail)throw Error('unavailable');return catalog;}}),persistSection(){saves++;}};vm.createContext(context);vm.runInContext(fn,context);const original=JSON.stringify(s.settings);await context.readPiModels();assert.equal(saves,0);assert.equal(JSON.stringify(s.settings),original);assert.equal(s.piPickerOpen,true);fail=true;await context.readPiModels();assert.equal(JSON.stringify(s.settings),original);assert.equal(s.piModelCatalog.length,2);assert.match(s.piModelCatalogError,/unavailable/);});
test('malformed or duplicated catalog entries cannot replace configured models',()=>{assert.throws(()=>normalizePiCatalog([{}]));assert.equal(normalizePiCatalog([catalog[0],catalog[0]]).length,1);});

test('Pi usage selection precedes defaults and the default options contain only enabled selections',()=>{
 const s=state();s.settingsSection='pi';s.settings.piAgentModels=[{...catalog[0],enabled:true},{...catalog[1],enabled:false}];const html=settingsHTML(t,s);assert.ok(html.indexOf('pi-model-selection-title')<html.indexOf('pi-default-settings-title'));assert.ok(html.indexOf('data-action="read-pi-models"')<html.indexOf('name="piModel"'));const modelSelect=html.match(/<select name="piModel"[\s\S]*?<\/select>/)[0];assert.match(modelSelect,/openai\/gpt-search/);assert.doesNotMatch(modelSelect,/anthropic\/sonnet/);
 s.settings.piAgentModels=[];const empty=settingsHTML(t,s);assert.match(empty,/<select name="piModel"[^>]*disabled/);assert.match(empty,/请先在第一步选择并启用/);
});
test('disabling the default clears it without silently choosing a different model',async()=>{
 const {piSelectionChange}=await import('../src/pi-model-picker.js');const models=[{...catalog[0],enabled:false},{...catalog[1],enabled:true}];assert.equal(piSelectionChange({piModel:catalog[0].modelId},models).piModel,'');assert.equal(piSelectionChange({piModel:catalog[1].modelId},models).piModel,catalog[1].modelId);
});
test('Pi default scalar edits report saving and errors in the Pi section',()=>{
 const source=fs.readFileSync(new URL('../src/main.js',import.meta.url),'utf8');const fn=source.slice(source.indexOf('function onScalarChange('),source.indexOf('// ---------- logs:',source.indexOf('function onScalarChange(')));const calls=[];const context={state:{settings:{piModel:''}},persistSection:(...args)=>calls.push(args)};vm.createContext(context);vm.runInContext(fn,context);context.onScalarChange({dataset:{persist:'piModel'},value:'openai/gpt-search'});assert.equal(calls[0][0],'pi');
});

test('unselected Pi models do not render as configured rows and can be selected again from the catalog',()=>{
 const s=state();s.settingsSection='pi';const html=settingsHTML(t,s);assert.doesNotMatch(html,/data-pi-agent-model="openai\/gpt-search"/);assert.match(html,/尚未选择模型/);const choices=piPickerOptionsHTML(t,s);assert.doesNotMatch(choices,/已添加|data-pi-picker-option="openai\/gpt-search" disabled/);
});
test('clear selection saves an empty selected inventory and empty default without changing the executable',async()=>{
 const source=fs.readFileSync(new URL('../src/main.js',import.meta.url),'utf8');const fn=source.slice(source.indexOf('async function clearPiModels()'),source.indexOf('// ---------- immediate save ----------'));const s={...state(),piModelCatalogLoading:false};let change;const context={state:s,persistSection:async(section,value)=>{assert.equal(section,'pi');change=value;return true;},onSettingsPage:()=>false};vm.createContext(context);vm.runInContext(fn,context);await context.clearPiModels();assert.equal(change.piModel,'');assert.equal(change.piAgentModels.length,0);assert.equal(s.settings.piExecutable,'pi');
});

test('Pi settings stay editable while managed services run and expose pending restart explicitly',()=>{
 const s=state();s.settingsSection='pi';s.settings.piAgentModels=[{...catalog[0],enabled:true}];s.status={core:{ownership:'MANAGED',state:'RUNNING'},piRestartRequired:true};
 let html=settingsHTML(t,s);
 assert.doesNotMatch(html,/<select name="piModel"[^>]*disabled/);
 assert.doesNotMatch(html,/<select name="piThinkingLevel"[^>]*disabled/);
 assert.match(html,/已保存，重启服务后生效/);
 assert.match(html,/应用并重启/);
 assert.match(html,/重启会中断运行中的任务/);
 s.status.piRestartRequired=false;html=settingsHTML(t,s);
 assert.doesNotMatch(html,/应用并重启/);
 assert.match(html,/配置即时保存/);
});
