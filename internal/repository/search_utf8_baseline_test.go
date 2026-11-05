package repository

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	bridgegit "github.com/lazyun/devbridge/internal/git"
	"github.com/lazyun/devbridge/internal/reviewaccess"
)

// These fakes deliberately expose no executable Git client or real store.
// Only Get and SearchFixedRoot may be called by the selected service method.
type snippetBoundaryStore struct {
	value       Repository
	calls       int
	seenContext context.Context
	seenID      string
}

func (s *snippetBoundaryStore) Get(ctx context.Context, id string) (Repository, error) {
	s.calls++
	s.seenContext = ctx
	s.seenID = id
	return s.value, nil
}

func (*snippetBoundaryStore) PutIfAbsent(context.Context, Repository) (Repository, bool, error) {
	panic("unexpected Store.PutIfAbsent")
}

func (*snippetBoundaryStore) List(context.Context) ([]Repository, error) {
	panic("unexpected Store.List")
}

type snippetBoundaryGit struct {
	matches      []bridgegit.SearchMatch
	calls        int
	seenContext  context.Context
	seenRoot     *reviewaccess.Root
	seenRootPath string
	seenQuery    string
	seenPaths    []string
}

func (*snippetBoundaryGit) RepositoryRoot(context.Context, string) (string, error) {
	panic("unexpected Git.RepositoryRoot")
}

func (*snippetBoundaryGit) ResolveHEAD(context.Context, string) (string, error) {
	panic("unexpected Git.ResolveHEAD")
}

func (g *snippetBoundaryGit) SearchFixedRoot(ctx context.Context, root *reviewaccess.Root, query string, paths []string) ([]bridgegit.SearchMatch, error) {
	g.calls++
	g.seenContext = ctx
	g.seenRoot = root
	if root != nil {
		g.seenRootPath = root.Path()
	}
	g.seenQuery = query
	g.seenPaths = append([]string(nil), paths...)
	// Return the fixture itself so a production mutation would be observable.
	return g.matches, nil
}

type snippetBoundaryContextKey struct{}

func TestSearchSnippetPreservesValidUTF8Boundary(t *testing.T) {
	// The oracle is a finite table of fixed byte counts and literal code points.
	// It never slices the input, invokes the production limiter, or calculates
	// an expected prefix by applying the proposed fix.
	a508 := strings.Repeat("a", 508)
	a509 := strings.Repeat("a", 509)
	a510 := strings.Repeat("a", 510)
	a511 := strings.Repeat("a", 511)
	a512 := strings.Repeat("a", 512)
	a513 := strings.Repeat("a", 513)
	cases := []struct {
		name            string
		input           string
		want            string
		wantInputBytes  int
		wantOutputBytes int
	}{
		{"split_two_byte_after_511", a511 + "é", a511, 513, 511},
		{"split_three_byte_after_510", a510 + "€", a510, 513, 510},
		{"split_three_byte_after_511", a511 + "€", a511, 514, 511},
		{"split_four_byte_after_509", a509 + "😀", a509, 513, 509},
		{"split_four_byte_after_510", a510 + "😀", a510, 514, 510},
		{"split_four_byte_after_511", a511 + "😀", a511, 515, 511},
		{"ascii_511", a511, a511, 511, 511},
		{"ascii_512", a512, a512, 512, 512},
		{"ascii_513", a513, a512, 513, 512},
		{"two_byte_exact_512", a510 + "é", a510 + "é", 512, 512},
		{"three_byte_exact_512", a509 + "€", a509 + "€", 512, 512},
		{"four_byte_exact_512", a508 + "😀", a508 + "😀", 512, 512},
		{"two_byte_exact_512_then_ascii", a510 + "éz", a510 + "é", 513, 512},
		{"three_byte_exact_512_then_ascii", a509 + "€z", a509 + "€", 513, 512},
		{"four_byte_exact_512_then_ascii", a508 + "😀z", a508 + "😀", 513, 512},
		{"existing_replacement_code_point", a509 + "\uFFFDz", a509 + "\uFFFD", 513, 512},
		{"short_ascii", "a", "a", 1, 1},
		{"short_unicode", "éa", "éa", 3, 3},
	}
	if len(cases) != 18 {
		t.Fatalf("fixture count = %d, want 18", len(cases))
	}

	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const query = "a"
			if !utf8.ValidString(tc.input) || !utf8.ValidString(tc.want) {
				t.Fatal("fixture and expected snippet must be valid UTF-8")
			}
			if len(tc.input) != tc.wantInputBytes || len(tc.want) != tc.wantOutputBytes {
				t.Fatalf("fixture lengths = %d/%d, want %d/%d", len(tc.input), len(tc.want), tc.wantInputBytes, tc.wantOutputBytes)
			}
			if !strings.Contains(tc.input, query) || strings.ContainsAny(tc.input, "\x00\r\n") {
				t.Fatal("fixture must be a possible nonempty fixed-string matched text line")
			}
			if len(tc.want) > 512 || !strings.HasPrefix(tc.input, tc.want) {
				t.Fatal("fixed expected snippet is not a bounded source prefix")
			}

			// This newly created directory is empty. Search only opens/closes its
			// capability because paths is nil and all matching bytes are fake.
			rootPath := t.TempDir()
			ctx := context.WithValue(context.Background(), snippetBoundaryContextKey{}, tc.name)
			store := &snippetBoundaryStore{value: Repository{ID: "repo-snippet-fixture", ProjectID: "project-snippet-fixture", Name: "Snippet fixture", Path: rootPath}}
			originalRepository := store.value
			inputMatches := []bridgegit.SearchMatch{{Path: "src/example.go", Line: index + 1, Snippet: tc.input}}
			originalMatches := append([]bridgegit.SearchMatch(nil), inputMatches...)
			git := &snippetBoundaryGit{matches: inputMatches}
			service := &Service{
				store: store,
				git:   git,
				newID: func() (string, error) { panic("unexpected ID generation") },
				now:   func() time.Time { panic("unexpected clock call") },
			}
			var paths []string
			result, err := service.Search(ctx, originalRepository.ID, query, paths, 1)
			if err != nil {
				t.Fatalf("Service.Search returned error: %v", err)
			}

			if store.calls != 1 || store.seenContext != ctx || store.seenID != originalRepository.ID {
				t.Errorf("Store.Get forwarding: calls=%d sameContext=%t id=%q", store.calls, store.seenContext == ctx, store.seenID)
			}
			if git.calls != 1 || git.seenContext != ctx || git.seenRoot == nil || git.seenRootPath != rootPath || git.seenQuery != query || len(git.seenPaths) != 0 {
				t.Errorf("GitSearch forwarding: calls=%d sameContext=%t nonnilRoot=%t sameRootPath=%t query=%q paths=%v", git.calls, git.seenContext == ctx, git.seenRoot != nil, git.seenRootPath == rootPath, git.seenQuery, git.seenPaths)
			}
			if git.seenRoot != nil && git.seenRoot.Path() != "" {
				t.Error("Service.Search did not close the opened root capability")
			}
			if paths != nil || !reflect.DeepEqual(store.value, originalRepository) || !reflect.DeepEqual(inputMatches, originalMatches) || !reflect.DeepEqual(git.matches, originalMatches) {
				t.Error("Service.Search changed a supplied input fixture")
			}
			if result.Truncated || len(result.Matches) != 1 {
				t.Fatalf("result metadata: truncated=%t matchCount=%d, want false/1", result.Truncated, len(result.Matches))
			}
			got := result.Matches[0]
			if got.Path != originalMatches[0].Path || got.Line != originalMatches[0].Line {
				t.Errorf("match metadata: path=%q line=%d, want %q/%d", got.Path, got.Line, originalMatches[0].Path, originalMatches[0].Line)
			}
			if got.Snippet != tc.want {
				t.Errorf("snippet bytes differ from fixed oracle: got=%d want=%d", len(got.Snippet), len(tc.want))
			}
			if !utf8.ValidString(got.Snippet) {
				t.Error("valid source text became an invalid UTF-8 snippet")
			}
			if len(got.Snippet) > 512 || !strings.HasPrefix(tc.input, got.Snippet) {
				t.Errorf("snippet violates byte/prefix bound: bytes=%d sourcePrefix=%t", len(got.Snippet), strings.HasPrefix(tc.input, got.Snippet))
			}

			// Synthetic JSON only: this does not invoke an MCP/Core consumer,
			// transport, Review protocol, or a second implementation of its DTO.
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatalf("marshal synthetic SearchResult: %v", err)
			}
			var roundTrip SearchResult
			if err := json.Unmarshal(encoded, &roundTrip); err != nil {
				t.Fatalf("unmarshal synthetic SearchResult: %v", err)
			}
			wantResult := SearchResult{Matches: []SearchMatch{{Path: "src/example.go", Line: index + 1, Snippet: tc.want}}, Truncated: false}
			if !reflect.DeepEqual(roundTrip, wantResult) {
				t.Error("JSON round-trip text or metadata differs from fixed source-prefix oracle")
			}
		})
	}
}
