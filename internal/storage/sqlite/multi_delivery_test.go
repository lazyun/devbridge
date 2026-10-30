package sqlite

import "testing"

func TestCanonicalDeliveryPathsRejectsAmbiguousOrEscapingPaths(t *testing.T) {
	paths, err := canonicalDeliveryPaths([]string{"b.txt", "a.txt"})
	if err != nil || len(paths) != 2 || paths[0] != "a.txt" {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	for _, value := range [][]string{{"a.txt", "a.txt"}, {"a/../b"}, {"../b"}, {"/b"}, {"a\\b"}, {" a.txt"}} {
		if _, err := canonicalDeliveryPaths(value); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}
