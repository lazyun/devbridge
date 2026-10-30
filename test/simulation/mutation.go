package simulation

import (
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	c "github.com/lazyun/devbridge/internal/confirmation"
	"github.com/lazyun/devbridge/internal/directfile"
	"github.com/lazyun/devbridge/internal/workset"
)

type mutationProof struct{ Input, Result map[string]any }

func (f *fixture) mutationSetup() map[string]map[string]string {
	f.approveFileFixture(true, []string{"same.txt", "created.txt", "moved.txt"})
	f.startFile()
	f.claimFile()
	expected := map[string]map[string]string{}
	for _, target := range f.targets {
		for name, repo := range f.repos {
			if repo == target.RepositoryID {
				expected[target.MemberID] = map[string]string{"same.txt": f.expectedSource[name]}
			}
		}
	}
	f.checkMemberFiles(expected)
	f.checkPartialFiles("claimed but no file effect", "IMPLEMENTATION_READY", "ACTIVE")
	return expected
}
func (f *fixture) mutationInput(t workset.Target, kind, path, id, content, digest, destination string) map[string]any {
	in := f.readArgs(t)
	in["action"], in["path"], in["requestId"], in["protocolVersion"], in["workerClaim"] = kind, path, id, 2, f.claim.WorkerClaimIdentity
	if kind == "CREATE" || kind == "REPLACE" {
		in["content"] = content
	}
	if kind != "CREATE" {
		in["expectedDigest"] = digest
	}
	if kind == "MOVE" {
		in["destination"] = destination
	}
	return in
}
func (f *fixture) mutate(in map[string]any, oldDigest, newDigest string) mutationProof {
	before := f.managedFileSnapshot()
	got := f.s.agent.call("file_write", in)
	path := field(in, "path")
	destination, _ := in["destination"].(string)
	if in["action"] == "MOVE" {
		path = field(in, "destination")
	}
	f.s.check("actual "+field(in, "action")+" has exact receipt and bounded filesystem delta", got["path"] == path && got["digest"] == newDigest && got["workflowComplete"] == false && mutationDelta(before, f.managedFileSnapshot(), field(in, "action"), field(in, "path"), destination, oldDigest, newDigest), got)
	f.checkPhase("successful file effect is implementation, not completion", "ACTIVE", "IMPLEMENTING")
	return mutationProof{Input: in, Result: got}
}
func (f *fixture) replayMutation(proof mutationProof) {
	before := f.managedFileSnapshot()
	got := f.s.agent.call("file_write", proof.Input)
	f.s.check("historical "+field(proof.Input, "action")+" receipt does not reapply old effect", reflect.DeepEqual(got, proof.Result) && reflect.DeepEqual(before, f.managedFileSnapshot()), got)
}
func (f *fixture) denyMutation(in map[string]any, code string) {
	before := f.managedFileSnapshot()
	f.s.agent.deny("file_write", in, code)
	f.s.check("rejected mutation preserves every managed file", reflect.DeepEqual(before, f.managedFileSnapshot()), nil)
}
func (f *fixture) checkMemberFiles(expected map[string]map[string]string) {
	for _, target := range f.targets {
		args := f.readArgs(target)
		args["path"] = "."
		listed := f.s.agent.call("file_list", args)
		var result directfile.Result
		decode(listed, &result)
		want := expected[target.MemberID]
		f.s.check("member file list is complete and exact", !result.Truncated && len(result.Entries) == len(want), listed)
		seen := map[string]bool{}
		for _, entry := range result.Entries {
			content, ok := want[entry.Name]
			f.s.check("no cross-member or unapproved file effect", ok && !seen[entry.Name] && entry.Type == "file", entry)
			seen[entry.Name] = true
			args["path"] = entry.Name
			read := f.s.agent.call("file_read", args)
			f.s.check("exact member has independently expected bytes and digest", read["content"] == content && read["digest"] == digestText(content), read)
		}
	}
	f.checkSources()
}
func (f *fixture) checkPartialFiles(label, phase, grantState string) {
	ws := f.checkPhase(label, "ACTIVE", phase)
	f.checkFrozenStartDefinition(ws)
	var state directfile.State
	decode(ws["taskView"].(map[string]any)["directFile"], &state)
	f.s.check(label+": exact authority state without Task completion", state.State == grantState && state.Enabled == (grantState == "ACTIVE") && state.WorkflowID == f.state.WorkflowID && state.RuntimeEpoch == f.state.RuntimeEpoch && state.GrantGeneration == f.state.GrantGeneration && state.TaskDefinitionRevisionID == f.revision && state.WorkspaceID == f.state.WorkspaceID && reflect.DeepEqual(state.RepositoryTargets, f.state.RepositoryTargets), state)
	f.checkAdmissionObjects(label, 1, 1)
	worker := f.s.agent.call("file_worker_get", f.claim)
	f.s.check("file effects do not invent formal worker result", worker["state"] == directfile.WorkerOutcomeUnknown && worker["executionPayload"] == nil && worker["resultReceipt"] == nil && reflect.DeepEqual(worker["receipt"], f.first["receipt"]), worker)
	f.s.gpt.deny("file_implementation_result_get", directfile.ImplementationResultReadInput{AISessionID: f.session, TaskID: f.task, TaskDefinitionRevisionID: f.revision, WorkflowID: f.state.WorkflowID}, "NOT_FOUND")
	f.checkSources()
}

func (s *scenario) fileMutationGates() {
	f := s.fixture
	expected := f.mutationSetup()
	proofs := []mutationProof{}
	// A generic workflowComplete hint must not bypass v2 formal result semantics.
	complete := f.mutationInput(f.targets[0], "CREATE", "created.txt", "forbidden-inline-completion", "must not exist", "", "")
	complete["workflowComplete"] = true
	f.denyMutation(complete, "v2 completion requires formal result submission")
	f.checkPartialFiles("rejected inline completion has no first effect", "IMPLEMENTATION_READY", "ACTIVE")
	firstTarget := f.targets[0]
	staleFirst := f.mutationInput(firstTarget, "REPLACE", "same.txt", "stale-before-first-effect", "forbidden stale first effect", strings.Repeat("0", 64), "")
	f.denyMutation(staleFirst, "mutation source digest changed")
	f.checkPartialFiles("failed digest does not begin implementation", "IMPLEMENTATION_READY", "ACTIVE")
	staleFirst["expectedDigest"] = digestText(expected[firstTarget.MemberID]["same.txt"])
	f.denyMutation(staleFirst, "direct-file mutation receipt conflicts")
	f.checkPartialFiles("failed request cannot be edited into a first effect", "IMPLEMENTATION_READY", "ACTIVE")
	for index, target := range f.targets {
		files := expected[target.MemberID]
		prefix := target.MemberID + "-"
		first := "SIMULATED created bytes for " + target.RepositoryID + "\n"
		create := f.mutate(f.mutationInput(target, "CREATE", "created.txt", prefix+"create", first, "", ""), "", digestText(first))
		proofs = append(proofs, create)
		files["created.txt"] = first
		f.checkMemberFiles(expected)
		f.checkPartialFiles("first or subsequent bounded CREATE", "IMPLEMENTING", "ACTIVE")
		f.replayMutation(create)
		conflict := object(create.Input)
		conflict["content"] = "changed replay must not overwrite"
		f.denyMutation(conflict, "direct-file mutation receipt conflicts")
		cross := object(create.Input)
		other := f.targets[1-index]
		cross["repositoryId"], cross["memberId"], cross["memberVersion"] = other.RepositoryID, other.MemberID, other.MemberVersion
		f.denyMutation(cross, "direct-file mutation receipt conflicts")
		exists := object(create.Input)
		exists["requestId"] = prefix + "create-no-overwrite"
		f.denyMutation(exists, "FILE_ALREADY_EXISTS")
		second := "SIMULATED replaced bytes for " + target.RepositoryID + "\n"
		replace := f.mutationInput(target, "REPLACE", "created.txt", prefix+"replace-stale", second, strings.Repeat("0", 64), "")
		f.denyMutation(replace, "mutation source digest changed")
		replace["expectedDigest"] = digestText(first)
		f.denyMutation(replace, "direct-file mutation receipt conflicts") // failed request IDs are not editable
		replace["requestId"] = prefix + "replace-fresh-observed"
		replaced := f.mutate(replace, digestText(first), digestText(second))
		proofs = append(proofs, replaced)
		files["created.txt"] = second
		f.replayMutation(create) // returns old proof, never old bytes
		f.checkMemberFiles(expected)
		move := f.mutationInput(target, "MOVE", "created.txt", prefix+"move", "", digestText(second), "moved.txt")
		existing := object(move)
		existing["requestId"], existing["destination"] = prefix+"move-existing", "same.txt"
		f.denyMutation(existing, "FILE_ALREADY_EXISTS")
		cross = object(move)
		cross["requestId"], cross["destinationRepositoryId"], cross["destinationMemberId"] = prefix+"cross-move", other.RepositoryID, other.MemberID
		f.denyMutation(cross, "CROSS_REPOSITORY_MOVE_UNSUPPORTED")
		outside := object(move)
		outside["requestId"], outside["destination"] = prefix+"move-outside", "outside.txt"
		f.denyMutation(outside, "path is outside approved write scope")
		moved := f.mutate(move, digestText(second), digestText(second))
		proofs = append(proofs, moved)
		delete(files, "created.txt")
		files["moved.txt"] = second
		f.replayMutation(create)
		f.replayMutation(replaced)
		f.checkMemberFiles(expected)
		deleted := f.mutate(f.mutationInput(target, "DELETE", "moved.txt", prefix+"delete", "", digestText(second), ""), digestText(second), "ABSENT")
		proofs = append(proofs, deleted)
		delete(files, "moved.txt")
		f.replayMutation(moved)
		f.checkMemberFiles(expected)
		reborn := "SIMULATED new object after historical delete for " + target.RepositoryID + "\n"
		recreated := f.mutate(f.mutationInput(target, "CREATE", "moved.txt", prefix+"recreate", reborn, "", ""), "", digestText(reborn))
		proofs = append(proofs, recreated)
		files["moved.txt"] = reborn
		f.replayMutation(deleted) // must not delete the new object at the same path
		f.checkMemberFiles(expected)
	}
	f.checkPartialFiles("multiple bounded effects still have no result", "IMPLEMENTING", "ACTIVE")
	s.agent.deny("direct_file_disable", directfile.Scope{AISessionID: f.session, FeatureAreaID: f.area, WorkflowID: f.state.WorkflowID}, "v2 Direct File closure requires consumed CANCEL_DIRECT_FILE confirmation")
	prepared := f.prepare(f.directPreparation("CANCEL_DIRECT_FILE", "cancel-after-partial-effects"))
	f.checkPartialFiles("preparing cancellation does not cancel existing authority", "IMPLEMENTING", "ACTIVE")
	beforeLast := f.managedFileSnapshot()
	target := f.targets[0]
	content := "SIMULATED acknowledged effect after cancel preparation\n"
	last := f.mutate(f.mutationInput(target, "REPLACE", "same.txt", "before-consumed-cancel", content, digestText(expected[target.MemberID]["same.txt"]), ""), digestText(expected[target.MemberID]["same.txt"]), digestText(content))
	s.check("pending cancellation is not silently consumed", !reflect.DeepEqual(beforeLast, f.managedFileSnapshot()), nil)
	proofs = append(proofs, last)
	expected[target.MemberID]["same.txt"] = content
	f.checkMemberFiles(expected)
	input := admissionCommit(prepared, "cancel-partial-files-choice", c.Selection{})
	beforeCancel := f.managedFileSnapshot()
	canceled := f.commitAdmission(input)
	s.check("consumed cancel preserves every acknowledged partial effect", reflect.DeepEqual(beforeCancel, f.managedFileSnapshot()), nil)
	f.closedMutationProofs(prepared, input, canceled, proofs, last)
}

// This is a real public-request race, not a controlled pause inside the effect
// journal. Both legal lock orderings are accepted and the observed one is saved.
func (s *scenario) mutationCancelRace() {
	f := s.fixture
	expected := f.mutationSetup()
	target := f.targets[0]
	prime := f.mutate(f.mutationInput(target, "REPLACE", "same.txt", "prime-implementing", "SIMULATED stable IMPLEMENTING baseline\n", digestText(expected[target.MemberID]["same.txt"]), ""), digestText(expected[target.MemberID]["same.txt"]), digestText("SIMULATED stable IMPLEMENTING baseline\n"))
	prepared := f.prepare(f.directPreparation("CANCEL_DIRECT_FILE", "prepare-cancel-race"))
	input := admissionCommit(prepared, "confirm-cancel-race", c.Selection{})
	mutation := f.mutationInput(target, "REPLACE", "same.txt", "racing-file-effect", "SIMULATED racing acknowledged effect\n", field(prime.Result, "digest"), "")
	before := f.managedFileSnapshot()
	gate := make(chan struct{})
	var wg sync.WaitGroup
	var written, closed callOutcome
	wg.Add(2)
	go func() { defer wg.Done(); <-gate; written = s.agent.invoke("downstream_agent", "file_write", mutation) }()
	go func() { defer wg.Done(); <-gate; closed = s.gpt.invoke("fixture_user", "confirmation_commit", input) }()
	close(gate)
	wg.Wait()
	s.must(written.Err)
	s.must(closed.Err)
	s.check("concurrent explicit cancel completes", !closed.Denied, closed.ErrorText)
	var canceled c.Intent
	decode(closed.Value["intent"], &canceled)
	s.check("race cancellation consumed exact original user choice", canceled.State == "COMPLETED" && canceled.Submission != nil && canceled.Submission.PayloadSHA256 == c.ValueHash(input) && canceled.Submission.Outcome == c.Consumed, canceled)
	proofs := []mutationProof{prime}
	last := prime
	if written.Denied {
		s.check("cancel-first race denies new effect without filesystem change", strings.Contains(written.ErrorText, "exact v2 file workflow is not active") && reflect.DeepEqual(before, f.managedFileSnapshot()), written.ErrorText)
		s.record(map[string]any{"kind": "ordering", "observed": "CANCEL_BEFORE_MUTATION"})
	} else {
		s.check("write-first race has exact acknowledged effect before closure", written.Value["path"] == "same.txt" && written.Value["workflowComplete"] == false && written.Value["digest"] == digestText(field(mutation, "content")) && mutationDelta(before, f.managedFileSnapshot(), "REPLACE", "same.txt", "", field(prime.Result, "digest"), digestText(field(mutation, "content"))), written.Value)
		last = mutationProof{Input: mutation, Result: written.Value}
		proofs = append(proofs, last)
		s.record(map[string]any{"kind": "ordering", "observed": "MUTATION_BEFORE_CANCEL"})
	}
	f.closedMutationProofs(prepared, input, canceled, proofs, last)
}

func (f *fixture) closedMutationProofs(prepared c.Intent, input c.CommitInput, canceled c.Intent, proofs []mutationProof, last mutationProof) {
	s := f.s
	s.check("cancellation closes one workflow without creating a Run", len(canceled.Steps) == 1 && canceled.Steps[0].Kind == "CANCEL_DIRECT_FILE" && canceled.Steps[0].WorkflowID == f.state.WorkflowID && canceled.Steps[0].RunID == "" && canceled.Steps[0].ExecutionID == "", canceled.Steps)
	baseline := f.managedFileSnapshot()
	f.checkPartialFiles("canceled partial work is neither rollback nor Task cancellation", "IMPLEMENTING", "CLOSED")
	f.checkAdmissionReplay(canceled, f.commitAdmission(input))
	for _, proof := range proofs {
		f.replayMutation(proof)
	}
	late := object(last.Input)
	late["requestId"], late["content"], late["expectedDigest"] = "late-after-consumed-cancel", "forbidden late bytes", last.Result["digest"]
	f.denyMutation(late, "exact v2 file workflow is not active")
	s.check("cancellation replay and historical proof preserve partial effects", reflect.DeepEqual(baseline, f.managedFileSnapshot()), nil)
	s.stop(false)
	s.launch()
	f.checkAdmissionReplay(canceled, f.readAdmission(prepared, input.RequestID))
	f.checkAdmissionReplay(canceled, f.commitAdmission(input))
	for _, proof := range proofs {
		f.replayMutation(proof)
	}
	f.denyMutation(late, "exact v2 file workflow is not active")
	f.checkPartialFiles("cold closed workflow does not invent result or recovery", "IMPLEMENTING", "CLOSED")
	s.check("cold closed history never reapplies or rolls back bytes", reflect.DeepEqual(baseline, f.managedFileSnapshot()), nil)
}

// Independent filesystem oracle: exactly the claimed operation delta, including
// unapproved new files. It does not inspect Core's database or invent evidence.
func mutationDelta(before, after map[string]string, kind, path, destination, oldDigest, newDigest string) bool {
	changed := []string{}
	for p, v := range before {
		if after[p] != v {
			changed = append(changed, p)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			changed = append(changed, p)
		}
	}
	matches := func(p, want string) bool { return strings.HasSuffix(filepath.ToSlash(p), "/"+want) }
	if kind == "MOVE" {
		if len(changed) != 2 {
			return false
		}
		var removed, added string
		for _, p := range changed {
			if _, ok := after[p]; !ok {
				removed = p
			}
			if _, ok := before[p]; !ok {
				added = p
			}
		}
		return removed != "" && added != "" && filepath.Dir(removed) == filepath.Dir(added) && matches(removed, path) && matches(added, destination) && before[removed] == oldDigest && after[added] == newDigest
	}
	if len(changed) != 1 {
		return false
	}
	p := changed[0]
	if !matches(p, path) {
		return false
	}
	switch kind {
	case "CREATE":
		_, existed := before[p]
		return !existed && after[p] == newDigest
	case "REPLACE":
		_, existed := before[p]
		_, exists := after[p]
		return existed && exists && before[p] == oldDigest && after[p] == newDigest
	case "DELETE":
		_, exists := after[p]
		return !exists && before[p] == oldDigest && newDigest == "ABSENT"
	}
	return false
}
