package workflowpresentation

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// RenderText renders a complete, client-independent textual representation of
// p. It formats only the facts already present in the projection.
func RenderText(p Projection) string {
	var b strings.Builder
	if p.PendingDecision != nil {
		writeString(&b, "Waiting for user decision", p.PendingDecision.Summary)
		writeStrings(&b, "Allowed decision actions", p.PendingDecision.AllowedActions)
	}
	if p.DirectFile != nil {
		writeString(&b, "Direct File authorization", p.DirectFile.State)
		writeString(&b, "Direct File workflow", p.DirectFile.WorkflowID)
		writeString(&b, "Direct File blocked", p.DirectFile.FailureReason)
		for _, m := range p.DirectFile.RepositoryTargets {
			writeString(&b, "Repository "+m.RepositoryID, m.State+" · read="+strconv.FormatBool(m.Read)+" · write="+m.Write.Kind)
			writeStrings(&b, "Writable paths", m.Write.Paths)
		}
	}
	if p.CommitView != nil {
		writeString(&b, "FeatureArea submission", p.CommitView.Summary)
		writeString(&b, "Submission availability", p.CommitView.Reason)
	}
	if p.CurrentTask == nil && p.CurrentState == "FEATURE_AREA_IDLE" {
		b.WriteString("FeatureArea ACTIVE / idle · 当前无运行中的 Task\n")
	}
	if p.LatestTask != nil {
		writeString(&b, "Latest Task", p.LatestTask.ID+" · "+p.LatestTask.Status)
	}
	if p.LatestTaskView != nil {
		v := p.LatestTaskView
		writeString(&b, "Latest Task title", v.Title)
		writeString(&b, "Latest Task Code", v.CommitSummary)
		writeString(&b, "Latest Task Dev", v.DevSummary)
		writeString(&b, "Latest Task token availability", string(v.Usage.Status))
		if v.Result != nil {
			writeString(&b, "Latest Task result", v.Result.Status+" · "+v.Result.Summary)
		}
	}
	if p.TaskView != nil {
		v := p.TaskView
		writeString(&b, "Task", v.Title)
		writeString(&b, "Goal", v.Goal)
		writeString(&b, "Status", v.Status)
		writeString(&b, "Task phase", string(v.Phase))
		writeString(&b, "Task phase label", v.PhaseLabel)
		writeString(&b, "Code", v.CommitSummary)
		writeString(&b, "Dev", v.DevSummary)
		writeString(&b, "Commit outcome", v.CommitErrorSummary)
		writeString(&b, "Task overall state", v.OverallState)
		if v.CurrentExecution != nil {
			writeString(&b, "Current Execution", v.CurrentExecution.ID+" · "+v.CurrentExecution.State+" · "+v.CurrentExecution.Outcome)
			writeString(&b, "Current Execution failure code", v.CurrentExecution.FailureCode)
		}
		if v.ImplementationRun != nil {
			writeString(&b, "Implementation failureCode", v.ImplementationRun.FailureCode)
			writeString(&b, "Implementation Run transport", v.ImplementationRun.ID+" · "+v.ImplementationRun.TransportStatus)
		}
		if v.VerificationRun != nil {
			writeString(&b, "Verification failureCode", v.VerificationRun.FailureCode)
			writeString(&b, "Verification Run transport", v.VerificationRun.ID+" · "+v.VerificationRun.TransportStatus)
		}
		if v.VerificationReport != nil {
			writeString(&b, "Verification Report", string(v.VerificationReport.Availability)+" · "+v.VerificationReport.Summary)
			facts, _ := json.Marshal(v.VerificationReport)
			writeString(&b, "Verification evidence", string(facts))
		}
		if v.CurrentExecutionAcceptance != nil {
			writeString(&b, "Current Execution acceptance", v.CurrentExecutionAcceptance.Status+" · "+v.CurrentExecutionAcceptance.DecisionID)
			if v.CurrentExecutionAcceptance.Receipt != nil {
				writeString(&b, "Current Execution acceptance receipt", v.CurrentExecutionAcceptance.Receipt.Option+" · "+v.CurrentExecutionAcceptance.Receipt.ResultKind+" · "+v.CurrentExecutionAcceptance.Receipt.ResultID)
			}
		}
		for _, history := range v.ExecutionHistory {
			status := "UNRECORDED"
			if history.Acceptance != nil {
				status = history.Acceptance.Option
			}
			writeString(&b, "Execution history", history.Execution.ID+" · "+history.Execution.Outcome+" · "+status)
		}
		for _, phase := range v.Progress {
			writeString(&b, "Progress", phase.Phase+" · "+phase.Status)
		}
		writeString(&b, "Task activity", v.Activity.Status+" · "+v.Activity.Summary)
		writeString(&b, "Task activity operation", v.Activity.OperationKind+" · "+v.Activity.OperationID+" · "+v.Activity.RunID)
		writeString(&b, "Task activity failure code", v.Activity.FailureCode)
		writeString(&b, "Token availability", string(v.Usage.Status))
		writeString(&b, "Duration ms", strconv.FormatInt(v.Usage.DurationMS, 10))
		if v.Result != nil {
			writeString(&b, "Result", v.Result.Status+" · "+v.Result.Summary)
		}
		if v.Usage.Status == "UNAVAILABLE" {
			b.WriteString("Token 用量无法完整可靠归因\n")
		}
		if v.Usage.Tokens != nil {
			tokens, _ := json.Marshal(v.Usage.Tokens)
			writeString(&b, "Tokens", string(tokens))
		}
		for _, g := range v.Goals {
			writeString(&b, "Goal Progress", g.Title+" · "+g.Status)
		}
	}

	if p.TaskView != nil {
		b.WriteString("Advanced details:\n")
	}
	b.WriteString("Feature area:\n")
	writeIdentity(&b, "  ", p.FeatureArea)
	writeString(&b, "  Product status", p.Product.DomainStatus)
	writeString(&b, "  Internal status", p.Product.InternalStatus)
	writeString(&b, "  Task phase", p.Product.TaskPhase)
	if p.Product.OperationActivity != nil {
		writeString(&b, "  Operation activity", p.Product.OperationActivity.State+" · "+p.Product.OperationActivity.Message)
	}

	b.WriteString("Development context:\n")
	b.WriteString("  Project:\n")
	writeIdentity(&b, "    ", p.DevelopmentContext.Project)
	writeBool(&b, "  Selection required", p.DevelopmentContext.SelectionRequired)
	writeBool(&b, "  Ready", p.DevelopmentContext.Ready)
	b.WriteString("  Repositories:\n")
	if len(p.DevelopmentContext.Repositories) == 0 {
		b.WriteString("    (none)\n")
	}
	for _, repository := range p.DevelopmentContext.Repositories {
		b.WriteString("    Repository:\n")
		writeIdentity(&b, "      ", repository.Repository)
		writeString(&b, "      Development cycle ID", repository.DevelopmentCycleID)
		writeString(&b, "      Development ref", repository.DevelopmentRef)
		writeString(&b, "      Branch", repository.Branch)
		writeString(&b, "      Revision", repository.Revision)
		writeString(&b, "      Selected revision", repository.SelectedRevision)
		writeString(&b, "      Status", repository.Status)
		writeBool(&b, "      Selection required", repository.SelectionRequired)
		writeOptionalBool(&b, "      Ref changed", repository.RefChanged)
		writeOptionalBool(&b, "      Checkout matches", repository.CheckoutMatches)
	}

	writeString(&b, "Lifecycle status", p.LifecycleStatus)
	if p.Retirement != nil {
		writeString(&b, "Retirement progress", p.Retirement.Summary)
		writeString(&b, "Retirement requested at", p.Retirement.RequestedAt.Format(time.RFC3339Nano))
		writeBool(&b, "Engineering available", false)
	}
	writeString(&b, "Workflow status", p.WorkflowStatus)
	writeString(&b, "Current phase", p.CurrentPhase)

	if p.TaskView == nil {
		b.WriteString("Progress:\n")
		if len(p.Progress) == 0 {
			b.WriteString("  (none)\n")
		}
		for _, progress := range p.Progress {
			b.WriteString("  Phase:\n")
			writeString(&b, "    Name", progress.Phase)
			writeString(&b, "    Status", progress.Status)
			writeString(&b, "    Source ID", progress.SourceID)
			writeInt(&b, "    Ordinal", progress.Ordinal)
		}

	}

	b.WriteString("Current operation:\n")
	if p.CurrentOperation == nil {
		b.WriteString("  (none)\n")
	} else {
		writeString(&b, "  Kind", p.CurrentOperation.Kind)
		writeString(&b, "  ID", p.CurrentOperation.ID)
		writeString(&b, "  Run ID", p.CurrentOperation.RunID)
		writeString(&b, "  Status", p.CurrentOperation.Status)
	}

	b.WriteString("Pending decision:\n")
	if p.PendingDecision == nil {
		b.WriteString("  (none)\n")
	} else {
		writeString(&b, "  Kind", p.PendingDecision.Kind)
		writeString(&b, "  ID", p.PendingDecision.ID)
		writeString(&b, "  Subject ID", p.PendingDecision.SubjectID)
		writeString(&b, "  Summary", p.PendingDecision.Summary)
		writeString(&b, "  Guarantee", p.PendingDecision.Guarantee)
		writeString(&b, "  Cancel effect", p.PendingDecision.CancelEffect)
		writeString(&b, "  Requested permissions", string(p.PendingDecision.Permissions))
		writeStrings(&b, "  Declared allowed actions", p.PendingDecision.AllowedActions)
	}

	b.WriteString("Recovery:\n")
	if p.Recovery == nil {
		b.WriteString("  (none)\n")
	} else {
		writeString(&b, "  Status", p.Recovery.Status)
		writeBool(&b, "  Required", p.Recovery.Required)
		writeString(&b, "  Summary", p.Recovery.Summary)
	}

	if p.Cancellation != nil {
		b.WriteString("Cancellation:\n")
		writeString(&b, "  State", p.Cancellation.State)
		writeString(&b, "  Run ID", p.Cancellation.RunID)
		writeBool(&b, "  Recovery required", p.Cancellation.RecoveryRequired)
	}
	writeStrings(&b, "Blockers", p.Blockers)
	b.WriteString("Capabilities:\n")
	if len(p.Capabilities) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, capability := range p.Capabilities {
		b.WriteString("  Capability:\n")
		writeString(&b, "    Action", capability.Action)
		writeBool(&b, "    Available", capability.Available)
		writeString(&b, "    Decision option", capability.DecisionOption)
		writeString(&b, "    Reason", capability.Reason)
		if capability.Target != nil {
			target, _ := json.Marshal(capability.Target)
			writeString(&b, "    Exact target", string(target))
		}
	}
	writeStrings(&b, "Available actions", AvailableActions(p))

	if p.CommitView != nil {
		facts, _ := json.Marshal(p.CommitView.Details)
		writeString(&b, "Commit details", string(facts))
	}
	if p.TaskView != nil {
		facts, _ := json.Marshal(p.TaskView.Details)
		writeString(&b, "Task details", string(facts))
	}
	b.WriteString("Details:\n")
	writeString(&b, "  View", p.Details.View)
	writeString(&b, "  Project ID", p.Details.ProjectID)
	writeString(&b, "  Feature area ID", p.Details.FeatureAreaID)
	writeString(&b, "  Review ID", p.Details.ReviewID)
	writeString(&b, "  Task ID", p.Details.TaskID)
	writeString(&b, "  Task definition revision ID", p.Details.TaskDefinitionRevisionID)
	writeString(&b, "  Task plan ID", p.Details.TaskPlanID)
	writeString(&b, "  Execution ID", p.Details.ExecutionID)
	writeString(&b, "  Run ID", p.Details.RunID)
	writeString(&b, "  Baseline", p.Details.Baseline)
	writeString(&b, "Updated at", p.UpdatedAt.Format(time.RFC3339Nano))

	return b.String()
}

// AvailableActions returns the actions whose authoritative capability is
// explicitly available. It preserves projection order and does not infer
// availability from decisions, statuses, or action names.
func AvailableActions(p Projection) []string {
	actions := make([]string, 0, len(p.Capabilities))
	for _, capability := range p.Capabilities {
		if capability.Available {
			actions = append(actions, capability.Action)
		}
	}
	return actions
}

func writeIdentity(b *strings.Builder, indent string, identity Identity) {
	writeString(b, indent+"ID", identity.ID)
	writeString(b, indent+"Name", identity.Name)
}

func writeString(b *strings.Builder, label, value string) {
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(strconv.Quote(value))
	b.WriteByte('\n')
}

func writeBool(b *strings.Builder, label string, value bool) {
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(strconv.FormatBool(value))
	b.WriteByte('\n')
}

func writeOptionalBool(b *strings.Builder, label string, value *bool) {
	b.WriteString(label)
	b.WriteString(": ")
	if value == nil {
		b.WriteString("(unknown)")
	} else {
		b.WriteString(strconv.FormatBool(*value))
	}
	b.WriteByte('\n')
}

func writeInt(b *strings.Builder, label string, value int) {
	b.WriteString(label)
	b.WriteString(": ")
	b.WriteString(strconv.Itoa(value))
	b.WriteByte('\n')
}

func writeStrings(b *strings.Builder, label string, values []string) {
	b.WriteString(label)
	b.WriteString(":\n")
	if len(values) == 0 {
		b.WriteString("  (none)\n")
		return
	}
	for _, value := range values {
		b.WriteString("  - ")
		b.WriteString(strconv.Quote(value))
		b.WriteByte('\n')
	}
}
