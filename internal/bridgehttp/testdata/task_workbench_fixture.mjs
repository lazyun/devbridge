// Scenario data/transport only; all UI is the production /ui/ implementation.
export const scenarios = ['workbench', 'empty', 'read-error', 'partial-history'];
const time = '2026-09-12T10:30:00Z';
export function extendWorkbenchFixture(tasks, area, workspace, scenario = 'workbench') {
  const clone = value => JSON.parse(JSON.stringify(value));
  const extra = (source, id, title, phase, status = 'ACTIVE') => {
    const value = clone(source), v = value.presentation.taskView;
    Object.assign(value, {id, title, phase, status, state: status});
    Object.assign(v, {id, title, phase, status, terminal: status !== 'ACTIVE', actions: [], reviewHistory: [], executionHistory: [], currentReview: null, currentExecution: null, currentAction: {code: 'WAIT', message: title}});
    value.presentation.details.taskId = id;
    value.presentation.capabilities = [];
    return value;
  };
  const failed = extra(tasks[2], 'task-review-failed', '审查未完成／本次运行已中断', 'REVIEWING');
  failed.presentation.taskView.currentWorkKind = 'REVIEW';
  failed.presentation.taskView.phaseLabel = '审查';
  failed.presentation.taskView.phaseDescription = '审查未完成；Task 仍为 ACTIVE';
  const failedReview = {id: 'review-failed', taskId: failed.id, ordinal: 1, reviewDepth: 'FAST', status: 'FAILED', resultAvailable: false, failureSummary: 'Fixture transport interrupted', run: {id: 'run-review-failed', transportStatus: 'INFRASTRUCTURE_FAILED', failureCode: 'BACKEND_DISCONNECTED'}, createdAt: time, updatedAt: time};
  failed.presentation.taskView.currentReview = failedReview;
  failed.presentation.taskView.reviewHistory = [failedReview];
  failed.presentation.taskView.activity = {status: 'INFRASTRUCTURE_FAILED', summary: failed.title};
  const accepted = extra(tasks[1], 'task-accepted-git-failed', '任务已验收，Git 提交失败', 'ACCEPTANCE_PENDING', 'SUCCEEDED');
  Object.assign(accepted.presentation.taskView, {phaseLabel: '完成', phaseDescription: '任务验收已完成', commitActivity: 'FAILED', commitStatus: 'FAILED', commitErrorSummary: 'Fixture: Git finalization failed', commitSummary: '提交失败', devSummary: '尚未集成', progress: ['REVIEW','APPROVED','IMPLEMENTATION','VERIFICATION','DONE'].map(phase=>({phase,status:'COMPLETE'}))});
  Object.assign(accepted, {commitActivity: 'FAILED', commitStatus: 'FAILED', commitErrorSummary: 'Fixture: Git finalization failed'});
  const multi = tasks[1].presentation.taskView;
  const prior = multi.executionHistory[0]; prior.current = false; prior.acceptance = {option: 'REQUEST_REVERIFICATION'};
  const next = {id: 'execution-verification-only', state: 'SUCCEEDED', outcome: 'PENDING', taskDefinitionRevisionId: prior.execution.taskDefinitionRevisionId, verificationRunId: 'run-verification-only'};
  multi.executionHistory.push({current: true, ordinal: 2, execution: next, verificationRun: {id: next.verificationRunId, transportStatus: 'SUCCEEDED'}});
  multi.currentExecution = next; multi.implementationRun = null;
  multi.verificationReport = {availability: 'AVAILABLE', verdict: 'FAIL', checks: [{id:'check-1', name:'Acceptance evidence', status:'FAILED', summary:'Transport succeeded; verification failed'}]};
  multi.currentWorkSpec = {goal: 'Frozen implementation specification', constraints: ['Task authority']};
  multi.currentVerificationSpec = {mode:'COMMANDS', commands:['go test ./...']};
  const older={current:false,ordinal:1,execution:{id:'execution-older',state:'SUCCEEDED',outcome:'SUCCEEDED',taskDefinitionRevisionId:'definition-older',provenanceKind:'INITIAL'},implementationRun:{id:'implementation-older',transportStatus:'SUCCEEDED'},verificationRun:{id:'verification-older',transportStatus:'SUCCEEDED'},acceptance:{option:'REQUEST_CHANGES'}};
  multi.executionHistory.forEach(h=>h.ordinal++);multi.executionHistory.unshift(older);
  next.provenanceKind='EXACT';multi.executionHistory.find(h=>h.current).sourceAction='REQUEST_REVERIFICATION';prior.execution.provenanceKind='EXACT';prior.sourceAction='REQUEST_CHANGES';
  const reviewView=tasks[2].presentation.taskView;reviewView.currentReview.ordinal=2;
  reviewView.reviewHistory.unshift({id:'review-fast-prior',taskId:tasks[2].id,ordinal:1,reviewDepth:'FAST',reviewDisposition:'FURTHER_REVIEW_REQUIRED',status:'SUCCEEDED',resultAvailable:true,instruction:'Historical FAST persisted instruction',run:{id:'review-fast-prior-run',transportStatus:'SUCCEEDED'},createdAt:time,updatedAt:time});
  tasks.push(failed, accepted);
  const runtime = {availability:'AVAILABLE', runId: 'implementation-0', taskId: tasks[0].id, projectId:'project', featureAreaId:'area', waitingState:'WAITING_FOR_PERMISSION', requests:[{id:'fixture-request', state:'PENDING', status:'PENDING', method:'item/commandExecution/requestApproval', projectId:'project', featureAreaId:'area',taskId:tasks[0].id,runId:'implementation-0',threadId:'fixture-thread',turnId:'fixture-turn',connectionId:'fixture-connection',requestId:'fixture-request',payload:{command:'go test ./...',reason:'Fixture-only runtime permission'},availableResponses:[{decision:'decline'}]}]};
  tasks[0].presentation.taskView.activity.runtime = runtime;
  tasks[0].presentation.taskView.activity.summary = '等待运行时权限请求';
  const runDetails = new Map();
  for (const task of tasks) {
    const v = task.presentation.taskView;
    const rows = [];
    const add = (run, purpose, review, execution) => {
      if (!run) return;
      const summary = {id:run.id, executionId:`agent-${run.id}`, agentExecutionId:`agent-${run.id}`, taskExecutionId:execution?.id || '', reviewId:review?.id || '', operationKind:review?'REVIEW':'DEVELOPMENT', operationId:review?.id||execution?.id||'', runPurpose:purpose, purpose:purpose==='SCOPE_REVIEW'?'REVIEW':purpose, transportStatus:run.transportStatus, failureCode:run.failureCode||'', tokenStatus:run.failureCode?'UNAVAILABLE':'AVAILABLE', model:'fixture-model', reasoningEffort:'high', configSource:review?'REVIEW':'EXECUTION', startedAt:time, createdAt:time, elapsedMs:12000, durationComplete:run.transportStatus!=='RUNNING', taskDefinitionRevisionId:execution?.taskDefinitionRevisionId||''};
      rows.push(summary);
      const raw = {id:run.id,task_id:task.id,project_id:'project',feature_area_id:'area',review_id:review?.id,execution_proposal_id:execution?.id,operation_kind:summary.operationKind,operation_id:summary.operationId,purpose,status:run.transportStatus,attempt:1,created_at:time,started_at:time,duration_ms:12000,thread_id:'fixture-thread',turn_id:'fixture-turn',token_usage:{status:summary.tokenStatus,counts:summary.tokenStatus==='AVAILABLE'?{input_tokens:0,cached_input_tokens:0,output_tokens:0,reasoning_output_tokens:0,total_tokens:0}:undefined},agent_runtime:run.id==='implementation-0'?runtime:undefined};
      raw.agent_execution_id=summary.agentExecutionId;
      const detail = {frozen_config:{model:summary.model,reasoning_effort:summary.reasoningEffort,source:summary.configSource,task_definition_revision_id:summary.taskDefinitionRevisionId},runtime:raw.agent_runtime,elapsed_ms:12000,run:raw, project_id:'project', task_id:task.id, agent_execution_id:summary.agentExecutionId, task_execution_id:summary.taskExecutionId, task_definition_revision_id:summary.taskDefinitionRevisionId, model:summary.model,reasoning_effort:summary.reasoningEffort,config_source:summary.configSource,duration_complete:summary.durationComplete,evidence:{available:purpose==='IMPLEMENTATION',changed_files:[]},approvals:[],events:[],failure:run.failureCode?{code:run.failureCode,message:'Fixture transport interrupted'}:undefined,verification_report:purpose==='VERIFICATION'?{availability:'AVAILABLE',verdict:'FAIL',checks:[{name:'fixture-check',status:'FAILED'}]}:undefined,review_evidence:review?{available:review.resultAvailable,provenance:{source:'fixture'}}:undefined};
      runDetails.set(run.id, detail);
    };
    for (const review of v.reviewHistory) add(review.run,'SCOPE_REVIEW',review);
    for (const history of v.executionHistory) {add(history.implementationRun,'IMPLEMENTATION',null,history.execution);add(history.verificationRun,'VERIFICATION',null,history.execution);}
    rows.sort((a,b)=>a.createdAt.localeCompare(b.createdAt)||a.id.localeCompare(b.id));
    v.details.runs = rows; v.details.runsTotal = rows.length; v.details.runsTruncated = false;
    v.details.definitionRevisions = [...new Set([v.details.taskDefinitionRevisionId,...v.executionHistory.map(h=>h.execution.taskDefinitionRevisionId)])].filter(Boolean).map((id,index)=>({id,ordinal:index+1,current:id===v.details.taskDefinitionRevisionId,payloadSha256:'fixture-revision-hash',createdAt:time}));
    v.details.definitionRevisionsStatus='AVAILABLE';
    v.actions.forEach(c=>{c.target={...c.target,taskId:task.id,decisionId:`decision-${task.id}`};});
    if (task.phase==='REVIEW_APPROVAL_PENDING' || task.phase==='ACCEPTANCE_PENDING') task.presentation.pendingDecision = {id:`decision-${task.id}`,kind:task.phase==='REVIEW_APPROVAL_PENDING'?'TASK_REVIEW_GATE':'TASK_ACCEPTANCE',status:'PENDING'};
  }
  const inspectionRun={id:'run-inspection',project_id:'project',task_id:'',purpose:'INSPECTION',operation_kind:'INSPECTION',operation_id:'inspection-fixture',status:'RUNNING',created_at:time,token_usage:{status:'PENDING'}};
  runDetails.set(inspectionRun.id,{run:inspectionRun,project_id:'project',frozen_config:{source:'UNKNOWN',model:'',reasoning_effort:''},elapsed_ms:null,duration_complete:false,evidence:{available:false,changed_files:[]},approvals:[],events:[]});
  workspace.tasks = scenario === 'empty' ? [] : tasks;
  workspace.active_agent_operations = [{kind:'INSPECTION',operation_id:'inspection-fixture',run_id:'run-inspection',state:'RUNNING',message:'Project-scope inspection; no semantic Task owner'}];
  area.task_count = workspace.tasks.length;
  const read = (path, query) => {
    const error = (status,message) => ({status,data:{message}});
    if (path === '/ui/api/project-workspace' && scenario === 'read-error') return error(503,'Fixture: project read failed');
    if (path === '/v1/runs') return {data:{items:[...runDetails.values()].map(d=>d.run)}};
    if (path === '/v1/approvals') return {data:{items:[{id:'runtime-permission',task_id:tasks[0].id,run_id:'implementation-0',kind:'COMMAND_EXECUTION',status:'PENDING'},{id:'inspection-input',task_id:'',run_id:'run-inspection',kind:'AGENT_INPUT',status:'PENDING'}]}};
    const revisionMatch=path.match(/^\/ui\/api\/tasks\/([^/]+)\/definition-revisions\/([^/]+)$/);
    if(revisionMatch){const task=workspace.tasks.find(t=>t.id===revisionMatch[1]),revision=task?.presentation.taskView.details.definitionRevisions.find(r=>r.id===revisionMatch[2]);if(!revision||query.get('project_id')!=='project')return error(404,'Revision owner mismatch');return {data:{...revision,taskId:task.id,definition:{workSpec:{goal:'Frozen specification for '+revision.id},verificationSpec:{mode:'COMMANDS',commands:['go test ./...']}}}};}
    const taskRuns = path.match(/^\/ui\/api\/tasks\/([^/]+)\/runs$/);
    if (taskRuns) {
      if(query.get('project_id')!=='project') return error(404,'Project owner mismatch');
      const task = workspace.tasks.find(t=>t.id===taskRuns[1]); if(!task) return error(404,'Task not found');
      if(scenario==='partial-history') return error(503,'Fixture: historical Runs temporarily unavailable');
      const limit=Math.min(100,Math.max(1,Number(query.get('limit')||20))),offset=Math.max(0,Number(query.get('offset')||0)),items=task.presentation.taskView.details.runs.filter(r=>!query.get('purpose')||r.runPurpose===query.get('purpose'));
      return {data:{items:items.slice(offset,offset+limit),total:items.length,limit,offset}};
    }
    const match=path.match(/^\/v1\/runs\/([^/]+)(\/prompt)?$/);
    if(match) {
      const detail=runDetails.get(match[1]);
      if(!detail || query.get('project_id')!=='project') return error(404,'Run not found in project');
      if(query.has('task_id') && query.get('task_id')!==detail.run.task_id) return error(404,'Run owner mismatch');
      if(scenario==='partial-history' && match[1].includes('review')) return error(503,'Fixture: historical detail unavailable');
      return {data:match[2]?{run_id:match[1],purpose:detail.run.purpose,available:true,prompt:'Persisted fixture prompt — <script>escaped, never executed</script>',bytes:76,sha256:'fixture-sha256'}:detail};
    }
    return null;
  };
  return {read,runDetails};
}
