package simulation

import (
	"reflect"
	"testing"

	"github.com/lazyun/devbridge/internal/directfile"
)

func TestGitBlobOraclePreservesExactBytes(t *testing.T) {
	if got := gitBlobSHA1(""); got != "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391" {
		t.Fatal(got)
	}
	if got := gitBlobSHA1("test\n"); got != "9daeafb9864cf43055ae93beb0afd6c7d144bfa4" {
		t.Fatal(got)
	}
	if gitBlobSHA1("test\n") == gitBlobSHA1("test") {
		t.Fatal("lost final newline")
	}
}
func TestStableFileResultNormalizesOnlyReplay(t *testing.T) {
	original := directfile.ImplementationResult{ID: "result", Replay: true, Accepted: false, VerificationStatus: "NOT_RUN", Members: []directfile.MemberResult{{State: "NO_CHANGE", StartSnapshot: "start", EndSnapshot: "start"}}}
	normalized := stableFileResult(original)
	if normalized.Replay || !original.Replay {
		t.Fatal("modified wrong replay flag")
	}
	original.Replay = false
	if !reflect.DeepEqual(normalized, original) {
		t.Fatal("discarded immutable result fields")
	}
	different := original
	different.Accepted = true
	if reflect.DeepEqual(stableFileResult(different), normalized) {
		t.Fatal("hid acceptance difference")
	}
}
