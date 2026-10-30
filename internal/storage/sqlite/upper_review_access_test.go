package sqlite

import "testing"

func TestUpperReviewPathScopePreservesFrozenScopeAndExplicitForbiddenPaths(t *testing.T) {
	for _, test := range []struct {
		path string
		want bool
	}{
		{"production/main.go", true},
		{"production/private/secret.go", false},
		{"other/main.go", false},
		{"production_test.go", false},
	} {
		if got := upperPathAllowed(test.path, []string{"production", "production_test.go"}, []string{"production/private", "production_test.go"}); got != test.want {
			t.Fatalf("path %q allowed=%v want %v", test.path, got, test.want)
		}
	}
}
