package memory

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/plans"
)

// TestStampPlanFile_IsReadableByPlansParser is the reader/writer parity test
// this pair never had. StampPlanFile is the ONLY writer of the `sessions:` key
// and plans.ParseFrontmatter is the only reader of it, in different packages
// with no shared code, so nothing has ever checked that what one writes the
// other can read.
//
// It writes through the real writer and reads back through the real reader, so
// every case here is a claim about production behaviour and not about either
// side's idea of the format.
func TestStampPlanFile_IsReadableByPlansParser(t *testing.T) {
	cases := []struct {
		name    string
		initial string
		// wantSessions is what the reader must report after stamping "wave81".
		wantSessions []string
		// wantTitle is what the reader must report for `title` after stamping.
		// It is asserted for EVERY case, empty included: `sessions` alone was
		// what this test used to check, and a plan's title is the other half of
		// what the reader exists to produce.
		wantTitle string
		// writerKeeps is a substring the stamped file must still contain: the
		// title's SOURCE SPELLING, proving the yaml.Node writer round-tripped
		// it verbatim and that any mismatch above is therefore the reader's
		// disagreement with YAML and not the writer having rewritten the value.
		writerKeeps string
	}{
		{
			name:         "no frontmatter at all",
			initial:      "# a plan\n",
			wantSessions: []string{"wave81"},
		},
		{
			name:         "block sequence, the shape the writer synthesizes",
			initial:      "---\nsessions:\n  - earlier\n---\n\n# a plan\n",
			wantSessions: []string{"earlier", "wave81"},
		},
		{
			name:         "FLOW sequence, which yaml.Node round-trips verbatim",
			initial:      "---\nsessions: [earlier]\n---\n\n# a plan\n",
			wantSessions: []string{"earlier", "wave81"},
		},
		{
			name:         "quoted block items",
			initial:      "---\nsessions:\n  - \"earlier\"\n---\n\n# a plan\n",
			wantSessions: []string{"earlier", "wave81"},
		},
		{
			name:         "empty flow sequence",
			initial:      "---\nsessions: []\n---\n\n# a plan\n",
			wantSessions: []string{"wave81"},
		},
		{
			name:         "sessions alongside a title",
			initial:      "---\ntitle: \"My Plan\"\nsessions: [earlier]\n---\n\nbody\n",
			wantSessions: []string{"earlier", "wave81"},
			wantTitle:    "My Plan",
			writerKeeps:  "title: \"My Plan\"",
		},

		// The three shapes below are ordinary YAML that the writer preserves
		// character for character, so the file on disk means exactly one thing.
		// The reader has to agree with that meaning, not with the source text.
		{
			name:         "a trailing comment is not part of the title",
			initial:      "---\ntitle: hardening # rev2\nsessions: [earlier]\n---\n\nbody\n",
			wantSessions: []string{"earlier", "wave81"},
			wantTitle:    "hardening",
			writerKeeps:  "title: hardening # rev2",
		},
		{
			name:         "a doubled quote inside a single-quoted title is one quote",
			initial:      "---\ntitle: 'it''s done'\nsessions: [earlier]\n---\n\nbody\n",
			wantSessions: []string{"earlier", "wave81"},
			wantTitle:    "it's done",
			writerKeeps:  "title: 'it''s done'",
		},
		{
			name:         "a literal block scalar title is its content, not its indicator",
			initial:      "---\ntitle: |\n  wrapped\nsessions: [earlier]\n---\n\nbody\n",
			wantSessions: []string{"earlier", "wave81"},
			wantTitle:    "wrapped\n",
			writerKeeps:  "title: |\n  wrapped",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "thing.plan.md")
			require.NoError(t, os.WriteFile(path, []byte(tc.initial), 0o644))

			require.NoError(t, StampPlanFile(path, "wave81"))

			data, err := os.ReadFile(path)
			require.NoError(t, err)

			// Fixture check: the writer really did record the harp. Without
			// this, a reader that returns nothing would be indistinguishable
			// from a writer that never wrote anything, and the assertion below
			// would pass for the wrong reason.
			require.Contains(t, string(data), "wave81",
				"the writer did not record the harp at all; the reader assertion would be vacuous")

			// Fixture check: the title survived the writer's yaml.Node
			// round-trip with its spelling intact. Without this, a title
			// mismatch below could equally mean the writer had reformatted the
			// value, and the test would be accusing the wrong half of the pair.
			if tc.writerKeeps != "" {
				require.Contains(t, string(data), tc.writerKeeps,
					"the writer did not round-trip the title verbatim, so a reader mismatch would be unattributable\n--- file ---\n%s", data)
			}

			gotTitle, got := plans.ParseFrontmatter(string(data))
			assert.Equal(t, tc.wantSessions, got,
				"the reader must recover exactly what the writer recorded\n--- file ---\n%s", data)
			assert.Equal(t, tc.wantTitle, gotTitle,
				"the reader must recover the title the file MEANS, not the source text the writer preserved\n--- file ---\n%s", data)
		})
	}
}

// TestStampPlanFile_UnterminatedFrontmatterAgreesWithReader pins the second
// disagreement in this pair: the writer REFUSES a document whose opening `---`
// is never closed ("refusing to guess where it ends"), so the reader must not
// treat that document as having frontmatter either. Anything else means a body
// line that merely looks like `title:` is read as metadata of a file the writer
// considers unparseable.
func TestStampPlanFile_UnterminatedFrontmatterAgreesWithReader(t *testing.T) {
	const unterminated = "---\ntitle: not really frontmatter\nsessions:\n  - ghost\n\nbody text\n"

	dir := t.TempDir()
	path := filepath.Join(dir, "thing.plan.md")
	require.NoError(t, os.WriteFile(path, []byte(unterminated), 0o644))

	// Fixture check: the writer must genuinely reject this document, or the
	// reader has nothing to agree with.
	err := StampPlanFile(path, "wave81")
	require.Error(t, err, "the writer is expected to refuse an unterminated block")
	require.ErrorContains(t, err, "no closing")

	title, sessions := plans.ParseFrontmatter(unterminated)
	assert.Empty(t, title, "an unterminated block is not frontmatter to the writer; it must not be to the reader")
	assert.Empty(t, sessions, "an unterminated block is not frontmatter to the writer; it must not be to the reader")
}
