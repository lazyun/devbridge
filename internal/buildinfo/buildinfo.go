// Package buildinfo provides linker-injectable Dev Bridge build metadata.
package buildinfo

import "strings"

const DefaultProductVersion = "0.1.0-dev"

// ProductVersion and GitCommit may be set with go build -ldflags -X.
var (
	ProductVersion = DefaultProductVersion
	GitCommit      string
)

// Version returns the product version with a short Git commit suffix when the
// build supplied a commit. It never inspects Git at runtime.
func Version() string {
	version := strings.TrimSpace(ProductVersion)
	if version == "" {
		version = DefaultProductVersion
	}
	commit := strings.TrimSpace(GitCommit)
	if !isHexCommit(commit) {
		return version
	}
	if len(commit) > 7 {
		commit = commit[:7]
	}
	return version + "+g" + strings.ToLower(commit)
}

func isHexCommit(value string) bool {
	if len(value) < 7 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') && (char < 'A' || char > 'F') {
			return false
		}
	}
	return true
}
