package review

import "testing"

func TestMatchRecordedPathUsesExactStructuredPathSemantics(t *testing.T) {
	recorded := []string{"internal/review/service.go", "internal/git", "docs/readme.md"}
	cases := []struct {
		changed string
		want    bool
	}{
		{"internal/review/service.go", true},       // exact file match
		{"internal/git/drift.go", true},            // directory-style recorded path contains it
		{"internal/git", true},                     // the recorded path itself changed
		{"docs/readme.md", true},                   // exact match
		{"internal/review/service_test.go", false}, // sibling file, not recorded
		{"internal/gitops/x.go", false},            // directory name prefix is not containment
		{"docs/readme.md.bak", false},              // near name is unrelated
		{"cmd/main.go", false},                     // unrelated
		{"", false},
	}
	for _, tc := range cases {
		if got := MatchRecordedPath(tc.changed, recorded); got != tc.want {
			t.Fatalf("MatchRecordedPath(%q) = %v, want %v", tc.changed, got, tc.want)
		}
	}
}

func TestMatchRecordedPathIgnoresEmptyRecords(t *testing.T) {
	if MatchRecordedPath("anything", nil) {
		t.Fatal("no recorded paths means no reviewed-path facts")
	}
	if MatchRecordedPath("anything", []string{"", " "}) {
		t.Fatal("blank recorded paths match nothing")
	}
}
