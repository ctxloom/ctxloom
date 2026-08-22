package plans

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestParseFrontmatter_Characterization pins ParseFrontmatter's answer — BOTH
// return values, exactly — for every frontmatter shape the reader is known to
// meet, so that a change to how the block is parsed has to declare itself in
// this file's diff rather than only in the field.
//
// It exists because the reader is being moved off a hand-rolled line scanner
// onto the YAML parser the paired WRITER already uses, and ~120 lines of
// prefix-matching are going away with it. Deleting a parser silently changes
// every edge it happened to handle; the point of this table is that the edges
// which must NOT move are asserted here first, and the ones that do move show
// up as an edit to an expectation with a reason attached.
//
// Every case asserts the title AND the sessions slice, including when the
// expectation is "nothing": a shape that yields nothing today and nothing
// tomorrow is a real claim, and asserting only the half that has a value is
// how the parity test in internal/memory managed to pass while checking almost
// nothing.
func TestParseFrontmatter_Characterization(t *testing.T) {
	cases := []struct {
		name         string
		content      string
		wantTitle    string
		wantSessions []string
	}{
		// --- delimiters -------------------------------------------------
		{
			name:    "no frontmatter: a body line shaped like a key is not metadata",
			content: "# heading\ntitle: not meta\n",
		},
		{
			name:    "an unterminated block is not frontmatter at all",
			content: "---\ntitle: nope\nsessions:\n  - ghost\n\nbody\n",
		},
		{
			name:    "a body line shaped like a key, after a closed block, is ignored",
			content: "---\nsessions: [a]\n---\ntitle: body line\n",
			// The block closed before the body began; `title:` below it is prose.
			wantSessions: []string{"a"},
		},
		{
			name:    "an immediately closed block carries nothing",
			content: "---\n---\nbody\n",
		},
		{
			name:    "an immediately closed block with no body carries nothing",
			content: "---\n---\n",
		},
		{
			name:    "a block holding only a blank line carries nothing",
			content: "---\n\n---\nbody\n",
		},
		{
			name:      "trailing and leading spaces on the fences still delimit",
			content:   "---  \ntitle: A\n  ---  \nbody\n",
			wantTitle: "A",
		},
		{
			name:    "a leading byte-order mark means the first line is not a fence",
			content: "\ufeff---\ntitle: A\n---\n",
		},
		{
			name:         "CRLF line endings parse the same as LF",
			content:      "---\r\ntitle: A\r\nsessions:\r\n  - a\r\n---\r\nbody\r\n",
			wantTitle:    "A",
			wantSessions: []string{"a"},
		},

		// --- sessions ---------------------------------------------------
		{
			name:         "block sequence",
			content:      "---\nsessions:\n  - a\n  - b\n---\n",
			wantSessions: []string{"a", "b"},
		},
		{
			name:         "block sequence with quoted items yields the values, not their spelling",
			content:      "---\nsessions:\n  - \"a\"\n  - 'b'\n---\n",
			wantSessions: []string{"a", "b"},
		},
		{
			name:         "flow sequence",
			content:      "---\nsessions: [a, b]\n---\n",
			wantSessions: []string{"a", "b"},
		},
		{
			name:         "a comma inside quotes does not split a flow item",
			content:      "---\nsessions: [\"a, b\", c]\n---\n",
			wantSessions: []string{"a, b", "c"},
		},
		{
			name:    "an empty flow sequence is no sessions, not one empty session",
			content: "---\nsessions: []\n---\n",
		},
		{
			name:      "a scalar `sessions` is not invented into one entry, and the title survives it",
			content:   "---\ntitle: T\nsessions: alpha\n---\n",
			wantTitle: "T",
		},
		{
			name:    "a mapping under `sessions` is not a list of sessions",
			content: "---\nsessions:\n  a: b\n---\n",
		},
		{
			name:         "a key after the sessions block ends it",
			content:      "---\nsessions:\n  - a\ntitle: T\n---\n",
			wantTitle:    "T",
			wantSessions: []string{"a"},
		},
		{
			name:      "no sessions key at all",
			content:   "---\ntitle: T\n---\nbody\n",
			wantTitle: "T",
		},

		// --- title ------------------------------------------------------
		{
			name:      "a double-quoted title containing a colon",
			content:   "---\ntitle: \"Quoted: Title\"\n---\n",
			wantTitle: "Quoted: Title",
		},
		{
			name:      "keys other than title and sessions are ignored, not confused for them",
			content:   "---\nstatus: draft\ntitle: Solo\nfoo: 1\n---\n",
			wantTitle: "Solo",
		},
		{
			name:      "a tab after the key separator is not part of the value",
			content:   "---\ntitle:\tA\n---\n",
			wantTitle: "A",
		},
		{
			name:    "a key with no value is an empty title, not a literal empty string to report",
			content: "---\ntitle:\n---\n",
		},
		{
			name:    "a frontmatter block that is a sequence, not a mapping, carries nothing",
			content: "---\n- a\n---\n",
		},

		// --- the three divergences this rewrite is about -----------------
		// The paired writer round-trips all three of these verbatim through
		// yaml.Node, so the file on disk means exactly one thing and the
		// reader now says that thing. The line-scanning reader reported the
		// source text instead: "hardening # rev2", "it''s done", and "|".
		{
			name:      "a trailing comment is not part of the title",
			content:   "---\ntitle: hardening # rev2\n---\n",
			wantTitle: "hardening",
		},
		{
			name:      "a doubled quote inside a single-quoted title is one quote",
			content:   "---\ntitle: 'it''s done'\n---\n",
			wantTitle: "it's done",
		},
		{
			name:      "a literal block scalar title is its content, not its indicator",
			content:   "---\ntitle: |\n  wrapped\n---\n",
			wantTitle: "wrapped\n",
		},

		// --- shapes the line scanner used to guess at --------------------
		// None of these were named in the change that prompted this file;
		// they moved because the guesses went with the scanner. Each is now
		// whatever YAML says, which is what the writer already believed.
		{
			name:    "a duplicate key makes the block invalid YAML, so it carries nothing",
			content: "---\ntitle: A\ntitle: B\n---\n",
			// The scanner took the last one and reported "B". YAML rejects the
			// mapping outright, and a document whose keys contradict each other
			// has no answer worth guessing at.
		},
		{
			name:      "an indented top-level key is still a key",
			content:   "---\n  title: A\n---\n",
			wantTitle: "A",
			// The scanner matched on a bare "title:" prefix and saw nothing here.
		},
		{
			name:    "a sequence title is not a title",
			content: "---\ntitle: [a, b]\n---\n",
			// The scanner reported the literal text "[a, b]" as the title.
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, sessions := ParseFrontmatter(tc.content)
			assert.Equal(t, tc.wantTitle, title, "title from:\n%s", tc.content)
			assert.Equal(t, tc.wantSessions, sessions, "sessions from:\n%s", tc.content)
		})
	}
}
