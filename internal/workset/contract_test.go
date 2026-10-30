package workset

import (
	"strings"
	"testing"
)

func TestFrozenWorksetIncludesExactRepositoryIdentityAndPermission(t *testing.T) {
	hash := strings.Repeat("a", 40)
	value := Set{Version: Version, WorkspaceID: "area-workspace", Targets: []Target{
		{RepositoryID: "app", MemberID: "member-app", MemberVersion: 1, OriginCommit: hash, Read: true, Write: WriteScope{Kind: "PATHS", Paths: []string{"src", "tests"}}},
		{RepositoryID: "sdk", MemberID: "member-sdk", MemberVersion: 1, OriginCommit: hash, Read: true, Write: WriteScope{Kind: "NONE"}},
	}}
	fingerprint, err := value.Fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*Set){
		"same hash in different repository": func(s *Set) { s.Targets[0].RepositoryID = "backend" },
		"member version":                    func(s *Set) { s.Targets[0].MemberVersion++ },
		"write expansion":                   func(s *Set) { s.Targets[1].Write.Kind = "REPOSITORY" },
		"origin drift":                      func(s *Set) { s.Targets[0].OriginCommit = strings.Repeat("b", 40) },
		"snapshot observation":              func(s *Set) { s.Targets[0].ObservedCommit = strings.Repeat("b", 40) },
		"workspace replacement":             func(s *Set) { s.WorkspaceID = "replacement" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			copy := value
			copy.Targets = CloneTargets(value.Targets)
			change(&copy)
			got, err := copy.Fingerprint()
			if err != nil {
				t.Fatal(err)
			}
			if got == fingerprint {
				t.Fatal("changed authority retained fingerprint")
			}
		})
	}
	reversed := value
	reversed.Targets = CloneTargets(value.Targets)
	reversed.Targets[0], reversed.Targets[1] = reversed.Targets[1], reversed.Targets[0]
	reversed.Targets[1].Write.Paths[0], reversed.Targets[1].Write.Paths[1] = reversed.Targets[1].Write.Paths[1], reversed.Targets[1].Write.Paths[0]
	got, err := reversed.Fingerprint()
	if err != nil || got != fingerprint {
		t.Fatalf("set ordering: %s %v", got, err)
	}
	if value.Targets[0].Write.Paths[0] != "src" {
		t.Fatal("fingerprint or clone mutated authored input")
	}
}
func TestWorksetRejectsAmbiguousAuthority(t *testing.T) {
	target := Target{RepositoryID: "app", MemberID: "member-app", MemberVersion: 1, OriginCommit: strings.Repeat("a", 40), Read: true, Write: WriteScope{Kind: "PATHS", Paths: []string{"src"}}}
	for _, p := range []string{"../sdk/file", "src/../../sdk", "/absolute", "src/../file", "src\\file", ".", "src\x00file"} {
		t.Run(p, func(t *testing.T) {
			copy := target
			copy.Write.Paths = []string{p}
			if ValidateTargets([]Target{copy}) == nil {
				t.Fatal("accepted path outside canonical repository-relative scope")
			}
		})
	}
	if ValidateTargets([]Target{target, target}) == nil {
		t.Fatal("duplicate target accepted")
	}
	target.Read = false
	if ValidateTargets([]Target{target}) == nil {
		t.Fatal("implicit read permission accepted")
	}
}
