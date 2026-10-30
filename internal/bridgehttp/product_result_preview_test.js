"use strict";
const {test}=require("node:test"),assert=require("node:assert/strict"),fs=require("node:fs");
const C=require("./ui/product-components.js"),TW=require("./ui/product-task-workbench.js");
const {createDocument}=require("./testdata/product_dom_harness.js");
const t=require("./ui/product-i18n.js").createI18n({navigator:{languages:["zh-CN"]}}).t;

test("result preview is one compact line with a lossless escaped full snapshot",()=>{
  const full='结论 😀 '.repeat(40)+'\r\n第二行\n<script>alert("x")</script> & &#39; \\path\t尾部';
  const doc=createDocument(C.resultPreview(full,t,{title:'报告 "一"'}));
  const preview=doc.querySelector('.result-preview-text'),button=doc.querySelector('[data-view-full-result]');
  assert.ok(Array.from(preview.textContent).length<=81);
  assert.doesNotMatch(preview.textContent,/[\r\n]/);assert.ok(preview.textContent.endsWith('…'));
  assert.equal(JSON.parse(button.dataset.resultContent),full);assert.equal(button.textContent,'查看全部');
  assert.equal(button.dataset.resultTitle,'报告 "一"');assert.equal(doc.querySelector('script'),null);
  const css=fs.readFileSync(`${__dirname}/ui/product.css`,'utf8');
  assert.match(css,/\.result-preview \.result-preview-text\s*\{[^}]*white-space: nowrap/);
  assert.match(css,/\.result-preview \.result-preview-text\s*\{[^}]*text-overflow: ellipsis/);
  assert.equal(C.resultPreview(null,t),'');assert.equal(C.resultPreview('',t),'');
  const structured=createDocument(C.resultPreview({passed:false,summary:'verification failed'},t));
  assert.match(structured.querySelector('.result-preview-text').textContent,/passed.*false.*verification failed/);
});

test("Native output stays readable while evidence previews and exact Review read targets remain available",()=>{
  const report={passed:false,summary:'失败结论 '.repeat(100)},evidence={available:true,agent_claim:'实施结果 '.repeat(100)};
  for(const [purpose,tab] of [['VERIFICATION','evidence'],['IMPLEMENTATION','evidence'],['IMPLEMENTATION','overview']]){
    const doc=createDocument(TW.renderRun({run:{purpose},verification_report:report,evidence},{C,t,activeTab:tab}));
    if(tab==='overview')assert.ok(doc.root.textContent.includes(evidence.agent_claim));else assert.ok(doc.querySelector('[data-view-full-result]'));
    for(const preview of doc.querySelectorAll('.result-preview-text'))assert.ok(Array.from(preview.textContent).length<=81);
    assert.doesNotMatch(doc.root.textContent,/失败结论 失败结论 失败结论 失败结论 失败结论 失败结论 失败结论 失败结论 失败结论 失败结论 失败结论 失败结论 失败结论 失败结论 失败结论 失败结论 失败结论 失败结论/);
  }
  const doc=createDocument(TW.renderRun({run:{purpose:'SCOPE_REVIEW',review_id:'exact-review'},review_result:'bounded preview',review_result_truncated:true},{C,t,activeTab:'evidence'}));
  assert.equal(doc.querySelector('[data-view-full-review-result]').dataset.viewFullReviewResult,'exact-review');
  assert.equal(doc.querySelector('[data-view-full-result]'),null);
});

test("persisted GPT report and native output are separately readable without compact truncation or executable markup",()=>{
 const summary='独立分析\n第二行 '+"完整内容 ".repeat(100)+'<script>bad()</script>';
 const native='Codex 原始答复\n保留原文 <img src=x onerror=bad>';
 const report={version:2,summary,checks:[{index:0,status:'PASSED',summary:'真实检查',evidence:'exit=0'}],acceptanceCriteria:[{status:'FAILED',summary:'保留失败结论'}],workSpec:{goal:'原方案'}};
 const detail={run:{status:'SUCCEEDED',purpose:'VERIFICATION',analysis_state:'RECORDED'},native_output:native,analysis:{state:'RECORDED',report,receipt:{submittedAt:'2026-10-22T10:00:00Z'}}};
 for(const tab of ['overview','evidence']){
  const doc=createDocument(TW.renderRun(detail,{C,t,activeTab:tab}));
  assert.match(doc.root.textContent,/GPT 分析/);assert.ok(doc.root.textContent.includes(summary));assert.ok(doc.root.textContent.includes(native));
  assert.match(doc.root.textContent,/保留失败结论/);assert.match(doc.root.textContent,/exit=0/);
  assert.equal(doc.querySelector('script'),null);assert.equal(doc.querySelector('img'),null);
  assert.equal(doc.querySelector('.run-output .result-preview'),null);
  assert.equal(doc.querySelector('[data-task-action]'),null);
 }
 assert.equal(C.needsAnalysisRefresh(detail),false);
});

test("unsubmitted, unreadable and truncated output remain explicit and read refresh stops after recorded analysis",()=>{
 const pending={run:{status:'SUCCEEDED',analysis_state:'REQUIRED'},native_output:'已完成自然语言输出'};
 assert.match(createDocument(C.runOutput(pending,t)).root.textContent,/等待 GPT 提交分析/);
 assert.equal(C.needsAnalysisRefresh(pending),true);
 assert.equal(C.needsAnalysisRefresh({run:{status:'SUCCEEDED',analysis_state:'OPTIONAL'}}),true);
 assert.equal(C.needsAnalysisRefresh({run:{status:'RUNNING',analysis_state:'WAITING_EXECUTION'}}),false);
 assert.equal(C.needsAnalysisRefresh({run:{status:'SUCCEEDED',analysis_state:'NOT_REQUIRED'}}),false);
 const failed={...pending,analysis_read_error:'读取失败，不表示未提交',native_output_truncated:true};
 assert.match(createDocument(C.runOutput(failed,t)).root.textContent,/读取失败，不表示未提交/);
 assert.match(createDocument(C.runOutput(failed,t)).root.textContent,/已截断/);
 assert.doesNotMatch(createDocument(C.runOutput(failed,t)).root.textContent,/等待 GPT 提交分析/);
 assert.match(createDocument(C.runOutput({run:{analysis_state:'RECORDED'}},t)).root.textContent,/正文尚未读取/);
});

test("Task overview links to exact Review, implementation and verification outputs",()=>{
 const doc=createDocument(TW.taskResults({currentReview:{run:{id:'review-run'}},currentExecution:{implementationRunId:'impl-run',verificationRunId:'verify-run'}},{C,t}));
 assert.deepEqual(doc.querySelectorAll('[data-task-run]').map(x=>x.dataset.taskRun),['review-run','impl-run','verify-run']);
 assert.ok(doc.querySelectorAll('[data-task-run]').every(x=>x.dataset.runOpenTab==='evidence'));
});
