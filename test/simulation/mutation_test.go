package simulation

import "testing"

func TestMutationDeltaRejectsUnrelatedEffects(t *testing.T) {
	source := map[string]string{"app/same.txt": "old", "sdk/same.txt": "sdk"}
	cases := []struct {
		name, kind, path, destination, oldDigest, newDigest string
		before, after                                       map[string]string
		want                                                bool
	}{
		{"replace", "REPLACE", "same.txt", "", "old", "new", source, map[string]string{"app/same.txt": "new", "sdk/same.txt": "sdk"}, true},
		{"other member also changed", "REPLACE", "same.txt", "", "old", "new", source, map[string]string{"app/same.txt": "new", "sdk/same.txt": "new"}, false},
		{"extra file", "REPLACE", "same.txt", "", "old", "new", source, map[string]string{"app/same.txt": "new", "sdk/same.txt": "sdk", "app/extra": "x"}, false},
		{"wrong source", "REPLACE", "same.txt", "", "foreign", "new", source, map[string]string{"app/same.txt": "new", "sdk/same.txt": "sdk"}, false},
		{"create", "CREATE", "new.txt", "", "", "new", source, map[string]string{"app/same.txt": "old", "sdk/same.txt": "sdk", "app/new.txt": "new"}, true},
		{"create cannot overwrite", "CREATE", "same.txt", "", "", "new", source, map[string]string{"app/same.txt": "new", "sdk/same.txt": "sdk"}, false},
		{"delete", "DELETE", "same.txt", "", "old", "ABSENT", source, map[string]string{"sdk/same.txt": "sdk"}, true},
		{"delete cannot remove wrong path", "DELETE", "other.txt", "", "old", "ABSENT", source, map[string]string{"sdk/same.txt": "sdk"}, false},
		{"move", "MOVE", "same.txt", "moved.txt", "old", "old", source, map[string]string{"app/moved.txt": "old", "sdk/same.txt": "sdk"}, true},
		{"move cross root", "MOVE", "same.txt", "moved.txt", "old", "old", source, map[string]string{"other/moved.txt": "old", "sdk/same.txt": "sdk"}, false},
		{"move wrong bytes", "MOVE", "same.txt", "moved.txt", "old", "old", source, map[string]string{"app/moved.txt": "new", "sdk/same.txt": "sdk"}, false},
		{"replace requires existing source", "REPLACE", "new.txt", "", "", "new", source, map[string]string{"app/same.txt": "old", "sdk/same.txt": "sdk", "app/new.txt": "new"}, false},
		{"no effect is not requested change", "REPLACE", "same.txt", "", "old", "new", source, source, false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := mutationDelta(tt.before, tt.after, tt.kind, tt.path, tt.destination, tt.oldDigest, tt.newDigest); got != tt.want {
				t.Fatalf("delta=%v want %v", got, tt.want)
			}
		})
	}
}
