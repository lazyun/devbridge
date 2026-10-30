package buildinfo

import "testing"

func TestVersionDefaultsToDevelopmentProductVersion(t *testing.T) {
	setBuildMetadata(t, "", "")
	if got := Version(); got != DefaultProductVersion {
		t.Fatalf("Version() = %q, want %q", got, DefaultProductVersion)
	}
}

func TestVersionIncludesInjectedCommit(t *testing.T) {
	setBuildMetadata(t, "0.1.0-dev", "0123456789ABCDEF0123456789ABCDEF01234567")
	if got := Version(); got != "0.1.0-dev+g0123456" {
		t.Fatalf("Version() = %q", got)
	}
}

func TestVersionIgnoresInvalidCommitMetadata(t *testing.T) {
	setBuildMetadata(t, "0.1.0-dev", "not-a-commit")
	if got := Version(); got != "0.1.0-dev" {
		t.Fatalf("Version() = %q", got)
	}
}

func setBuildMetadata(t *testing.T, version, commit string) {
	t.Helper()
	oldVersion, oldCommit := ProductVersion, GitCommit
	ProductVersion, GitCommit = version, commit
	t.Cleanup(func() { ProductVersion, GitCommit = oldVersion, oldCommit })
}
