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

			_, got := plans.ParseFrontmatter(string(data))
			assert.Equal(t, tc.wantSessions, got,
				"the reader must recover exactly what the writer recorded\n--- file ---\n%s", data)
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
