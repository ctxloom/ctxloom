package gitignore

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// U054-F14 parity: `dedupe` and `missingPatterns` were two map-based filters
// over the same []string, and `dedupe` existed ONLY to compensate for
// missingPatterns deduping against the FILE and not within the requested set.
// The two must agree: filtering a duplicate-bearing request list against an
// EMPTY file is exactly "dedupe the request list". This was RED before the
// collapse (missingPatterns emitted the repeated pattern twice, so the same
// line would be written to .gitignore twice) and is the pin afterwards.
func TestMissingPatterns_DedupesWithinTheRequestedSet(t *testing.T) {
	cases := []struct {
		name     string
		patterns []string
		want     []string
	}{
		{name: "no duplicates", patterns: []string{"a", "b"}, want: []string{"a", "b"}},
		{name: "adjacent duplicate", patterns: []string{"a", "a", "b"}, want: []string{"a", "b"}},
		{name: "separated duplicate", patterns: []string{"a", "b", "a"}, want: []string{"a", "b"}},
		{name: "all duplicates", patterns: []string{"a", "a", "a"}, want: []string{"a"}},
		{
			name:     "private-state prepend collides with caller list",
			patterns: append(append([]string{}, PrivateStatePatterns...), PrivateStatePatterns[0], "extra"),
			want:     append(append([]string{}, PrivateStatePatterns...), "extra"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Against an empty file nothing is already present, so the only
			// filtering left is the within-set dedup dedupe() used to do.
			assert.Equal(t, tc.want, missingPatterns(nil, tc.patterns),
				"missingPatterns must emit each pattern at most once")
		})
	}
}

// The file-based half must keep working unchanged: a pattern already present
// in the file is dropped regardless of how many times it is requested.
func TestMissingPatterns_StillFiltersAgainstTheFile(t *testing.T) {
	content := []byte("# header\n  a  \nc\n")
	assert.Equal(t, []string{"b"}, missingPatterns(content, []string{"a", "b", "b", "c"}))
	assert.Nil(t, missingPatterns(content, []string{"a", "c", "a"}))
}
