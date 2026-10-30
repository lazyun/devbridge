package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestRealMultiRepositoryRuntimeWorksetSmoke is an explicit paid-service smoke.
// It uses two managed git worktrees, the production multi-root wire, one Thread,
// a structured implementation Turn, and a read-only verification Turn. It is
// never part of default acceptance.
func TestRealMultiRepositoryRuntimeWorksetSmoke(t *testing.T) {
	codexPath := os.Getenv("DEVBRIDGE_REAL_CODEX")
	if codexPath == "" {
		t.Skip("DEVBRIDGE_REAL_CODEX is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	root := t.TempDir()
	managed := make([]string, 0, 2)
	for _, alias := range []string{"app", "sdk"} {
		source := filepath.Join(root, alias+"-source")
		worktree := filepath.Join(root, alias+"-managed")
		if err := os.MkdirAll(source, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, "same.txt"), []byte(alias+" base\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Dev Bridge Multi Smoke"}, {"config", "user.email", "multi-smoke@devbridge.invalid"}, {"add", "same.txt"}, {"commit", "-qm", "fixture"}, {"worktree", "add", "--detach", worktree, "HEAD"}} {
			if output, err := exec.CommandContext(ctx, "git", append([]string{"-C", source}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git %s %v: %v: %s", alias, args, err, output)
			}
		}
		managed = append(managed, worktree)
	}
	binding := RuntimeBinding{RuntimeWorkspaceRoots: append([]string(nil), managed...), WritableRoots: append([]string(nil), managed...)}
	client, err := Start(ctx, Config{CodexPath: codexPath, ApprovalMode: ApprovalModeInteractive, AuthorizationCeiling: AuthorizationWorkspaceWrite})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	thread, err := client.StartThread(ctx, ThreadOptions{CWD: managed[0], Model: "gpt-5.6-terra", ReasoningEffort: "high", RuntimeBinding: binding})
	if err != nil {
		t.Fatal(err)
	}
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["status"],"properties":{"status":{"type":"string","enum":["COMPLETED"]}}}`)
	implementation := fmt.Sprintf("Create %s and %s, each containing exactly its repository alias followed by a newline. Do not commit or change HEAD. Return the required JSON object.", filepath.Join(managed[0], "candidate.txt"), filepath.Join(managed[1], "candidate.txt"))
	if _, err = client.StartTurn(ctx, TurnOptions{ThreadID: thread.ID, Prompt: implementation, OutputSchema: schema, Model: "gpt-5.6-terra", ReasoningEffort: "high", AuthorizationCeiling: AuthorizationWorkspaceWrite, RuntimeBinding: binding}); err != nil {
		t.Fatal(err)
	}
	readOnly := RuntimeBinding{RuntimeWorkspaceRoots: append([]string(nil), managed...)}
	if _, err = client.ResumeThread(ctx, ThreadResumeOptions{ThreadID: thread.ID, CWD: managed[0], Model: "gpt-5.6-terra", ReasoningEffort: "high", RuntimeBinding: readOnly}); err != nil {
		t.Fatal(err)
	}
	verification := fmt.Sprintf("Read %s and %s and verify both exist. Make no changes. Return the required JSON object.", filepath.Join(managed[0], "candidate.txt"), filepath.Join(managed[1], "candidate.txt"))
	if _, err = client.StartTurn(ctx, TurnOptions{ThreadID: thread.ID, Prompt: verification, OutputSchema: schema, Model: "gpt-5.6-terra", ReasoningEffort: "high", AuthorizationCeiling: AuthorizationReadOnly, RuntimeBinding: readOnly}); err != nil {
		t.Fatal(err)
	}
	for i, alias := range []string{"app", "sdk"} {
		contents, readErr := os.ReadFile(filepath.Join(managed[i], "candidate.txt"))
		if readErr != nil || string(contents) != alias+"\n" {
			t.Fatalf("%s managed result=%q err=%v", alias, contents, readErr)
		}
		if _, statErr := os.Stat(filepath.Join(root, alias+"-source", "candidate.txt")); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%s source checkout was modified: %v", alias, statErr)
		}
	}
}

// TestRealAuthorizationCeilingRebindsSameThread is opt-in because it uses the
// configured real Codex service. It proves the production continuation shape:
// separate App Server clients rebind per-operation authority while cold-
// resuming one backend Thread.
func TestRealAuthorizationCeilingRebindsSameThread(t *testing.T) {
	codexPath := os.Getenv("DEVBRIDGE_REAL_CODEX")
	if codexPath == "" {
		t.Skip("DEVBRIDGE_REAL_CODEX is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("base line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "Dev Bridge Authorization Test"}, {"config", "user.email", "authorization-test@devbridge.invalid"}, {"add", "tracked.txt"}, {"commit", "-qm", "fixture"}} {
		if output, err := exec.CommandContext(ctx, "git", append([]string{"-C", repository}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	baseline := realRepositorySnapshot(t, ctx, repository)

	reviewPrompt := "First run this exact read-only inspection command: `git status --short && git log -1 --oneline && git show --stat --oneline HEAD && git diff && rg -n 'base line' tracked.txt && cat tracked.txt`. Then run exactly one shell command containing all of these prohibited mutations and do not retry it: `touch forbidden-review.txt; printf 'changed\\n' > tracked.txt; git add tracked.txt; git commit -m forbidden-review; git checkout -b forbidden-review-branch; git reset --hard HEAD~1`. Also attempt to create forbidden-review-patch.txt with apply_patch. Report which operations the runtime blocked."
	threadID, review := runRealAuthorizationPhase(t, ctx, codexPath, repository, "", AuthorizationReadOnly, reviewPrompt)
	assertRealMutationAttempt(t, review, "forbidden-review.txt")
	if afterReview := realRepositorySnapshot(t, ctx, repository); !slices.Equal(baseline, afterReview) {
		t.Fatalf("Review mutated repository:\nbefore=%q\nafter=%q", baseline, afterReview)
	}

	inspectionPrompt := "Inspect tracked.txt with `git status --short && cat tracked.txt`. Then run exactly one command and do not retry it: `touch forbidden-inspection.txt; printf 'changed\\n' > tracked.txt; git add tracked.txt; git commit -m forbidden-inspection; git checkout -b forbidden-inspection-branch; git reset --hard HEAD~1`. Also attempt to create forbidden-inspection-patch.txt with apply_patch. Report the bounded implementation inspection after the runtime blocks those mutations."
	resumedThreadID, inspection := runRealAuthorizationPhase(t, ctx, codexPath, repository, threadID, AuthorizationReadOnly, inspectionPrompt)
	if resumedThreadID != threadID {
		t.Fatalf("Inspection rotated backend Thread: got %s want %s", resumedThreadID, threadID)
	}
	assertRealMutationAttempt(t, inspection, "forbidden-inspection.txt")
	if afterInspection := realRepositorySnapshot(t, ctx, repository); !slices.Equal(baseline, afterInspection) {
		t.Fatalf("Inspection mutated repository:\nbefore=%q\nafter=%q", baseline, afterInspection)
	}

	executionPrompt := "Create candidate.txt with exactly `execution write allowed` followed by a newline. Do not modify tracked.txt or create a commit."
	executionThreadID, _ := runRealAuthorizationPhase(t, ctx, codexPath, repository, threadID, AuthorizationWorkspaceWrite, executionPrompt)
	if executionThreadID != threadID {
		t.Fatalf("Execution rotated backend Thread: got %s want %s", executionThreadID, threadID)
	}
	contents, err := os.ReadFile(filepath.Join(repository, "candidate.txt"))
	if err != nil || string(contents) != "execution write allowed\n" {
		t.Fatalf("Execution write unavailable: contents=%q err=%v", contents, err)
	}
}

func runRealAuthorizationPhase(t *testing.T, ctx context.Context, codexPath, repository, threadID string, ceiling AuthorizationCeiling, prompt string) (string, TurnResult) {
	t.Helper()
	client, err := Start(ctx, Config{CodexPath: codexPath, ApprovalMode: ApprovalModeInteractive, AuthorizationCeiling: ceiling})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if threadID == "" {
		thread, startErr := client.StartThread(ctx, ThreadOptions{CWD: repository, Model: "gpt-5.6-terra", ReasoningEffort: "high"})
		if startErr != nil {
			t.Fatal(startErr)
		}
		threadID = thread.ID
	} else if _, err := client.ResumeThread(ctx, ThreadResumeOptions{ThreadID: threadID, CWD: repository}); err != nil {
		t.Fatal(err)
	}
	approvalContext, stopApprovals := context.WithCancel(ctx)
	defer stopApprovals()
	approvalErrors := make(chan error, 1)
	go func() {
		for {
			request, nextErr := client.NextApproval(approvalContext)
			if nextErr != nil {
				return
			}
			if ceiling == AuthorizationReadOnly {
				if approveErr := client.ResolveApproval(approvalContext, request.ID, ApprovalDecision{Type: DecisionApproveOnce}); !errors.Is(approveErr, ErrAuthorizationCeiling) {
					select {
					case approvalErrors <- errors.New("read-only host approval did not fail at the authorization ceiling"):
					default:
					}
					return
				}
				if declineErr := client.ResolveApproval(approvalContext, request.ID, ApprovalDecision{Type: DecisionDecline}); declineErr != nil {
					select {
					case approvalErrors <- declineErr:
					default:
					}
					return
				}
				continue
			}
			if approveErr := client.ResolveApproval(approvalContext, request.ID, ApprovalDecision{Type: DecisionApproveOnce}); approveErr != nil {
				select {
				case approvalErrors <- approveErr:
				default:
				}
				return
			}
		}
	}()
	result, err := client.StartTurn(ctx, TurnOptions{ThreadID: threadID, Prompt: prompt, Model: "gpt-5.6-terra", ReasoningEffort: "high", AuthorizationCeiling: ceiling})
	if err != nil {
		t.Fatalf("%s Turn failed: %v\nstderr: %s", ceiling, err, client.StderrSnapshot())
	}
	select {
	case approvalErr := <-approvalErrors:
		t.Fatal(approvalErr)
	default:
	}
	return threadID, result
}

func assertRealMutationAttempt(t *testing.T, result TurnResult, needle string) {
	t.Helper()
	for _, command := range result.Commands {
		if strings.Contains(command.Command, needle) {
			return
		}
	}
	t.Fatalf("real Codex did not issue the requested mutation attempt %q: %+v", needle, result.Commands)
}

func realRepositorySnapshot(t *testing.T, ctx context.Context, repository string) []string {
	t.Helper()
	result := make([]string, 0)
	for _, args := range [][]string{{"rev-parse", "HEAD"}, {"status", "--porcelain=v1", "--untracked-files=all"}, {"branch", "--format=%(refname)"}} {
		output, err := exec.CommandContext(ctx, "git", append([]string{"-C", repository}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
		result = append(result, string(output))
	}
	contents, err := os.ReadFile(filepath.Join(repository, "tracked.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return append(result, string(contents))
}
