package reviewaccess

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestBoundedStringValidUTF8Prefix512 tests only the existing pure helper at its
// sole production limit. Every expected string is fixed independently of the
// helper; neither Inspector nor any Root operation is constructed or called.
func TestBoundedStringValidUTF8Prefix512(t *testing.T) {
	const limit = 512
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "two_byte_split_after_one",
			input: strings.Repeat("a", 511) + "é",
			want:  strings.Repeat("a", 511),
		},
		{
			name:  "three_byte_split_after_one",
			input: strings.Repeat("a", 511) + "中",
			want:  strings.Repeat("a", 511),
		},
		{
			name:  "three_byte_split_after_two",
			input: strings.Repeat("a", 510) + "中",
			want:  strings.Repeat("a", 510),
		},
		{
			name:  "four_byte_split_after_one",
			input: strings.Repeat("a", 511) + "🙂",
			want:  strings.Repeat("a", 511),
		},
		{
			name:  "four_byte_split_after_two",
			input: strings.Repeat("a", 510) + "🙂",
			want:  strings.Repeat("a", 510),
		},
		{
			name:  "four_byte_split_after_three",
			input: strings.Repeat("a", 509) + "🙂",
			want:  strings.Repeat("a", 509),
		},
		{
			name:  "empty_unchanged",
			input: "",
			want:  "",
		},
		{
			name:  "short_unicode_unchanged",
			input: "é中🙂",
			want:  "é中🙂",
		},
		{
			name:  "ascii_511_unchanged",
			input: strings.Repeat("a", 511),
			want:  strings.Repeat("a", 511),
		},
		{
			name:  "ascii_512_unchanged",
			input: strings.Repeat("a", 512),
			want:  strings.Repeat("a", 512),
		},
		{
			name:  "ascii_513_truncated",
			input: strings.Repeat("a", 513),
			want:  strings.Repeat("a", 512),
		},
		{
			name:  "two_byte_exact_512_unchanged",
			input: strings.Repeat("a", 510) + "é",
			want:  strings.Repeat("a", 510) + "é",
		},
		{
			name:  "two_byte_exact_boundary_truncated",
			input: strings.Repeat("a", 510) + "éZ",
			want:  strings.Repeat("a", 510) + "é",
		},
		{
			name:  "three_byte_exact_512_unchanged",
			input: strings.Repeat("a", 509) + "中",
			want:  strings.Repeat("a", 509) + "中",
		},
		{
			name:  "three_byte_exact_boundary_truncated",
			input: strings.Repeat("a", 509) + "中Z",
			want:  strings.Repeat("a", 509) + "中",
		},
		{
			name:  "four_byte_exact_512_unchanged",
			input: strings.Repeat("a", 508) + "🙂",
			want:  strings.Repeat("a", 508) + "🙂",
		},
		{
			name:  "four_byte_exact_boundary_truncated",
			input: strings.Repeat("a", 508) + "🙂Z",
			want:  strings.Repeat("a", 508) + "🙂",
		},
		{
			name:  "valid_replacement_rune_preserved",
			input: strings.Repeat("a", 509) + "\uFFFDZ",
			want:  strings.Repeat("a", 509) + "\uFFFD",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !utf8.ValidString(tc.input) {
				t.Fatal("invalid test input: the contract covers valid UTF-8 only")
			}
			if !utf8.ValidString(tc.want) || len(tc.want) > limit || !strings.HasPrefix(tc.input, tc.want) {
				t.Fatal("invalid fixed expected prefix")
			}

			got := boundedString(tc.input, limit)
			t.Logf("inputBytes=%d outputBytes=%d expectedBytes=%d", len(tc.input), len(got), len(tc.want))
			if got != tc.want {
				t.Errorf("complete output mismatch: got %q; want %q", got, tc.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("output is not valid UTF-8: %q", got)
			}
			if len(got) > limit {
				t.Errorf("output has %d bytes, exceeds %d-byte limit", len(got), limit)
			}
			if !strings.HasPrefix(tc.input, got) {
				t.Errorf("output is not a byte prefix of the input: %q", got)
			}
		})
	}
}
