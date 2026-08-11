package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ltkengine "github.com/ctxloom/ctxloom/internal/ltk/engine"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// engineNameCorpus is every spelling of an engine name either registry is
// expected to accept, plus the shapes that must be refused.
var engineNameCorpus = []struct {
	in   string
	want string // canonical name, or "" when the spelling names no engine
}{
	{"claude-code", "claude-code"},
	{"CLAUDE-CODE", "claude-code"},
	{"claudecode", "claude-code"},
	{"claude", "claude-code"},
	{"CLAUDE", "claude-code"},
	{"antigravity", ""}, // removed engine (0.7.0): no longer resolves anywhere
	{"agy", ""},         // its former alias, also removed
	{"antigravity-cli", ""},
	{"", ""},
	{"claude-", ""},
	{"clau", ""},
	{"antigrav", ""},
	{"nonsense", ""},
}

// TestEngineNameVocabularyParity pins the two engine registries to ONE spelling
// vocabulary. The engine names are shared vocabulary — a user types the same
// --engine at ltk and at taskloom — and each registry asserting its own list
// independently is how a spelling used to resolve under one and error under
// the other.
func TestEngineNameVocabularyParity(t *testing.T) {
	for _, tc := range engineNameCorpus {
		assert.Equal(t, tc.want, canonicalOrEmpty(tc.in), "agent.CanonicalEngineName(%q)", tc.in)

		mine, myErr := Get(tc.in)
		theirs, theirErr := ltkengine.Get(tc.in)

		if tc.want == "" {
			assert.Error(t, myErr, "taskloom Get(%q) must reject", tc.in)
			assert.Error(t, theirErr, "ltk Get(%q) must reject", tc.in)
			continue
		}
		require.NoError(t, myErr, "taskloom Get(%q)", tc.in)
		require.NoError(t, theirErr, "ltk Get(%q)", tc.in)
		assert.Equal(t, tc.want, mine.Name())
		assert.Equal(t, tc.want, theirs.Name())
	}
}

// canonicalOrEmpty reports the canonical engine name for in, or "" when in
// names no engine either registry holds.
func canonicalOrEmpty(in string) string {
	got := agent.CanonicalEngineName(in)
	for _, e := range All() {
		if e.Name() == got {
			return got
		}
	}
	return ""
}
