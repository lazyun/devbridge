package mcpserver

import (
	"strings"
	"testing"
)

func TestDecisionAppUsesStandardBoundedSemanticFollowUpAfterDurableDecision(t *testing.T) {
	html := decisionUIHTML
	for _, required := range []string{
		"protocolVersion:'2026-01-26'",
		"hostCapabilities.message",
		"request('ui/message',{role:'user',content:[{type:'text'",
		"prompt.slice(0,1024)",
		"the pending scope expansion",
		"prompt=text(v.message)",
		"continuation.featureAreaId+'|'+continuation.reviewId+'|'+continuation.runId+'|'+continuation.kind",
		"if(def.semanticAfter)await semantic(def.semanticAfter,projection(current))",
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("semantic continuation contract missing %q", required)
		}
	}
	toolCall := strings.Index(html, "const result=await request('tools/call'")
	semanticCall := strings.Index(html, "if(def.semanticAfter)await semantic")
	if toolCall < 0 || semanticCall < toolCall {
		t.Fatal("ui/message can be emitted before the durable decision tool succeeds")
	}
	start := strings.Index(html, "function semantic(")
	end := strings.Index(html[start:], "async function act(")
	if start < 0 || end < 0 {
		t.Fatal("semantic continuation function not found")
	}
	semanticBody := html[start : start+end]
	for _, forbidden := range []string{"tools/call", "/Users/", "/private/", "password", "token", "secret"} {
		if strings.Contains(semanticBody, forbidden) {
			t.Fatalf("semantic handoff contains state mutation, path, or secret mechanism %q", forbidden)
		}
	}
}

func TestDecisionAppRecoversOutcomeUnknownWithStableRequest(t *testing.T) {
	html := decisionUIHTML
	for _, required := range []string{"SUBMITTING", "FAILED", "OUTCOME_UNKNOWN", "RESOLVED", "error.outcomeUnknown=true", "Retry uses the same Decision identity"} {
		if !strings.Contains(html, required) {
			t.Fatalf("Decision recovery state machine missing %q", required)
		}
	}
	if strings.Contains(html, "localStorage") || strings.Contains(html, "sessionStorage") {
		t.Fatal("Decision retry identity leaked into browser-local persistence")
	}
}

func TestDecisionAppHandsOffTerminalScopeReviewExactlyOncePerMount(t *testing.T) {
	html := decisionUIHTML
	handoff := decisionFunction(t, html, "handoffScopeReview", "workflow")
	for _, required := range []string{
		"active.purpose!=='SCOPE_REVIEW'",
		"['SUCCEEDED','FAILED','INFRASTRUCTURE_FAILED','CANCELED'].includes(status)",
		"continuation.attempt!==true",
		"terminalHandoffs.has(key)",
		"terminalHandoffs.add(key)",
		"stopPolling()",
		"continuation.kind==='REVIEW_SUCCEEDED'?'review-success':'review-failure'",
		"semantic(kind,continuation)",
		"syncSize('scope-review-handoff')",
	} {
		if !strings.Contains(handoff, required) {
			t.Fatalf("Scope Review handoff contract missing %q", required)
		}
	}
	if strings.Index(handoff, "terminalHandoffs.add(key)") > strings.Index(handoff, "semantic(kind") {
		t.Fatal("Scope Review handoff is not marked sent before ui/message")
	}
	for _, forbidden := range []string{"task_plan_create", "execution_proposal_confirm", "run_cancel", "agentClaim", "gitEvidence", "changedFiles", "/Users/", "/private/", "password", "secret"} {
		if strings.Contains(handoff, forbidden) {
			t.Fatalf("Scope Review handoff mutates workflow or leaks detailed state via %q", forbidden)
		}
	}
	workflow := decisionFunction(t, html, "workflow", "rememberIdentity")
	if !strings.Contains(workflow, "run(value,v);handoffScopeReview(v)") {
		t.Fatal("terminal Scope Review projection does not render before semantic handoff")
	}
	if strings.Count(html, "terminalHandoffs=new Set()") != 1 {
		t.Fatal("terminal handoff guard is not ephemeral and singular per mounted app")
	}
}

func TestDecisionAppDoesNotAutoContinueTerminalExecutionResults(t *testing.T) {
	run := decisionFunction(t, decisionUIHTML, "run", "stage")
	if !strings.Contains(run, "taskDone&&canMessage()") || !strings.Contains(run, "label:'Ask GPT to summarize',semanticOnly:'summary'") {
		t.Fatal("terminal execution interpretation is no longer user-driven")
	}
	if strings.Contains(run, "review-success") || strings.Contains(run, "review-failure") {
		t.Fatal("automatic Scope Review continuation leaked into generic terminal Run rendering")
	}
}

func TestDecisionAppKeepsActiveExecutionCancellationInChat(t *testing.T) {
	run := decisionFunction(t, decisionUIHTML, "run", "stage")
	if !strings.Contains(run, "else if(!scopeReview&&!terminal&&!runTerminal&&v.status!=='WAITING_APPROVAL'&&!(workflow&&workflow.agentExecution))") {
		t.Fatal("cancellation guidance is not limited to active Execution progress")
	}
	if !strings.Contains(run, "To cancel this Execution, ask in chat.") {
		t.Fatal("active Execution progress does not explain how to request cancellation")
	}
	for _, forbidden := range []string{"Cancel Task", "ui_execution_decide", "execution_cancel", "tool:'ui_task_decide'", "decision:'CONFIRM'", "decision:'EXECUTE'", "decision:'CANCEL'"} {
		if strings.Contains(run, forbidden) {
			t.Fatalf("active progress exposes a Task, Execution, or cancellation decision through %q", forbidden)
		}
	}
}

func TestDecisionAppUsesOneAppPrivatePollingLoopForInPlaceWorkflowState(t *testing.T) {
	for _, required := range []string{
		"name:'ui_workflow_state_get'",
		"if(pollTimer||pollInFlight)return",
		"pollTimer=setTimeout(pollWorkflow,2500)",
		"if(data.phase&&(data.task||data.reviewId))workflow(data)",
		"if(d.method==='ui/notifications/tool-result'){current=d.params&&d.params.structuredContent;render(current)}",
		"if(oldKey&&newKey!==oldKey)",
		"tool:'ui_approval_decide'",
		"ui/resource-teardown",
		"function teardown(){tornDown=true;stopPolling()",
		"stopPolling()",
		"No interactive Dev Bridge decision is required for this result.",
		"Ask GPT to summarize",
	} {
		if !strings.Contains(decisionUIHTML, required) {
			t.Fatalf("card continuation contract missing %q", required)
		}
	}
	poll := decisionFunction(t, decisionUIHTML, "pollWorkflow", "stopPolling")
	if !strings.Contains(poll, "name:'ui_workflow_state_get'") {
		t.Fatal("ordinary progress polling does not target the app-private projection")
	}
	for _, forbidden := range []string{"name:'run_get'", "name:'approval_list'", "name:'execution_proposal_get'"} {
		if strings.Contains(poll, forbidden) {
			t.Fatalf("ordinary progress polling still uses model-facing tool %q", forbidden)
		}
	}
	if strings.Count(decisionUIHTML, "setTimeout(pollWorkflow") != 1 {
		t.Fatal("workflow app has more than one polling timer creation path")
	}
}

func TestDecisionAppPollingLifecycleStopsAndResumesInSameWidget(t *testing.T) {
	html := decisionUIHTML
	ensure := decisionFunction(t, html, "ensurePolling", "pollWorkflow")
	stop := decisionFunction(t, html, "stopPolling", "scopeSummary")
	run := decisionFunction(t, html, "run", "stage")
	approval := decisionFunction(t, html, "approval", "expansion")
	for _, required := range []string{"pollingActive=true", "if(pollTimer||pollInFlight)return", "identityKey(workflowIdentity)"} {
		if !strings.Contains(ensure, required) {
			t.Fatalf("single-loop guard missing %q", required)
		}
	}
	for _, required := range []string{"pollingActive=false", "clearTimeout(pollTimer)", "pollGeneration++"} {
		if !strings.Contains(stop, required) {
			t.Fatalf("poll teardown missing %q", required)
		}
	}
	if !strings.Contains(run, "if(terminal)stopPolling();else ensurePolling()") {
		t.Fatal("terminal and live Run polling lifecycle is not explicit")
	}
	if !strings.Contains(approval, "if(live)ensurePolling()") {
		t.Fatal("Approval does not remain in the mounted workflow polling loop")
	}
}

func TestDecisionAppUsesCompactCollapsedInformationHierarchy(t *testing.T) {
	html := decisionUIHTML
	for _, required := range []string{
		"el('details','decision-details')",
		"el('summary','','Details')",
		"max-height:min(52vh,420px)",
		"-webkit-line-clamp:2",
		"constraints.slice(0,3)",
		"names.slice(0,4)",
		"Canceled task",
		"statusline",
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("compact/collapsed presentation contract missing %q", required)
		}
	}
}

func TestDecisionAppOrdersNegativeActionsBeforePositiveActions(t *testing.T) {
	html := decisionUIHTML
	actionHelper := decisionFunction(t, html, "actions", "canMessage")
	negativeGroup := strings.Index(actionHelper, "group('negative-actions',negative)")
	positiveGroup := strings.Index(actionHelper, "group('positive-actions',positive)")
	if !strings.Contains(actionHelper, "defs.filter(d=>d.intent==='negative')") || !strings.Contains(actionHelper, "defs.filter(d=>d.intent==='positive')") || negativeGroup < 0 || positiveGroup < negativeGroup {
		t.Fatal("shared action renderer does not freeze negative-before-positive DOM order")
	}

	approval := decisionFunction(t, html, "approval", "expansion")
	if !strings.Contains(approval, "negative=new Set(['DECLINE','CANCEL'])") || !strings.Contains(approval, "negative.has(d)?'negative':'positive'") {
		t.Fatal("Approval decisions are not partitioned into negative and authorization groups")
	}
	for _, mobileContract := range []string{"@media(max-width:430px)", ".action-row{flex-direction:column", ".positive-actions{margin-left:0}"} {
		if !strings.Contains(html, mobileContract) {
			t.Fatalf("mobile semantic action order missing %q", mobileContract)
		}
	}
}

func TestDecisionAppKeepsProgressFallbackAndDependenciesCompact(t *testing.T) {
	html := decisionUIHTML
	run := decisionFunction(t, html, "run", "stage")
	for _, required := range []string{
		"const changed=workflow?workflow.changedFileCount:(v.changedFiles||[]).length",
		"To cancel this Execution, ask in chat.",
		"details(card,runDetailRows(detail),runDiagnostics(detail))",
	} {
		if !strings.Contains(run, required) {
			t.Fatalf("compact progress/result contract missing %q", required)
		}
	}
	for _, required := range []string{"['AgentClaim',v.agentClaim||'None (not Evidence)']", "['Evidence',evidenceDetails(v.gitEvidence)]"} {
		if !strings.Contains(html, required) {
			t.Fatalf("run Details contract missing %q", required)
		}
	}
	for _, required := range []string{
		"function compactNotice(message){stopPolling();root.replaceChildren(el('div','statusline',message));syncSize('compact-state')}",
		"@media(prefers-color-scheme:dark)",
		"color:var(--color-text-primary",
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("compact fallback/theme contract missing %q", required)
		}
	}
	for _, forbidden := range []string{"localStorage", "sessionStorage", "https://", "http://", "fetch(", "XMLHttpRequest", "@font-face"} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("decision resource contains persistence or external dependency %q", forbidden)
		}
	}
}

func TestDecisionAppSynchronizesCurrentContentSizeIncludingShrink(t *testing.T) {
	html := decisionUIHTML
	for _, required := range []string{
		"ui/notifications/size-changed",
		"root.getBoundingClientRect()",
		"height:Math.ceil(rootRect.height+paddingTop+paddingBottom)",
		"resizeObserver.observe(root)",
		"syncSize('initial-render')",
		"syncSize('render')",
		"syncSize(section.open?'details-expanded':'details-collapsed')",
		"syncSize('compact-state')",
		"syncSize('notice')",
		"syncSize('scope-review-handoff')",
		"resizeObserver.disconnect()",
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("size synchronization contract missing %q", required)
		}
	}
	measurement := decisionFunction(t, html, "measuredContentSize", "syncSize")
	for _, forbidden := range []string{"scrollHeight", "clientHeight", "Math.max", "lastHeight", "minHeight"} {
		if strings.Contains(measurement, forbidden) {
			t.Fatalf("content measurement can preserve a stale larger height via %q", forbidden)
		}
	}
	for _, forbidden := range []string{"min-height:", "height:420px", "height:100vh"} {
		if strings.Contains(html, forbidden) {
			t.Fatalf("fixed/minimum layout defeats host shrinking: %q", forbidden)
		}
	}
	if strings.Contains(decisionFunction(t, html, "syncSize", "teardown"), "height >=") {
		t.Fatal("size notification only permits growth")
	}
	run := decisionFunction(t, html, "run", "stage")
	if !strings.Contains(run, "taskDone||(scopeReview&&terminal)") {
		t.Fatal("terminal Scope Review handoff does not use compact card sizing")
	}
}

func TestDecisionAppPreservesContinuationActionMappings(t *testing.T) {
	for _, mapping := range []string{
		"tool:'ui_approval_decide'",
		"if(def.semanticAfter)await semantic(def.semanticAfter,projection(current))",
	} {
		if !strings.Contains(decisionUIHTML, mapping) {
			t.Fatalf("continuation mapping changed or missing %q", mapping)
		}
	}
}

func TestDecisionAppUsesProductVocabularyAndOnlyAppDecisionTools(t *testing.T) {
	html := decisionUIHTML
	for _, required := range []string{
		"title:v.title",
		"shell('EXECUTION',actionable?'READY TO EXECUTE'",
		"title=scopeReview?(terminal?'Feature Area Review complete':'Feature Area Review')",
		"stage(stageBox,'Feature Area Review'",
		"stage(stageBox,'Implementation'",
		"stage(stageBox,'Verification'",
		"ui_approval_decide",
	} {
		if !strings.Contains(html, required) {
			t.Fatalf("product decision card contract missing %q", required)
		}
	}
	for _, obsolete := range []string{
		"tool:'task_plan_",
		"tool:'execution_proposal_",
		"tool:'scope_expansion_",
		"tool:'approval_decide'",
		"tool:'run_cancel'",
		"name:'run_get'",
	} {
		if strings.Contains(html, obsolete) {
			t.Fatalf("card still calls non-app decision tool %q", obsolete)
		}
	}
}

func decisionFunction(t *testing.T, html, name, next string) string {
	t.Helper()
	start := strings.Index(html, "function "+name+"(")
	end := strings.Index(html, "\nfunction "+next+"(")
	if end < 0 {
		end = strings.Index(html, "\nasync function "+next+"(")
	}
	if start < 0 || end < 0 || end <= start {
		t.Fatalf("could not isolate %s function", name)
	}
	return html[start:end]
}
