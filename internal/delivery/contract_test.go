package delivery

import "testing"

func TestFeatureAreaPlanDigestCoversEveryRepositoryAndCommit(t *testing.T) {
	plan := FeatureAreaPlan{ProjectID: "project", FeatureAreaID: "area", WorkspaceID: "workspace", Members: []FeatureAreaMember{
		{RepositoryID: "sdk", MemberID: "member-sdk", MemberVersion: 1, WorktreeID: "wt-sdk", SourcePath: "/tmp/sdk-source", SourceBranch: "area-sdk", SourceHead: "bbbb", ExpectedDevHead: "cccc", DevelopmentRef: "refs/heads/dev-sdk", DevelopmentCycleID: "cycle-sdk", TargetPath: "/tmp/sdk-target", TaskCommits: []string{"commit-2", "commit-1"}},
		{RepositoryID: "app", MemberID: "member-app", MemberVersion: 1, WorktreeID: "wt-app", SourcePath: "/tmp/app-source", SourceBranch: "area-app", SourceHead: "aaaa", ExpectedDevHead: "dddd", DevelopmentRef: "refs/heads/dev-app", DevelopmentCycleID: "cycle-app", TargetPath: "/tmp/app-target", TaskCommits: []string{"commit-3"}},
	}}
	if err := plan.Canonicalize(); err != nil || !plan.Valid() {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	digest := plan.SHA256
	if plan.Members[0].RepositoryID != "app" || plan.Members[1].TaskCommits[0] != "commit-1" {
		t.Fatalf("not canonical: %+v", plan)
	}
	plan.Members[1].ExpectedDevHead = "changed"
	if plan.Valid() || plan.SHA256 != digest {
		t.Fatal("target drift was not bound to digest")
	}
}
