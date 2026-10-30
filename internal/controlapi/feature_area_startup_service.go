package controlapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/lazyun/devbridge/internal/aicontext"
	"github.com/lazyun/devbridge/internal/codexconfig"
	"github.com/lazyun/devbridge/internal/developmentcycle"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/module"
	"github.com/lazyun/devbridge/internal/observability"
	"github.com/lazyun/devbridge/internal/review"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/scopereview"
	"github.com/lazyun/devbridge/internal/task"
)

const startupContract = "feature-area-startup-v1"
const startupCommand = "FEATURE_AREA_CREATE"

type preparedStartup struct {
	Input  FeatureAreaCreateInput
	Task   CreatePlanningTaskInput
	Review review.Review
}

func startupDigest(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func startupChild(r featurearea.CreationReceipt, step string) string {
	return "startup-" + step + "-" + startupDigest([]string{startupContract, r.AISessionID, r.ProjectID, r.Command, r.RequestID, step})
}
func normalizeStartup(in FeatureAreaCreateInput) (FeatureAreaCreateInput, error) {
	if in.Mode == "" {
		in.Mode = CreateOnly
	}
	if in.Mode != CreateOnly && in.Mode != CreateAndReview {
		return in, invalid("Unknown FeatureArea creation mode")
	}
	if in.RequestID != "" && !observability.ValidRequestID(in.RequestID) {
		return in, invalid("A stable valid requestId is required")
	}
	if in.Mode == CreateOnly {
		if in.InitialTask != nil || in.InitialReview != nil {
			return in, invalid("CREATE_ONLY rejects initialTask and initialReview")
		}
		return in, nil
	}
	if in.RequestID == "" || in.InitialTask == nil || in.InitialReview == nil {
		return in, invalid("CREATE_AND_REVIEW requires requestId, initialTask and initialReview")
	}
	t, r := *in.InitialTask, *in.InitialReview
	in.InitialTask, in.InitialReview = &t, &r
	if len(t.Constraints) == 0 {
		t.Constraints = nil
	}
	if len(r.ReviewPlan.SelectedModules) == 0 {
		r.ReviewPlan.SelectedModules = nil
	}
	if strings.TrimSpace(t.RepositoryID) == "" || strings.TrimSpace(t.ProblemStatement) == "" {
		return in, invalid("Initial Task repositoryId and problemStatement are required")
	}
	if e := task.ValidateIntentText(strings.TrimSpace(t.Title), t.ProblemStatement, t.Constraints); e != nil {
		return in, invalid(e.Error())
	}
	if r.ReviewDepth != "FAST" && r.ReviewDepth != "DEEP" {
		return in, invalid("Review depth must be FAST or DEEP")
	}
	if scopereview.ValidateInstruction(r.ReviewInstruction) != nil || r.Model == "" {
		return in, invalid("A complete authored Review instruction and model are required")
	}
	if r.ReviewPlan.Mode != "FOCUSED" && r.ReviewPlan.Mode != "REPOSITORY" {
		return in, invalid("A FOCUSED or REPOSITORY Review plan is required")
	}
	return in, nil
}

// CreateFeatureAreaWithInitialReview composes existing canonical commands. Only
// explicit command retries resume steps; readers never call this method.
func (f *Facade) CreateFeatureAreaWithInitialReview(ctx context.Context, supplied FeatureAreaCreateInput) (FeatureAreaCreateResult, error) {
	in, err := normalizeStartup(supplied)
	if err != nil {
		return FeatureAreaCreateResult{}, err
	}
	project, err := f.sessionProject(ctx, in.AISessionID)
	if err != nil {
		return FeatureAreaCreateResult{}, err
	}
	if in.Mode == CreateOnly && in.RequestID == "" {
		area, e := f.CreateFeatureArea(ctx, featurearea.CreateInput{AISessionID: in.AISessionID, Title: in.Title, ProblemStatement: in.ProblemStatement})
		return FeatureAreaCreateResult{FeatureArea: area}, e
	}
	receipts, ok := f.deps.FeatureAreas.(creationReceipts)
	if !ok {
		return FeatureAreaCreateResult{}, errors.New("durable FeatureArea creation receipts unavailable")
	}
	fingerprint := startupDigest(struct {
		Version string
		Input   FeatureAreaCreateInput
	}{startupContract, in})
	area, receipt, err := receipts.ReplayCreation(ctx, in.AISessionID, startupCommand, in.RequestID, fingerprint)
	var prepared preparedStartup
	if errors.Is(err, featurearea.ErrNotFound) {
		prepared = preparedStartup{Input: in}
		if in.Mode == CreateAndReview {
			prepared, err = f.prepareStartup(ctx, project, in)
			if err != nil {
				return FeatureAreaCreateResult{}, err
			}
		}
		payload, e := json.Marshal(prepared)
		if e != nil {
			return FeatureAreaCreateResult{}, e
		}
		area, receipt, err = receipts.CreateReceipted(ctx, featurearea.CreateInput{AISessionID: in.AISessionID, Title: in.Title, ProblemStatement: in.ProblemStatement}, featurearea.CreationReceipt{AISessionID: in.AISessionID, ProjectID: project, Command: startupCommand, RequestID: in.RequestID, PayloadSHA256: fingerprint, ContractVersion: startupContract, Prepared: payload})
		if err != nil {
			// A failed transport/readback may follow a committed atomic root.
			recovered, saved, readErr := receipts.ReplayCreation(ctx, in.AISessionID, startupCommand, in.RequestID, fingerprint)
			if readErr == nil {
				area, receipt, err = recovered, saved, nil
			}
		}
	}
	if err != nil {
		return FeatureAreaCreateResult{}, MapError(err)
	}
	if err = json.Unmarshal(receipt.Prepared, &prepared); err != nil || receipt.ContractVersion != startupContract {
		out := startupResult(area, receipt, in.Mode)
		out.Startup.FailedStep = "READBACK"
		out.Startup.Error = &StartupError{Code: "RECEIPT_UNAVAILABLE", Message: "Committed creation identities are preserved; frozen startup contract cannot be read"}
		out.Startup.RefreshRequired = true
		return out, nil
	}
	out := startupResult(area, receipt, in.Mode)
	fail := func(step string, e error) (FeatureAreaCreateResult, error) {
		out.Startup.FailedStep = startupPublicStep(step)
		out.Startup.Error = &StartupError{Code: startupErrorCode(e), Message: e.Error()}
		out.Startup.Retryable = true
		out.Startup.Retry = &StartupRetry{Action: "RETRY_SAME_REQUEST", RequestID: in.RequestID}
		var rejectedAdmission *review.AdmissionBusyError
		if step == "VALIDATE_TASK" || step == "VALIDATE_FEATURE_AREA" || errors.As(e, &rejectedAdmission) {
			out.Startup.Retryable = false
			out.Startup.Retry = nil
		}
		receipt.FailedStep, receipt.ErrorCode, receipt.ErrorMessage = out.Startup.FailedStep, out.Startup.Error.Code, e.Error()
		if saveErr := receipts.UpdateCreationReceipt(ctx, receipt); saveErr != nil {
			out.Startup.RefreshRequired = true
		}
		return out, nil
	}
	save := func() error {
		receipt.FailedStep, receipt.ErrorCode, receipt.ErrorMessage = "", "", ""
		for attempt := 0; attempt < 4; attempt++ {
			e := receipts.UpdateCreationReceipt(ctx, receipt)
			if e == nil {
				out = startupResult(area, receipt, in.Mode)
				return nil
			}
			if !errors.Is(e, featurearea.ErrConflict) {
				return e
			}
			_, latest, e := receipts.ReplayCreation(ctx, in.AISessionID, startupCommand, in.RequestID, fingerprint)
			if e != nil {
				return e
			}
			for _, pair := range [][2]*string{{&receipt.TaskID, &latest.TaskID}, {&receipt.ReviewID, &latest.ReviewID}, {&receipt.RunID, &latest.RunID}} {
				if *pair[0] != "" && *pair[1] != "" && *pair[0] != *pair[1] {
					return featurearea.ErrConflict
				}
				if *pair[0] == "" {
					*pair[0] = *pair[1]
				}
			}
			receipt.ReviewAdmitted = receipt.ReviewAdmitted || latest.ReviewAdmitted
		}
		return featurearea.ErrConflict
	}
	if in.Mode == CreateOnly {
		return out, nil
	}
	// Recover the canonical substep receipt before consulting mutable configuration.
	prepared.Task.AISessionID, prepared.Task.FeatureAreaID, prepared.Task.RequestID = in.AISessionID, area.ID, startupChild(receipt, "task")
	if receipt.TaskID == "" {
		existing, e := f.ReplayPlanningTaskForSession(ctx, prepared.Task)
		if e == nil {
			receipt.TaskID = existing.ID
			out = startupResult(area, receipt, in.Mode)
			if e = save(); e != nil {
				return fail("RECORD_TASK", e)
			}
		} else if !errors.Is(e, task.ErrNotFound) {
			return fail("READ_TASK_RECEIPT", e)
		}
	}
	// Admission recovery uses the existing durable Review request identity. It
	// must precede lifecycle/default checks even if the original response was lost.
	if !receipt.ReviewAdmitted && receipt.TaskID != "" {
		values, e := f.deps.Reviews.ListByFeatureArea(ctx, area.ID)
		if e != nil {
			return fail("READ_REVIEW_RECEIPT", e)
		}
		for _, v := range values {
			if v.StartRequestID == startupChild(receipt, "review") {
				if v.ProjectID != project || v.TaskID != receipt.TaskID || v.CreatedByAISessionID != in.AISessionID || v.RunID == "" {
					return fail("READ_REVIEW_RECEIPT", errors.New("Original Review ownership unavailable"))
				}
				receipt.ReviewID, receipt.RunID, receipt.ReviewAdmitted = v.ID, v.RunID, true
				out = startupResult(area, receipt, in.Mode)
				if e = save(); e != nil {
					return fail("RECORD_REVIEW", e)
				}
				break
			}
		}
	}
	if receipt.ReviewAdmitted {
		return f.observeStartup(ctx, out, receipt), nil
	}
	if _, e := f.deps.FeatureAreas.GetForSession(ctx, in.AISessionID, area.ID); e != nil {
		return fail("VALIDATE_FEATURE_AREA", e)
	}
	if e := f.validateFrozenStartup(ctx, project, prepared); e != nil {
		return fail("VALIDATE_STARTUP", e)
	}
	if receipt.TaskID == "" {
		created, e := f.CreatePlanningTaskForSession(ctx, prepared.Task)
		if e != nil {
			replay, readErr := f.ReplayPlanningTaskForSession(ctx, prepared.Task)
			if readErr != nil {
				return fail("CREATE_TASK", e)
			}
			created = replay
		}
		receipt.TaskID = created.ID
		out = startupResult(area, receipt, in.Mode)
		if e = save(); e != nil {
			return fail("RECORD_TASK", e)
		}
	}
	if receipt.ReviewAdmitted {
		return f.observeStartup(ctx, out, receipt), nil
	}
	owner, e := f.deps.Tasks.Get(ctx, receipt.TaskID)
	if e != nil {
		return fail("READ_TASK", e)
	}
	if owner.CurrentReviewID != "" {
		v, e := f.deps.Reviews.Get(ctx, owner.CurrentReviewID)
		if e == nil && v.StartRequestID == startupChild(receipt, "review") && v.TaskID == receipt.TaskID && v.ProjectID == project && v.FeatureAreaID == area.ID && v.RunID != "" {
			receipt.ReviewID, receipt.RunID, receipt.ReviewAdmitted = v.ID, v.RunID, true
			if e = save(); e != nil {
				return fail("RECORD_REVIEW", e)
			}
			return f.observeStartup(ctx, out, receipt), nil
		}
	}
	if owner.ProjectID != project || owner.FeatureAreaID != area.ID || owner.RepositoryID != prepared.Task.RepositoryID || owner.Status != task.StatusActive || owner.Phase != task.PhaseReviewRequired || owner.CurrentReviewID != "" {
		return fail("VALIDATE_TASK", errors.New("Original Task is no longer eligible for its first Review; no replacement will be created"))
	}
	candidate := prepared.Review
	candidate.TaskID, candidate.FeatureAreaID = receipt.TaskID, area.ID
	admitted, e := f.startAdmittedReview(ctx, scopereview.AdmittedStartInput{AISessionID: in.AISessionID, RequestID: startupChild(receipt, "review"), PayloadSHA256: startupDigest(candidate), Review: candidate}, true)
	if e != nil && admitted.Review.ID == "" {
		values, readErr := f.deps.Reviews.ListByFeatureArea(ctx, area.ID)
		if readErr == nil {
			for _, v := range values {
				if v.StartRequestID == startupChild(receipt, "review") && v.ProjectID == project && v.TaskID == receipt.TaskID && v.CreatedByAISessionID == in.AISessionID && v.RunID != "" {
					admitted.Review = v
					break
				}
			}
		}
	}
	if admitted.Review.ID != "" {
		runID := admitted.Run.ID
		if runID == "" {
			runID = admitted.Review.RunID
		}
		if runID != "" {
			receipt.ReviewID, receipt.RunID, receipt.ReviewAdmitted = admitted.Review.ID, runID, true
			out = startupResult(area, receipt, in.Mode)
			if saveErr := save(); saveErr != nil {
				return fail("RECORD_REVIEW", saveErr)
			}
		}
	}
	if e != nil {
		if receipt.ReviewAdmitted {
			out = f.observeStartup(ctx, out, receipt)
			out.Startup.FailedStep = "DISPATCH"
			out.Startup.Error = &StartupError{Code: startupErrorCode(e), Message: e.Error()}
			out.Startup.Retry = &StartupRetry{Action: "REFRESH_RESULT", RequestID: in.RequestID}
			out.Startup.RefreshRequired = true
			receipt.FailedStep, receipt.ErrorCode, receipt.ErrorMessage = "DISPATCH", out.Startup.Error.Code, e.Error()
			_ = receipts.UpdateCreationReceipt(ctx, receipt)
			return out, nil
		}
		return fail("ADMIT_REVIEW", e)
	}
	if !receipt.ReviewAdmitted {
		return fail("ADMIT_REVIEW", errors.New("Review admission returned no durable identity"))
	}
	return f.observeStartup(ctx, out, receipt), nil
}

func startupResult(area featurearea.FeatureArea, r featurearea.CreationReceipt, mode string) FeatureAreaCreateResult {
	s := &FeatureAreaStartup{Mode: mode, RequestID: r.RequestID, FeatureAreaID: area.ID, TaskID: r.TaskID, ReviewID: r.ReviewID, RunID: r.RunID, ReviewAdmitted: r.ReviewAdmitted, CompletedSteps: []string{"FEATURE_AREA_CREATED"}}
	if r.TaskID != "" {
		s.CompletedSteps = append(s.CompletedSteps, "TASK_CREATED")
	}
	if r.ReviewAdmitted {
		s.CompletedSteps = append(s.CompletedSteps, "REVIEW_ADMITTED")
	}
	return FeatureAreaCreateResult{FeatureArea: area, Startup: s}
}
func startupErrorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return string(e.Code)
	}
	return "STARTUP_BLOCKED"
}
func (f *Facade) observeStartup(ctx context.Context, out FeatureAreaCreateResult, r featurearea.CreationReceipt) FeatureAreaCreateResult {
	v, e := f.deps.Runs.Get(ctx, r.RunID)
	if e != nil || v.ID != r.RunID || v.ReviewID != r.ReviewID || v.TaskID != r.TaskID {
		out.Startup.FailedStep = "READBACK"
		out.Startup.Error = &StartupError{Code: "READ_UNAVAILABLE", Message: "Startup committed; original Run status is unavailable"}
		out.Startup.RefreshRequired = true
		out.Startup.Retry = &StartupRetry{Action: "REFRESH_RESULT", RequestID: r.RequestID}
		return out
	}
	out.Startup.RunStatus = string(v.Status)
	if r.FailedStep == "DISPATCH" && (v.Status == run.StatusCreated || v.Status == run.StatusPreparing) {
		out.Startup.FailedStep = "DISPATCH"
		out.Startup.Error = &StartupError{Code: r.ErrorCode, Message: r.ErrorMessage}
		out.Startup.RefreshRequired = true
		out.Startup.Retry = &StartupRetry{Action: "REFRESH_RESULT", RequestID: r.RequestID}
	}
	return out
}

func (f *Facade) prepareStartup(ctx context.Context, project string, in FeatureAreaCreateInput) (preparedStartup, error) {
	p := preparedStartup{Input: in}
	if f.deps.StartupContext == nil || f.deps.DevelopmentCycles == nil {
		return p, errors.New("Startup context and development branch services are required")
	}
	session, e := f.deps.StartupContext.GetSession(ctx, in.AISessionID)
	if e != nil {
		return p, e
	}
	if session.Project.ID != project || !session.ProjectContext.SnapshotUsable() {
		return p, invalid("A usable Session-bound Project Context is required")
	}
	repo, e := f.deps.Repositories.Get(ctx, in.InitialTask.RepositoryID)
	if e != nil || repo.ProjectID != project {
		return p, notFound()
	}
	effort, e := f.resolveCurrentExecutionSelection(in.InitialReview.Model, in.InitialReview.ReasoningEffort)
	if e != nil {
		return p, e
	}
	plan, e := ResolveInitialReviewPlan(ctx, f.deps.StartupContext, in.AISessionID, repo.ID, in.InitialReview.ReviewPlan)
	if e != nil {
		return p, e
	}
	if len(plan.Blockers) > 0 {
		return p, invalid(strings.Join(plan.Blockers, "; "))
	}
	if _, e = scopereview.CanonicalizeScope(repo.Path, plan.Scope); e != nil {
		return p, e
	}
	development, e := f.deps.DevelopmentCycles.Context(ctx, project)
	if e != nil {
		return p, e
	}
	var base, ref string
	for _, v := range development.Repositories {
		if v.RepositoryID == repo.ID && !v.DevelopmentBranchSelectionRequired {
			base, ref = v.CurrentHeadSHA, v.DevelopmentRef
		}
	}
	if base == "" || ref == "" {
		return p, MapError(developmentcycle.ErrNotSelected)
	}
	configs, e := f.deps.StartupContext.ListCodexConfigs(ctx, in.AISessionID, aicontext.ListOptions{Limit: 100})
	if e != nil {
		return p, e
	}
	sort.Slice(configs, func(i, j int) bool { return configs[i].ID < configs[j].ID })
	var config string
	var backend codexconfig.BackendSelection
	for _, c := range configs {
		if c.Enabled && (config == "" || c.ID == codexconfig.DefaultConfigID) {
			config = c.ID
			backend = c.Selection("SESSION_INVENTORY")
		}
	}
	if config == "" {
		return p, invalid("No enabled Codex configuration is available")
	}
	modules, e := f.deps.StartupContext.ListModules(ctx, in.AISessionID, aicontext.ListOptions{Limit: 100})
	if e != nil {
		return p, e
	}
	var moduleID string
	for _, m := range modules {
		if m.Key == module.UnclassifiedKey {
			moduleID = m.ID
		}
	}
	if moduleID == "" {
		m, e := f.CreateModuleForSession(ctx, in.AISessionID, module.UnclassifiedKey, module.UnclassifiedName, "Fallback ownership for work not yet classified.", nil)
		if e != nil {
			return p, e
		}
		moduleID = m.ID
	}
	p.Task = CreatePlanningTaskInput{AISessionID: in.AISessionID, RepositoryID: repo.ID, CodexConfigID: config, ModuleID: moduleID, BaseRevision: base, Title: in.InitialTask.Title, ProblemStatement: in.InitialTask.ProblemStatement, Constraints: in.InitialTask.Constraints}
	summary := "Review relevant implementation across the selected Repository."
	if plan.Plan.Mode == run.ReviewModeFocused {
		summary = "Start with the selected implementation modules, then follow materially relevant dependencies."
	}
	p.Review = review.Review{ProjectID: project, CreatedByAISessionID: in.AISessionID, RepositoryID: repo.ID, CodexConfigID: config, BackendSelection: &backend, BaseCommit: base, ReviewDepth: review.ReviewDepth(in.InitialReview.ReviewDepth), Model: in.InitialReview.Model, ReasoningEffort: effort, Instruction: in.InitialReview.ReviewInstruction, EngineeringGoal: in.InitialTask.ProblemStatement, Focus: review.Focus{Kind: review.FocusKind(plan.Plan.Mode), Summary: summary, Paths: plan.Scope.Paths, Modules: plan.Plan.SelectedModules}, RepositoryProvenance: &review.RepositoryProvenance{RepositoryID: repo.ID, Commit: base, RepositoryHEAD: base, BranchKind: review.ProvenanceBranch, BranchRef: ref, InspectionMode: review.InspectionFeatureAreaWorkspace, CapturedAt: time.Now().UTC()}}
	return p, nil
}
func (f *Facade) validateFrozenStartup(ctx context.Context, project string, p preparedStartup) error {
	if _, e := f.resolveCurrentExecutionSelection(p.Review.Model, p.Review.ReasoningEffort); e != nil {
		return e
	}
	configs, e := f.deps.StartupContext.ListCodexConfigs(ctx, p.Input.AISessionID, aicontext.ListOptions{Limit: 100})
	if e != nil {
		return e
	}
	enabled := false
	for _, v := range configs {
		if v.ID == p.Task.CodexConfigID && v.Enabled {
			enabled = true
		}
	}
	if !enabled {
		return invalid("The frozen Codex configuration is no longer enabled")
	}
	session, e := f.deps.StartupContext.GetSession(ctx, p.Input.AISessionID)
	if e != nil {
		return e
	}
	if session.Project.ID != project || !session.ProjectContext.SnapshotUsable() {
		return invalid("Current Session context is not usable")
	}
	d, e := f.deps.DevelopmentCycles.Context(ctx, project)
	if e != nil {
		return e
	}
	valid := false
	for _, v := range d.Repositories {
		if v.RepositoryID == p.Task.RepositoryID && !v.DevelopmentBranchSelectionRequired && v.CurrentHeadSHA == p.Task.BaseRevision && p.Review.RepositoryProvenance != nil && v.DevelopmentRef == p.Review.RepositoryProvenance.BranchRef {
			valid = true
		}
	}
	if !valid {
		return invalid("Frozen development context changed; retry cannot select a new baseline")
	}
	plan, e := ResolveInitialReviewPlan(ctx, f.deps.StartupContext, p.Input.AISessionID, p.Task.RepositoryID, p.Input.InitialReview.ReviewPlan)
	if e != nil {
		return e
	}
	if len(plan.Blockers) > 0 {
		return invalid(strings.Join(plan.Blockers, "; "))
	}
	if startupDigest(plan.Scope.Paths) != startupDigest(p.Review.Focus.Paths) || startupDigest(plan.Plan.SelectedModules) != startupDigest(p.Review.Focus.Modules) {
		return invalid("Frozen Review scope changed")
	}
	repo, e := f.deps.Repositories.Get(ctx, p.Task.RepositoryID)
	if e != nil {
		return e
	}
	if repo.ProjectID != project {
		return notFound()
	}
	_, e = scopereview.CanonicalizeScope(repo.Path, plan.Scope)
	return e
}

func startupPublicStep(step string) string {
	switch step {
	case "CREATE_TASK", "RECORD_TASK", "READ_TASK_RECEIPT":
		return "TASK_CREATE"
	case "READ_RUN", "READ_TASK", "READ_REVIEW_RECEIPT":
		return "READBACK"
	case "DISPATCH":
		return "DISPATCH"
	default:
		return "REVIEW_ADMISSION"
	}
}
