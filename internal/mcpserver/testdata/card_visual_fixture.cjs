// Isolated presentation fixtures. These are snapshots, never a workflow engine.
const states = {
 FEATURE_AREA_IDLE:["IDLE","当前无运行中的 Task","WAIT_FOR_USER_TASK"],
 FAILED:['IMPLEMENTATION','Implementation failed.','INSPECT_FAILURE'],
 CANCELED:['IMPLEMENTATION','Execution canceled.','WORKFLOW_CANCELED'],
 POSSIBLY_STALLED:['IMPLEMENTATION','Implementation is running.','WAIT_FOR_IMPLEMENTATION_COMPLETION'],
 WAITING_FOR_PERMISSION:['IMPLEMENTATION','Permission request is pending.','RESOLVE_RUNTIME_APPROVAL'],
 RECOVERY_RESTART_REQUIRED:['IMPLEMENTATION','Restart Core to fence the interrupted runtime, then call agent_operation_recover.','RECOVER_AGENT_OPERATION'],
 REVIEW_REQUIRED:['REVIEW','Start a FeatureArea Review.','START_FEATURE_AREA_REVIEW'],
 REVIEW_RUNNING:['REVIEW','Review is running.','WAIT_FOR_REVIEW_COMPLETION'],
 REVIEW_APPROVAL_REQUIRED:['APPROVAL','Review 已完成，等待实施审批。','WAIT_FOR_USER_APPROVAL'],
 DEVELOPMENT_TASK_READY:['TASK','Approved Task is ready for Implementation.','WAIT_FOR_IMPLEMENTATION_START'],
 DEVELOPMENT_TASK_STARTING:['TASK','Starting the approved implementation.','WAIT_FOR_IMPLEMENTATION_START'],
 IMPLEMENTATION_RUNNING:['IMPLEMENTATION','Implementation is running.','WAIT_FOR_IMPLEMENTATION_COMPLETION'],
 VERIFICATION_RUNNING:['VERIFICATION','Verifying the implementation.','WAIT_FOR_VERIFICATION_COMPLETION'],
 COMPLETE:['VERIFICATION','Development and verification completed.','WORKFLOW_COMPLETE'],
 RETIRING:['IMPLEMENTATION','Closing this FeatureArea.','WAIT_FOR_RETIREMENT'],
 RETIRED:['VERIFICATION','This FeatureArea is closed.','WORKFLOW_CLOSED'],
 RUNTIME_APPROVAL_REQUIRED:['IMPLEMENTATION','The agent requests permission.','RESOLVE_RUNTIME_APPROVAL'],
 RECOVERY_REQUIRED:['IMPLEMENTATION','Interrupted operation requires recovery.','RECOVER_AGENT_OPERATION']
};
function fixture(state='REVIEW_APPROVAL_REQUIRED',long=false){
 if(state==='FEATURE_AREA_IDLE'){
 const v=fixture('COMPLETE',long),p=v.presentation;v.selectedTaskId='';v.selectedWork=null;p.latestTaskView=p.taskView;delete p.taskView;delete p.taskStages;p.productProgress=[];p.currentState=p.workflowStatus='FEATURE_AREA_IDLE';p.currentPhase='IDLE';p.currentTask=null;p.latestTask={id:'task-a',status:'SUCCEEDED'};p.details.taskId='';p.workflowGuidance={currentState:'FEATURE_AREA_IDLE',taskId:'',sourceSequence:10,generation:p.generation};p.nextAction={code:'WAIT_FOR_USER_TASK',message:'当前无运行中的 Task'};p.capabilities=['CREATE_TASK','RETIRE','RESET_AGENT_CONTEXT'].map(action=>({action,available:true}));p.taskOptions={models:['fixture-model'],reasoningEfforts:['medium']};return v;
 }

 const [phase,message,action]=states[state],phases=['REVIEW','APPROVAL','TASK','IMPLEMENTATION','VERIFICATION'],index=phases.indexOf(phase),review=index<2;
 const task={id:'task-a',featureAreaId:'area-a',name:long?'Hot-reload runtime model configuration across distributed coding-agent environments while preserving in-flight requests':'Hot-reload runtime model configuration',status:'RUNNING',work:{type:'DEVELOPMENT',phase,status:state==='RETIRED'?'CANCELED':state==='RECOVERY_REQUIRED'?'UNKNOWN':state==='COMPLETE'?'SUCCEEDED':'RUNNING',readOnly:state==='RETIRED'}};
 task.status=task.work.status;
 if(state==='REVIEW_APPROVAL_REQUIRED')task.status=task.work.status='SUCCEEDED';
 if(['DEVELOPMENT_TASK_READY','DEVELOPMENT_TASK_STARTING'].includes(state))task.status=task.work.status='READY';
 const decision=state==='REVIEW_APPROVAL_REQUIRED'?{kind:'REVIEW_APPROVAL',id:'REVIEW_APPROVAL:review-a',subjectId:'task-a',summary:message,allowedActions:['APPROVE','REVISE','REJECT']}:state==='RUNTIME_APPROVAL_REQUIRED'?{kind:'RUNTIME_APPROVAL',id:'approval-a',subjectId:'approval-a',summary:message,allowedActions:['APPROVE_ONCE','APPROVE_FOR_SESSION','DECLINE']}:null;
 const capabilities=(decision?decision.allowedActions.map(action=>({action,available:true,target:{featureAreaId:'area-a',decisionId:decision.id,subjectId:decision.subjectId}})):[]);
 if(state==='RECOVERY_REQUIRED')capabilities.push({action:'RECOVER',available:true,target:{featureAreaId:'area-a',operationKind:'EXECUTION',operationId:'execution-a',runId:'run-a',agentConversationId:'conversation-a'}});
 const status=['RETIRING','RETIRED'].includes(state)?state:state==='REVIEW_REQUIRED'?'DISCOVERING':'ACTIVE';
 const feature={id:'area-a',name:long?'Dynamic coding-agent model support for multi-repository engineering workspaces with long-lived review history':'Dynamic coding-agent model support',status};
 const projection={featureArea:{id:feature.id,name:feature.name},lifecycleStatus:status,workflowStatus:state,currentState:state,currentPhase:phase,progress:phases.map((phase,i)=>({phase,status:state==='COMPLETE'?'COMPLETE':i<index?'COMPLETE':i===index?(state==='RETIRED'?'CANCELED':state==='RECOVERY_REQUIRED'?'UNKNOWN':'CURRENT'):'PENDING'})),nextAction:{code:action,message},workflowGuidance:{currentState:state,taskId:'task-a',generation:'fixture-'+state},pendingDecision:decision,capabilities,blockers:[],details:{featureAreaId:'area-a',taskId:'task-a'}};
 if(state==='RECOVERY_REQUIRED')projection.recovery={required:true,status:'RECOVERY_REQUIRED',summary:'UNKNOWN_ORPHANED · Authority revoked; inspect the interrupted operation.'};
 if(['RETIRING','RETIRED'].includes(state))projection.retirement={summary:state==='RETIRING'?'正在停止当前任务和审查':'历史记录可继续浏览。'};

 const recovery=['RECOVERY_REQUIRED','RECOVERY_RESTART_REQUIRED'].includes(state), failed=state==='FAILED'||recovery,canceled=state==='CANCELED'||state==='RETIRED';
 const generation='fixture-'+state;
 projection.generation=generation;projection.sourceSequence=10;projection.workflowGuidance.sourceSequence=10;projection.details.projectId='project';
 if(long)task.name='让 Dev Bridge 在服务运行时动态修改 Coding Agent configuration '+ 'veryLongUnbrokenTaskIdentifier'.repeat(7);
 if(failed)task.work.status=task.status='FAILED';if(canceled)task.work.status=task.status='CANCELED';
 if(recovery)projection.recovery={required:true,status:'RECOVERY_REQUIRED',prerequisite:state==='RECOVERY_RESTART_REQUIRED'?'CORE_RESTART':'',summary:message};
 if(state==='RECOVERY_RESTART_REQUIRED')capabilities.push({action:'RECOVER',available:false,reason:message});
 if(!review){
  const stages=['IMPLEMENTATION','VERIFICATION'].map((v,i)=>({phase:v,status:state==='COMPLETE'?'SUCCEEDED':i<index-3?'SUCCEEDED':i===index-3?(failed?'INFRASTRUCTURE_FAILED':canceled?'CANCELED':['DEVELOPMENT_TASK_READY','DEVELOPMENT_TASK_STARTING'].includes(state)?'PENDING':'RUNNING'):'PENDING',sourceId:i===0?'run-a':'verify-a'}));
  if(['DEVELOPMENT_TASK_READY','DEVELOPMENT_TASK_STARTING'].includes(state))stages.forEach(v=>v.status='PENDING');
  if(state==='RETIRED'){stages[0].status='CANCELED';stages[1].status='PENDING';}
  projection.taskStages={taskId:'task-a',title:task.name,status:task.status,phase,approved:true,stages,readOnly:['RETIRING','RETIRED'].includes(state),recovery:projection.recovery};
  projection.productProgress=['REVIEW','APPROVAL','TASK','RUN'].map(v=>({phase:v,status:v!=='RUN'?'COMPLETE':state==='COMPLETE'?'COMPLETE':recovery?'RECOVERY_REQUIRED':failed?'FAILED':canceled?'CANCELED':['DEVELOPMENT_TASK_READY','DEVELOPMENT_TASK_STARTING'].includes(state)?'PENDING':'RUNNING'}));
  projection.details.runId=index===4?'verify-a':index===3?'run-a':'';
  if(!['DEVELOPMENT_TASK_READY','DEVELOPMENT_TASK_STARTING','COMPLETE','RETIRED'].includes(state))projection.agentRuntime={runId:projection.details.runId,threadId:'hidden-thread',turnId:'hidden-turn',planTotal:5,planCompleted:2,step:'Updating MCP integration',currentItemType:'commandExecution',waitingState:state==='WAITING_FOR_PERMISSION'?'WAITING_FOR_PERMISSION':'RUNNING',agentHealth:state==='POSSIBLY_STALLED'?'POSSIBLY_STALLED':'ACTIVE',lastActivityAt:new Date(Date.now()-(state==='POSSIBLY_STALLED'?43*60000:8000)).toISOString()};
  if(['IMPLEMENTATION_RUNNING','VERIFICATION_RUNNING','POSSIBLY_STALLED'].includes(state))capabilities.push({action:'CANCEL_EXECUTION',available:true,target:{featureAreaId:'area-a',executionId:'execution-a',runId:'run-a'}});
 }
 if(state==='CANCELED')projection.cancellation={state:'CANCELED',runId:'run-a'};
 if(state==='REVIEW_REQUIRED'){projection.details.taskId='';projection.workflowGuidance.taskId='';}

 const terminal=['COMPLETE','FAILED','CANCELED','RETIRED'].includes(state);
 const stages=projection.taskStages?.stages||[{phase:'REVIEW',status:review?'RUNNING':'PENDING'}];
 const rail=review?[{phase:'REVIEW',status:state==='REVIEW_APPROVAL_REQUIRED'?'COMPLETE':'RUNNING'},{phase:'APPROVED',status:state==='REVIEW_APPROVAL_REQUIRED'?'CURRENT':'PENDING'},{phase:'IMPLEMENTATION',status:'PENDING'},{phase:'VERIFICATION',status:'PENDING'}]:[{phase:'REVIEW',status:'COMPLETE'},{phase:'APPROVED',status:'COMPLETE'},...stages];
 rail.push({phase:'DONE',status:terminal?(state==='COMPLETE'?'SUCCEEDED':task.status):'PENDING'});
 projection.productProgress=rail;
 if(state!=='REVIEW_REQUIRED')projection.taskView={id:'task-a',title:task.name,goal:'Make coding-agent configuration editable and preserve workspace continuity.',kind:'DEVELOPMENT',status:terminal?(state==='COMPLETE'?'SUCCEEDED':task.status):'RUNNING',terminal,readOnly:terminal,progress:rail,activity:{status:terminal?'SUCCEEDED':state==='WAITING_FOR_PERMISSION'?'WAITING_FOR_PERMISSION':'RUNNING',summary:terminal?'Codex 工作已结束':state==='WAITING_FOR_PERMISSION'?'等待权限确认':state==='POSSIBLY_STALLED'?'较长时间没有新活动':'正在执行命令',elapsedMs:482000,nativePlan:{completed:2,total:5,steps:[{step:'Inspect configuration ownership',status:'completed'},{step:'Update shared presentation',status:'completed'},{step:'Run regression checks',status:'inProgress'}]}},goals:[{title:'Preserve FeatureArea workspace',status:'COMPLETED'},{title:'Unify Task presentation',status:terminal?'COMPLETED':'RUNNING'}],usage:{status:'UNAVAILABLE',durationMs:1912000,implementationMs:1430000,verificationMs:482000,otherMs:0,durationComplete:true,runCount:2,unavailableRuns:['verify-a'],pendingRuns:[]},result:terminal?{status:state==='COMPLETE'?'SUCCEEDED':task.status,summary:'Configuration changes are preserved. Regression checks completed.'}:null,verification:state==='COMPLETE'?{status:'REPORTED',summary:'Agent Run completed. Check environment was unavailable; see report.'}:null,actions:terminal?[]:capabilities.filter(c=>c.available),details:{taskDefinitionRevisionId:'revision-a',model:'fixture',reasoningEffort:'high',runs:[{id:'run-a',executionId:'execution-a',workspaceId:'workspace-a'},{id:'verify-a',executionId:'execution-a',workspaceId:'workspace-a'}]}};
 if(state==='COMPLETE')Object.assign(projection.taskView,{overallState:'SUCCEEDED',currentExecution:{id:'execution-a',outcome:'SUCCEEDED',taskDefinitionRevisionId:'revision-a',implementationRunId:'run-a',verificationRunId:'verify-a'},verificationRun:{id:'verify-a',transportStatus:'SUCCEEDED'},verificationReport:{version:2,availability:'AVAILABLE',summary:'Verification report delivered; an environment prerequisite was unavailable.',checks:[{index:0,kind:'TEST_COMMAND',reported:{status:'BLOCKED',summary:'vite was unavailable',reason:'MISSING_TOOL'}}],acceptanceCriteria:[]},currentExecutionAcceptance:{decisionId:'TASK_ACCEPTANCE:execution-a',status:'ACCEPTED',allowedActions:[],receipt:{option:'ACCEPT'}},executionHistory:[{execution:{id:'execution-a',outcome:'SUCCEEDED'},acceptance:{option:'ACCEPT'}}]});
 if(review&&projection.taskView){projection.details.runId='review-run';projection.taskView.details.runs=[{id:'review-run',executionId:'review:review-a',workspaceId:'workspace-a'}];projection.taskView.activity.nativePlan=null;projection.taskView.phase=state==='REVIEW_APPROVAL_REQUIRED'?'REVIEW_APPROVAL_PENDING':'REVIEWING';projection.taskView.status='ACTIVE';task.status='ACTIVE';task.phase=projection.taskView.phase;}
 return {schemaVersion:'1',developmentContext:{project:{id:'project',name:'devbridge'},repositories:[{repository:{id:'repo',name:'devbridge'},developmentCycleId:'cycle',branch:long?'dev_20260908_1913/long-lived-development-branch-for-dynamic-agent-model-configuration':'dev_20260908_1913',developmentRef:'refs/heads/dev_20260908_1913',revision:'da0b6a330e954dbee2d8c43287f7d19a5eca17b7',checkoutMatches:false,refChanged:true}]},featureAreas:[feature,{id:'area-b',name:'Historical feature',status:'RETIRED'}],selectedFeatureAreaId:'area-a',selectedTaskId:state==='REVIEW_REQUIRED'?'':'task-a',tasks:state==='REVIEW_REQUIRED'?[]:[task],selectedWork:state==='REVIEW_REQUIRED'?null:{kind:state.startsWith('REVIEW')||state==='AWAITING_APPROVAL'?'REVIEW':'EXECUTION',operationId:state.startsWith('REVIEW')||state==='AWAITING_APPROVAL'?'review-a':'execution-a',ownerTaskId:'task-a',runId:'run-a',status:task.status,updatedAt:task.updatedAt},presentation:projection};
}
const branches={projectId:'project',availableActions:['development_branch_list','development_branch_select','development_branch_create'],repositories:[{repositoryId:'repo',repositoryCheckoutRef:'refs/heads/main',repositoryCheckoutMatchesDevelopmentRef:false,branches:[{fullRef:'refs/heads/dev_20260908_1913',shortName:'dev_20260908_1913',headSha:'da0b6a330e954dbee2d8c43287f7d19a5eca17b7'},{fullRef:'refs/heads/main',shortName:'main',headSha:'abcdef1234567890'}]}]};
module.exports={states,fixture,branches};
