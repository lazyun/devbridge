package controlapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/delivery"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/featurearea"
	"github.com/lazyun/devbridge/internal/run"
	"github.com/lazyun/devbridge/internal/task"
	wp "github.com/lazyun/devbridge/internal/workflowprogression"
)

// Preparation resolves exact recorded identities and scope using reads only.
// It freezes observations, not authorization: every canonical write rechecks.
func (f *Facade) validatePreparedConfirmation(ctx context.Context, in *c.PrepareInput, project string) error {
	if in == nil {
		return invalid("confirmation content required")
	}
	if in.Execution != nil && in.Action != "CONFIRM_IMPLEMENTATION" && in.Action != "CONFIRM_AND_START_IMPLEMENTATION" && (in.Execution.RepositoryID != "" || in.Execution.BaseCommit != "") {
		return invalid("Repository selection is only accepted by an unreviewed implementation confirmation")
	}
	if verificationTaskChoice(in.Action) {
		return f.validateVerificationChoiceConfirmation(ctx, in, project)
	}
	if in.Action == "CONFIRM_IMPLEMENTATION" || in.Action == "CONFIRM_AND_START_IMPLEMENTATION" {
		return f.validateUnreviewedImplementation(ctx, in, project)
	}
	if in.Purpose == c.ProjectSetup {
		return nil
	}
	if in.Purpose == c.RecoveryPurpose {
		body := in.RecoveryPurpose
		if body == nil || in.Action != c.ConfirmRecoveryPurpose || in.AISessionID != body.Proposal.SourceAISessionID ||
			body.Proposal.SubmittedHash != body.Proposal.ComputeSubmittedHash() || body.ExpectedTaskStatus != "ACTIVE" || body.ExpectedTaskPhase != "RECOVERY_REQUIRED" {
			return invalid("incomplete Recovery purpose confirmation")
		}
		current, err := f.deps.Tasks.Get(ctx, body.Proposal.TaskID)
		if err != nil {
			return err
		}
		if current.ProjectID != project || current.FeatureAreaID != body.Proposal.FeatureAreaID || string(current.Status) != body.ExpectedTaskStatus || string(current.Phase) != body.ExpectedTaskPhase || current.CurrentDefinitionRevisionID != body.Proposal.TaskDefinitionRevisionID {
			return invalid("Recovery purpose source is no longer current")
		}
		return nil
	}
	projectSnapshot := func() (int64, string, error) {
		var topology int64
		if reader, ok := f.deps.Repositories.(interface {
			TopologyRevision(context.Context, string) (int64, error)
		}); ok {
			var err error
			topology, err = reader.TopologyRevision(ctx, project)
			if err != nil {
				return 0, "", err
			}
		}
		if f.deps.DevelopmentCycles == nil {
			return topology, "", nil
		}
		development, err := f.deps.DevelopmentCycles.Context(ctx, project)
		if err != nil {
			return 0, "", err
		}
		type branchIdentity struct {
			RepositoryID     string `json:"repositoryId"`
			DevelopmentRef   string `json:"developmentRef"`
			DevelopmentCycle string `json:"developmentCycleId"`
			SelectedHead     string `json:"selectedHeadSha"`
		}
		identities := make([]branchIdentity, 0, len(development.Repositories))
		for _, value := range development.Repositories {
			identity := branchIdentity{RepositoryID: value.RepositoryID, DevelopmentRef: value.DevelopmentRef, SelectedHead: value.SelectedHeadSHA}
			if value.DevelopmentCycle != nil {
				identity.DevelopmentCycle = value.DevelopmentCycle.ID
			}
			identities = append(identities, identity)
		}
		slices.SortFunc(identities, func(a, b branchIdentity) int { return strings.Compare(a.RepositoryID, b.RepositoryID) })
		return topology, c.ValueHash(identities), nil
	}
	repository := func(id string) error {
		if id == "" {
			return invalid("exact Repository required")
		}
		r, e := f.deps.Repositories.Get(ctx, id)
		if e != nil {
			return e
		}
		if r.ProjectID != project {
			return notFound()
		}
		return nil
	}
	area := func(id string) error {
		if id == "" {
			return invalid("exact FeatureArea required")
		}
		a, e := f.deps.FeatureAreas.GetForSession(ctx, in.AISessionID, id)
		if e != nil {
			return e
		}
		if a.ProjectID != project || a.LegacyReadOnly || !featurearea.IsUsable(a) {
			return notFound()
		}
		return nil
	}
	plan := func(repo string, depth task.ReviewDepth, p *run.ReviewPlan) error {
		if depth != task.ReviewDepthFast && depth != task.ReviewDepthDeep {
			return invalid("explicit FAST or DEEP Review depth required")
		}
		if f.deps.StartupContext == nil {
			return invalid("Review scope reader unavailable")
		}
		resolved, e := ResolveInitialReviewPlan(ctx, f.deps.StartupContext, in.AISessionID, repo, InitialReviewPlan{Mode: string(p.Mode), SelectedModules: p.SelectedModules})
		if e != nil {
			return e
		}
		if len(resolved.Blockers) > 0 {
			return invalid(strings.Join(resolved.Blockers, "; "))
		}
		// InitialScope is a derived production scope, never an arbitrary transport override.
		if p.InitialScope.Kind != "" && (p.InitialScope.Kind != resolved.Plan.InitialScope.Kind || !slices.Equal(p.InitialScope.Paths, resolved.Plan.InitialScope.Paths)) {
			return invalid("Review scope changed; prepare a new version")
		}
		*p = resolved.Plan
		return nil
	}
	taskBody := func(b *c.TaskBody, newArea bool) error {
		if b == nil {
			return invalid("complete initial Task required")
		}
		if e := task.ValidateIntentText(b.Title, b.ProblemStatement, b.Constraints); e != nil {
			return e
		}
		if b.RepositoryID != "" || strings.HasSuffix(in.Action, "_AND_REVIEW") {
			if e := repository(b.RepositoryID); e != nil {
				return e
			}
		}
		if newArea {
			if b.FeatureAreaID != "" {
				return invalid("new area initial Task must not claim an existing area")
			}
		} else if e := area(b.FeatureAreaID); e != nil {
			return e
		}
		start := strings.HasSuffix(in.Action, "_AND_REVIEW")
		if b.StartReview != start {
			return invalid("Task startReview must match the explicit confirmation action")
		}
		if start {
			return plan(b.RepositoryID, b.ReviewDepth, &b.ReviewPlan)
		}
		if b.ReviewDepth != "" || b.ReviewPlan.Mode != "" {
			return invalid("create-only content cannot carry an unrequested Review")
		}
		return nil
	}
	if in.FeatureArea != nil {
		b := in.FeatureArea
		if in.Action == "MERGE_FEATURE_AREA" {
			if err := area(b.FeatureAreaID); err != nil {
				return err
			}
			if b.InitialTask != nil {
				return invalid("merge cannot create a Task")
			}
			store, ok := f.deps.WorkflowProgression.(featureAreaPlanStore)
			if !ok {
				return invalid("multi-repository delivery unavailable")
			}
			var observed delivery.FeatureAreaPlan
			var err error
			if b.CompletionTaskID != "" {
				completionStore, ok := f.deps.WorkflowProgression.(interface {
					PrepareTaskCompletionMergePlan(context.Context, string, string, string) (delivery.FeatureAreaPlan, error)
				})
				if !ok {
					return invalid("Task completion merge preparation unavailable")
				}
				observed, err = completionStore.PrepareTaskCompletionMergePlan(ctx, project, b.FeatureAreaID, b.CompletionTaskID)
			} else {
				observed, err = store.PrepareFeatureAreaDeliveryPlan(ctx, project, b.FeatureAreaID)
			}
			if err != nil {
				return err
			}
			if b.DeliveryPlan != nil && !reflect.DeepEqual(*b.DeliveryPlan, observed) {
				return invalid("FeatureArea delivery plan changed")
			}
			b.DeliveryPlan = &observed
			b.CompletionTaskID = observed.CompletionTaskID
			return nil
		}
		if b.CompletionTaskID != "" {
			return invalid("Completion Task applies only to merge preparation")
		}
		if strings.TrimSpace(b.Title) == "" || strings.TrimSpace(b.ProblemStatement) == "" {
			return invalid("complete FeatureArea definition required")
		}
		if in.Action == "CREATE_AREA_AND_REVIEW" {
			if e := taskBody(b.InitialTask, true); e != nil {
				return e
			}
		} else if b.InitialTask != nil {
			return invalid("CREATE_AREA does not create an implicit Task")
		}
	}
	if in.Task != nil {
		if e := taskBody(in.Task, false); e != nil {
			return e
		}
	}
	if in.Review != nil {
		b := in.Review
		t, e := f.deps.Tasks.Get(ctx, b.TaskID)
		if e != nil {
			return e
		}
		if t.ProjectID != project || t.FeatureAreaID != b.FeatureAreaID || t.Status != task.StatusActive || t.LegacyReadOnly {
			return notFound()
		}
		if e = area(b.FeatureAreaID); e != nil {
			return e
		}
		if confirmationReviewDecision(in.Action) {
			topology, developmentHash, snapshotErr := projectSnapshot()
			if snapshotErr != nil {
				return snapshotErr
			}
			if b.PreparedTopologyRevision == 0 {
				b.PreparedTopologyRevision = topology
			} else if topology != 0 && b.PreparedTopologyRevision != topology {
				return c.NewFailure("PROJECT_TOPOLOGY_CHANGED", "CORE", "CONFIRMATION_PREFLIGHT", "The Project repository topology changed after this confirmation was prepared.", c.PrepareNewVersion, nil)
			}
			if b.PreparedDevelopmentContextHash == "" {
				b.PreparedDevelopmentContextHash = developmentHash
			} else if developmentHash != "" && b.PreparedDevelopmentContextHash != developmentHash {
				return c.NewFailure("DEVELOPMENT_BRANCH_CONFIGURATION_CHANGED", "CORE", "CONFIRMATION_PREFLIGHT", "The Project development branch configuration changed after this confirmation was prepared.", c.PrepareNewVersion, nil)
			}
			formalDecision := b.DecisionID == wp.DecisionID(b.ReviewID) && t.Phase == task.PhaseReviewApprovalPending
			invalidDecision := b.DecisionID == wp.InvalidResultDecisionID(b.ReviewID) && t.Phase == task.PhaseReviewing
			if b.ReviewID == "" || b.ReviewID != t.CurrentReviewID || !formalDecision && !invalidDecision {
				return c.NewFailure("CURRENT_REVIEW_CHANGED", "DOMAIN", "CONFIRMATION_PREFLIGHT", "The Task no longer points to the Review frozen by this confirmation.", c.PrepareNewVersion, nil)
			}
			g, e := f.WorkflowGuidanceForResource(ctx, wp.ResourceScope{AISessionID: in.AISessionID, ProjectID: project, TaskID: t.ID})
			if e != nil {
				return e
			}
			if g.Approval == nil || g.Approval.DecisionID != b.DecisionID || g.Approval.DecisionVersion == "" {
				return c.NewFailure("DECISION_NOT_PENDING", "DOMAIN", "CONFIRMATION_PREFLIGHT", "No current Review Decision matches the frozen Decision identity.", c.PrepareNewVersion, nil)
			}
			if g.Approval.Status != "PENDING" {
				code := g.Approval.ReasonCode
				if code == "" {
					code = "DECISION_NOT_PENDING"
				}
				return c.NewFailure(code, "DOMAIN", "CONFIRMATION_PREFLIGHT", "The frozen Review Decision is no longer pending.", c.PrepareNewVersion, nil)
			}
			// The Decision version token is a Core observation, never a client
			// input. A frozen token that no longer matches is its own code.
			if b.PreparedDecisionVersion == "" {
				b.PreparedDecisionVersion = g.Approval.DecisionVersion
			} else if b.PreparedDecisionVersion != g.Approval.DecisionVersion {
				return c.NewFailure("DECISION_VERSION_CHANGED", "DOMAIN", "CONFIRMATION_PREFLIGHT", "The Review Decision changed after this confirmation was prepared.", c.PrepareNewVersion, nil)
			}
			if in.Action == "REVISE" {
				if !wp.ValidReviewFeedback(b.ReviewFeedback) {
					return invalid("REVISE requires non-empty bounded reviewFeedback")
				}
			} else if b.ReviewFeedback != "" {
				return invalid("REJECT cannot carry reviewFeedback")
			}
			if b.RepositoryID != "" || b.WorkspaceID != "" || len(b.RepositoryTargets) != 0 || b.ReviewDepth != "" || !reflect.DeepEqual(b.ReviewPlan, run.ReviewPlan{}) || b.SupplementalPurpose != "" || b.ExpectedTarget != nil {
				return invalid("Review decision cannot inject Review target or runtime inputs")
			}
			for _, v := range in.Variants {
				if v.ExecutorKind != "" || v.ReviewInstruction != "" || v.ImplementationMode != "" || v.WorkSpec != nil || v.VerificationSpec != nil || v.ExecutionInstructions != nil {
					return invalid("Review decision cannot carry replacement content")
				}
			}
			return nil
		}
		if t.RepositoryID != b.RepositoryID {
			return notFound()
		}
		floor, depthErr := f.preparedReviewDepthFloor(ctx, t, in.Action == "REQUEST_SUPPLEMENTAL_REVIEW")
		if depthErr != nil {
			return depthErr
		}
		if floor != nil && !b.ReviewDepth.Satisfies(*floor) {
			return c.NewFailure("REVIEW_DEPTH_TOO_SHALLOW", "DOMAIN", "CONFIRMATION_PREFLIGHT", fmt.Sprintf("Task %s requires %s review; proposed %s is not allowed. Prepare this same Task with an allowed reviewDepth; do not lower its requirement.", t.ID, *floor, b.ReviewDepth), c.PrepareNewVersion, nil)
		}
		if len(b.RepositoryTargets) > 0 {
			if e = f.prepareReviewTargets(ctx, in, project); e != nil {
				return e
			}
		} else {
			if e = repository(b.RepositoryID); e != nil {
				return e
			}
			if e = plan(b.RepositoryID, b.ReviewDepth, &b.ReviewPlan); e != nil {
				return e
			}
		}
		observed := wp.TaskReviewRequestTarget{Phase: t.Phase, TaskDefinitionRevisionID: t.CurrentDefinitionRevisionID, CurrentReviewID: t.CurrentReviewID, CurrentExecutionID: t.CurrentExecutionID}
		if in.Action == "REQUEST_SUPPLEMENTAL_REVIEW" {
			if b.ExpectedTarget == nil || strings.TrimSpace(b.SupplementalPurpose) == "" {
				return invalid("supplemental Review requires complete observed target and purpose")
			}
			store, ok := f.deps.WorkflowProgression.(wp.TaskReviewRequestStore)
			if !ok {
				return invalid("supplemental eligibility unavailable")
			}
			facts, e := store.TaskReviewRequestEligibility(ctx, in.AISessionID, t.ID)
			if e != nil {
				return e
			}
			if facts.Target == nil || *facts.Target != *b.ExpectedTarget {
				return invalid("supplemental target changed")
			}
			if !facts.Available {
				return invalid(facts.Reason)
			}
		} else {
			if b.SupplementalPurpose != "" {
				return invalid("first/revised Review cannot carry a supplemental mutation")
			}
			if t.Phase != task.PhaseReviewRequired {
				return invalid("Task is not awaiting a new Review")
			}
			if b.ExpectedTarget != nil && *b.ExpectedTarget != observed {
				return invalid("Review target changed")
			}
			b.ExpectedTarget = &observed
		}
	}
	if in.Execution != nil {
		b := in.Execution
		t, e := f.deps.Tasks.Get(ctx, b.TaskID)
		if e != nil {
			return e
		}
		if t.ProjectID != project || t.FeatureAreaID != b.FeatureAreaID || t.CurrentDefinitionRevisionID != b.TaskDefinitionRevisionID || t.Status != task.StatusActive || t.LegacyReadOnly {
			return notFound()
		}
		if e = area(b.FeatureAreaID); e != nil {
			return e
		}
		if b.ReviewID != "" {
			r, e := f.deps.Reviews.Get(ctx, b.ReviewID)
			if e != nil {
				return e
			}
			if r.ProjectID != project || r.TaskID != t.ID || r.FeatureAreaID != t.FeatureAreaID {
				return notFound()
			}
		}
		if b.ExecutionID != t.CurrentExecutionID {
			return invalid("Execution observation is not current")
		}
		if in.Action == "APPROVE" || in.Action == "APPROVE_AND_START" || in.Action == "START_APPROVED" {
			formal := false
			for _, variant := range in.Variants {
				if variant.ImplementationMode == task.ImplementationCodingAgent && len(variant.RepositoryTargets) > 0 {
					formal = true
				}
			}
			if formal {
				topology, developmentHash, err := projectSnapshot()
				if err != nil {
					return err
				}
				if topology <= 0 || developmentHash == "" {
					return invalid("formal confirmation requires Core Project context")
				}
				if b.PreparedTopologyRevision == 0 {
					b.PreparedTopologyRevision = topology
				} else if b.PreparedTopologyRevision != topology {
					return c.NewFailure("PROJECT_TOPOLOGY_CHANGED", "CORE", "CONFIRMATION_PREFLIGHT", "The Project repository topology changed after this confirmation was prepared.", c.PrepareNewVersion, nil)
				}
				if b.PreparedDevelopmentContextHash == "" {
					b.PreparedDevelopmentContextHash = developmentHash
				} else if b.PreparedDevelopmentContextHash != developmentHash {
					return c.NewFailure("DEVELOPMENT_BRANCH_CONFIGURATION_CHANGED", "CORE", "CONFIRMATION_PREFLIGHT", "The Project development branch configuration changed after this confirmation was prepared.", c.PrepareNewVersion, nil)
				}
			}
		}
		useApprovedTaskWorkset := b.ExecutionID == ""
		if b.ExecutionID != "" {
			x, e := f.deps.ExecutionProposals.Get(ctx, in.AISessionID, b.ExecutionID)
			if e != nil {
				return e
			}
			if x.ProjectID != project || x.TaskID != t.ID {
				return notFound()
			}
			if x.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID {
				// A definition-only revision approval retains the canceled owner
				// until explicit start. Require the exact persisted new approval;
				// its new workset must not be taken from the historical predecessor.
				if in.Action != "START_APPROVED" {
					return notFound()
				}
				eligible, err := f.noRunRevisionStartEligible(ctx, t)
				if err != nil {
					return err
				}
				if !eligible {
					return notFound()
				}
				useApprovedTaskWorkset = true
			}
			if !useApprovedTaskWorkset && x.ApprovedWorksetHash != "" && b.ExpectedTarget != nil {
				if (b.ExpectedTarget.PrimaryMemberID != "" && b.ExpectedTarget.PrimaryMemberID != x.PrimaryMemberID) || (b.ExpectedTarget.ApprovedWorksetHash != "" && b.ExpectedTarget.ApprovedWorksetHash != x.ApprovedWorksetHash) {
					return invalid("Execution runtime workset changed")
				}
				b.ExpectedTarget.PrimaryMemberID = x.PrimaryMemberID
				b.ExpectedTarget.ApprovedWorksetHash = x.ApprovedWorksetHash
			}
		}
		if useApprovedTaskWorkset && b.ExpectedTarget != nil && len(t.RepositoryTargets) != 0 && t.WorkSpec.ImplementationMode == task.ImplementationCodingAgent {
			raw, hashErr := json.Marshal(t.RepositoryTargets)
			if hashErr != nil {
				return hashErr
			}
			sum := sha256.Sum256(raw)
			hash := hex.EncodeToString(sum[:])
			if (b.ExpectedTarget.PrimaryMemberID != "" && b.ExpectedTarget.PrimaryMemberID != t.PrimaryMemberID) || (b.ExpectedTarget.ApprovedWorksetHash != "" && b.ExpectedTarget.ApprovedWorksetHash != hash) {
				return invalid("Execution runtime workset changed")
			}
			b.ExpectedTarget.PrimaryMemberID = t.PrimaryMemberID
			b.ExpectedTarget.ApprovedWorksetHash = hash
		}
		switch in.Action {
		case "APPROVE", "APPROVE_AND_START":
			if b.ReviewID == "" || b.ReviewID != t.CurrentReviewID || b.DecisionID != wp.DecisionID(b.ReviewID) || t.Phase != task.PhaseReviewApprovalPending {
				return invalid("exact pending Review approval required")
			}
			g, e := f.WorkflowGuidanceForResource(ctx, wp.ResourceScope{AISessionID: in.AISessionID, ProjectID: project, TaskID: t.ID})
			if e != nil {
				return e
			}
			if g.Approval == nil || g.Approval.DecisionID != b.DecisionID || g.Approval.Status != "PENDING" {
				return invalid("Review approval is no longer pending")
			}
		case "START_DIRECT_FILE", "RECOVER_DIRECT_FILE", "CANCEL_DIRECT_FILE":
			if t.WorkSpec.ImplementationMode != task.ImplementationDirectFile || t.CurrentDefinitionRevisionID == "" || b.ReviewID != t.CurrentReviewID || b.DecisionID != wp.DecisionID(t.CurrentReviewID) || t.CurrentExecutionID != "" {
				return invalid("exact approved Direct File target required")
			}
			if in.Action == "START_DIRECT_FILE" {
				if t.Phase != task.PhaseImplementationReady || b.WorkflowID != "" || b.RuntimeEpoch != "" || b.GrantGeneration != 0 {
					return invalid("new Direct File start requires unstarted exact revision")
				}
			} else {
				if f.deps.DirectFiles == nil {
					return invalid("Direct File unavailable")
				}
				state, err := f.deps.DirectFiles.TaskStatus(ctx, in.AISessionID, t.ID)
				if err != nil {
					return err
				}
				if b.WorkflowID == "" || b.WorkflowID != state.WorkflowID || b.RuntimeEpoch != state.RuntimeEpoch || b.GrantGeneration != state.GrantGeneration {
					return invalid("exact observed Direct File generation required")
				}
				if in.Action == "RECOVER_DIRECT_FILE" && state.FailureCode == directfile.WorkerContinuationUnsupported {
					return directfile.Failure(directfile.WorkerContinuationUnsupported, state.FailureReason)
				}
				if in.Action == "RECOVER_DIRECT_FILE" && state.State != "RECOVERY_REQUIRED" && state.State != "PREPARING" {
					return directfile.Failure(directfile.NotEnabled, "Direct File workflow does not require recovery")
				}
			}
		case "START_APPROVED":
			if b.ExpectedTarget == nil || b.ExpectedTarget.Phase != task.PhaseImplementationReady || b.ExpectedTarget.FeatureAreaID != t.FeatureAreaID || b.ExpectedTarget.TaskDefinitionRevisionID != t.CurrentDefinitionRevisionID || b.ExpectedTarget.ExecutionID != t.CurrentExecutionID || t.Phase != task.PhaseImplementationReady {
				return invalid("exact approved implementation target required")
			}
		case "RETRY_IMPLEMENTATION", "REQUEST_CHANGES":
			if b.ExecutionID == "" || b.DecisionID != wp.TaskAcceptanceDecisionID(b.ExecutionID) {
				return invalid("exact current acceptance source required")
			}
			if in.Action == "REQUEST_CHANGES" && v2CodingDefinition(t) {
				return requestChangesRequiresReviewFailure()
			}
			if in.Action == "RETRY_IMPLEMENTATION" && v2CodingDefinition(t) {
				available, reason, err := f.executionRuntimeEligibility(ctx, in.AISessionID, t)
				if err != nil {
					return err
				}
				if !available {
					return c.NewFailure(reason, "CORE", "CONFIRMATION_PREFLIGHT", "The exact frozen runtime authority is unavailable. Read the current Task and request a new Review if its definition or runtime must change.", c.ReadCurrentTarget, nil)
				}
			}
		case "ACCEPT", "REJECT", "REQUEST_REVERIFICATION":
			if b.ExecutionID == "" || b.DecisionID != wp.TaskAcceptanceDecisionID(b.ExecutionID) {
				return invalid("exact current acceptance decision required")
			}
			for _, v := range in.Variants {
				if v.ExecutorKind != "" || v.ReviewInstruction != "" || v.ImplementationMode != "" || v.WorkSpec != nil || v.VerificationSpec != nil || v.ExecutionInstructions != nil {
					return invalid("Task acceptance decision cannot carry replacement content")
				}
			}
			// Preparation must expose only a currently legal shared-domain action;
			// waiting until commit to reject an ineligible ACCEPT would turn the
			// purpose Card into a misleading generic decision form.
			presented, e := f.TaskPresentationForSession(ctx, in.AISessionID, b.FeatureAreaID, b.TaskID)
			if e != nil {
				return e
			}
			allowed := false
			if presented.TaskView != nil && presented.TaskView.ActionsStatus == "AVAILABLE" && presented.TaskView.CurrentExecutionAcceptance != nil && presented.TaskView.CurrentExecutionAcceptance.Receipt == nil && presented.TaskView.CurrentExecutionAcceptance.DecisionID == b.DecisionID {
				for _, capability := range presented.TaskView.Actions {
					target := capability.Target
					if capability.Action == in.Action && capability.Available && target != nil && target.TaskID == b.TaskID && target.FeatureAreaID == b.FeatureAreaID && target.SubjectID == b.ExecutionID && target.ExecutionID == b.ExecutionID && target.TaskDefinitionRevisionID == b.TaskDefinitionRevisionID && target.DecisionID == b.DecisionID {
						allowed = true
						break
					}
				}
			}
			if !allowed {
				return invalid("Task acceptance action is no longer available")
			}
			return nil
		}
		for _, v := range in.Variants {
			if v.WorkSpec == nil || v.VerificationSpec == nil {
				return invalid("each implementation variant needs complete displayed canonical specs")
			}
			if len(v.RepositoryTargets) > 0 {
				definition := task.Definition{Version: 2, RepositoryTargets: v.RepositoryTargets, PrimaryMemberID: v.PrimaryMemberID, WorkSpec: *v.WorkSpec, VerificationSpec: *v.VerificationSpec}
				e = task.ValidateDefinitionTargets(definition)
				if e == nil {
					e = task.ValidateExecutablePrimary(definition)
				}
			} else {
				e = task.ValidateWorkSpec(*v.WorkSpec)
			}
			if e != nil {
				return e
			}
			if e = task.ValidateExecutableVerificationSpec(*v.VerificationSpec); e != nil {
				return e
			}
			if v.ExecutionInstructions != nil {
				w, verification, e := task.ApplyExecutionInstructions(*v.WorkSpec, *v.VerificationSpec, v.ExecutionInstructions)
				if e != nil {
					return e
				}
				if !reflect.DeepEqual(w, *v.WorkSpec) || !reflect.DeepEqual(verification, *v.VerificationSpec) {
					return invalid("displayed canonical instructions differ from supplied execution instructions")
				}
			}
			if in.Action == "START_APPROVED" || confirmationDirectFileAction(in.Action) {
				if confirmationDirectFileAction(in.Action) && (v.ImplementationMode != task.ImplementationDirectFile || !reflect.DeepEqual(t.RepositoryTargets, v.RepositoryTargets)) {
					return invalid("exact approved Direct File workset required")
				}
				if !reflect.DeepEqual(t.WorkSpec, *v.WorkSpec) || !reflect.DeepEqual(t.VerificationSpec, *v.VerificationSpec) {
					return invalid("START_APPROVED confirms the frozen candidate content; new instructions require a successor")
				}
			}
			if in.Action == "RETRY_IMPLEMENTATION" {
				w, verification, e := task.ApplyExecutionInstructions(t.WorkSpec, t.VerificationSpec, v.ExecutionInstructions)
				if e != nil {
					return e
				}
				if !reflect.DeepEqual(w, *v.WorkSpec) || !reflect.DeepEqual(verification, *v.VerificationSpec) {
					return invalid("immutable candidate requires its exact specs or an explicit instruction-only successor")
				}
				if v2CodingDefinition(t) && (!reflect.DeepEqual(t.WorkSpec, w) || !reflect.DeepEqual(t.VerificationSpec, verification) || !reflect.DeepEqual(t.RepositoryTargets, v.RepositoryTargets) || t.PrimaryMemberID != v.PrimaryMemberID) {
					return c.NewFailure("RETRY_REQUIRES_FROZEN_DEFINITION", "CORE", "CONFIRMATION_PREFLIGHT", "Retry keeps the approved definition and runtime unchanged. Request a supplemental Review to change instructions or scope.", c.ReadCurrentTarget, nil)
				}
			}
		}
	}
	if in.Action == "CREATE_TASK" || in.Action == "CREATE_AREA" {
		for _, v := range in.Variants {
			if v.ExecutorKind != "" || v.ImplementationMode != "" || v.ReviewInstruction != "" || v.WorkSpec != nil || v.VerificationSpec != nil || v.ExecutionInstructions != nil {
				return invalid("create-only confirmation cannot carry hidden running content")
			}
		}
		if in.RecommendedRuntimeConfig != nil {
			return invalid("create-only confirmation has no runtime configuration")
		}
	}
	return nil
}
