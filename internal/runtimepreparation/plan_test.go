package runtimepreparation

import (
	"strings"
	"testing"
	"time"

	"github.com/lazyun/devbridge/internal/workset"
)

func TestPlanContainsIntendedScopeOnly(t *testing.T) {
	head := strings.Repeat("a", 40)
	p := Plan{ID: "plan", Generation: 1, OwnerKind: OwnerReview, OwnerID: "review", ProjectID: "project", TaskID: "task", SourceCommand: "START_REVIEW", RepositoryTargets: []workset.Target{{RepositoryID: "repo", MemberID: "member", MemberVersion: 1, OriginCommit: head, Read: true, Write: workset.WriteScope{Kind: "NONE"}}}, PrimaryMemberID: "member", BackendIdentity: "backend", Model: "model", ReasoningEffort: "medium", RuntimeAdapterIdentity: "adapter", DevelopmentContextHash: "context", ExpectedSourceHEADs: map[string]string{"member": head}, PreparationRequestID: "request", PreparationPayloadHash: strings.Repeat("b", 64), CreatedAt: time.Now().UTC()}
	hash, err := p.ComputeContentHash()
	if err != nil {
		t.Fatal(err)
	}
	p.ContentHash = hash
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.RepositoryTargets[0].ObservedCommit = head
	if err := p.Validate(); err == nil {
		t.Fatal("actual observation accepted in intended plan")
	}
}
