"use strict";

(function (root) {
  const statusMaps = {
    project: {
      READY: ["success", "status.ready"], NO_GUIDANCE: ["success", "status.noGuidance"], REQUIRED: ["waiting", "status.required"],
      RUNNING: ["active", "status.running"], FAILED: ["error", "status.error"]
    },
    task: {
      ACTIVE: ["active", "status.running"],
      PLANNING: ["active", "status.planning"], READY: ["neutral", "status.readyWork"], RUNNING: ["active", "status.running"],
      SUCCEEDED: ["success", "status.success"], FAILED: ["error", "status.error"], CANCELED: ["neutral", "status.canceled"]
    },
    run: {
      CREATED: ["neutral", "status.readyWork"], PREPARING: ["active", "status.running"], RUNNING: ["active", "status.running"],
      WAITING_APPROVAL: ["waiting", "status.waiting"], COLLECTING: ["active", "status.running"], SUCCEEDED: ["success", "status.success"],
      FAILED: ["error", "status.error"], INFRASTRUCTURE_FAILED: ["error", "status.error"], UNKNOWN_ORPHANED: ["warning", "status.orphaned"], CANCELED: ["neutral", "status.canceled"]
    },
    featureAreaProduct: {
      ACTIVE: ["active", "status.areaActive"], CLOSED: ["neutral", "status.areaClosed"]
    },
    product: {
      RUNNING: ["active", "status.running"], SUCCEEDED: ["success", "status.success"],
      CANCEL_REQUESTED: ["waiting", "status.canceling"], UNKNOWN_ORPHANED: ["warning", "status.orphaned"],
      FEATURE_AREA_IDLE:["neutral","status.areaIdle"],REVIEW_REQUIRED:["waiting","status.required"],REVIEW_RUNNING:["active","status.reviewing"],REVIEW_APPROVAL_REQUIRED:["waiting","status.reviewApproval"],
      REVIEW_NOT_USABLE:["error","status.error"],REVIEW_APPROVAL_STALE:["warning","status.conflict"],REVIEW_REVISION_REQUIRED:["warning","status.conflict"],REVIEW_APPROVAL_REJECTED:["neutral","status.canceled"],
      DEVELOPMENT_TASK_READY:["success","status.readyWork"],
      TASK_DELIVERING:["waiting","status.taskDelivering"],TASK_RECOVERY_REQUIRED:["warning","status.taskRecoveryRequired"],TASK_NO_RUN_SUCCESSOR_REQUIRED:["waiting","status.noRunSuccessorRequired"],TASK_IMPLEMENTATION_PREPARING:["active","status.successorPreparing"],REVIEW_MATERIALIZING:["active","status.reviewMaterializing"],FEATURE_AREA_BUSY:["active","status.areaBusy"],GIT_FINALIZATION_PENDING:["waiting","status.gitFinalization"],
      DEVELOPMENT_TASK_STARTING:["active","status.approvedExecution"],EXECUTION_HANDOFF_PENDING:["active","status.preparing"],IMPLEMENTATION_RUNNING:["active","status.executing"],VERIFICATION_RUNNING:["active","status.verifying"],
      FEATURE_AREA_RETIRING:["warning","topics.closing"],RUNTIME_APPROVAL_REQUIRED:["waiting","status.waiting"],RECOVERY_REQUIRED:["warning","status.continuityUnavailable"],
      READY_FOR_TASK: ["success", "status.readyForTask"], REVIEWING: ["active", "status.reviewing"],
      STARTING: ["active", "status.starting"], COMPLETE: ["success", "status.success"], BLOCKED: ["error", "status.blocked"],
      DRAFT: ["neutral", "status.draft"], PREPARING: ["active", "status.preparing"], AWAITING_CONFIRMATION: ["waiting", "status.awaitingConfirmation"],
      AWAITING_AUTHORIZATION: ["waiting", "status.awaitingAuthorization"], CONFLICT: ["warning", "status.conflict"],
      READY_TO_EXECUTE: ["success", "status.readyToExecute"], READY: ["success", "status.ready"], EXECUTING: ["active", "status.executing"],
      VERIFYING: ["active", "status.verifying"], COMPLETED: ["success", "status.success"], FAILED: ["error", "status.error"],
      CANCELED: ["neutral", "status.canceled"], UNAVAILABLE: ["neutral", "status.unknown"], CONTINUITY_UNAVAILABLE: ["warning", "status.continuityUnavailable"]
    },
    level: {DEBUG: ["neutral", "status.ready"], INFO: ["neutral", "status.ready"], WARN: ["warning", "status.warning"], ERROR: ["error", "status.error"]}
    ,
    goal: {
      PENDING: ["neutral", "status.goalPending"], RUNNING: ["active", "status.running"],
      COMPLETED: ["success", "status.success"], FAILED: ["error", "status.error"], SKIPPED: ["neutral", "status.canceled"]
    }
  };

  function presentStatus(domain, raw, t) {
    const entry = statusMaps[domain] && statusMaps[domain][String(raw || "").toUpperCase()];
    const category = entry ? entry[0] : "neutral";
    const key = entry ? entry[1] : "status.unknown";
    return {category, label: t(`${key}.label`), explanation: t(`${key}.explanation`), raw: raw || ""};
  }

  const phaseKeys = {REVIEW_REQUIRED:"reviewRequired",REVIEWING:"reviewing",REVIEW_APPROVAL_PENDING:"approval",IMPLEMENTATION_READY:"implementationReady",NO_RUN_SUCCESSOR_REQUIRED:"successorRequired",IMPLEMENTATION_PREPARING:"preparing",IMPLEMENTING:"implementing",VERIFICATION_READY:"verificationReady",VERIFYING:"verifying",ACCEPTANCE_PENDING:"acceptance",DELIVERING:"delivering",RECOVERY_REQUIRED:"recovery"};
  function taskPhaseLabel(task, t) {
    if(task?.verificationDecisions?.hold)return "已暂存";
    const completion=task?.verificationDecisions?.completion;if(completion)return completion.state==="COMPLETED"?"已完成并合入":completion.state==="BLOCKED"?"合入受阻":"正在合入";
    if (["SUCCEEDED","FAILED","CANCELED"].includes(task?.status)) return presentStatus("task", task.status, t).label;
    if(task?.phase==="RECOVERY_REQUIRED"&&task.businessPhase&&task.businessPhase!=="RECOVERY_REQUIRED"&&phaseKeys[task.businessPhase]){
      const neutral={REVIEWING:"workbench.task.reviews",IMPLEMENTING:"phase.implementation",VERIFYING:"phase.verification"};
      return t(neutral[task.businessPhase]||"taskPhase."+phaseKeys[task.businessPhase]);
    }
    return phaseKeys[task?.phase] ? t("taskPhase."+phaseKeys[task.phase]) : task?.phaseLabel || task?.phase || t("workbench.unknown");
  }
  function runPurposeLabel(purpose,t) {
    return ["SCOPE_REVIEW","IMPLEMENTATION","VERIFICATION","INSPECTION"].includes(purpose) ? t("runPurpose."+purpose) : purpose || t("workbench.unknown");
  }
  function progressLabel(status,t,phase) {
    if(status==="BLOCKED"&&phase==="VERIFICATION")return t("progress.INCOMPLETE");
    return ["COMPLETE","CURRENT","PENDING","FAILED","CANCELED","BLOCKED"].includes(status) ? t("progress."+status) : status;
  }
  const exported = {statusMaps, presentStatus, taskPhaseLabel, runPurposeLabel, progressLabel};
  root.DevBridgeStatus = exported;
  if (typeof module !== "undefined" && module.exports) module.exports = exported;
})(typeof globalThis !== "undefined" ? globalThis : this);
