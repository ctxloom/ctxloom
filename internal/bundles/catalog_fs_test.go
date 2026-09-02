package bundles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReadersFS_PrefersTheProjectTreeRegardlessOfReaderOrder pins WHICH
// filesystem a composed loader reports, and pins it to a property of the
// readers rather than to their position.
//
// Why this matters more than it looks: a skill's trust preimage is derived
// from its on-disk tree, so the filesystem reported here decides the hash. Get
// it wrong and the hash is computed against a tree the skill does not live in,
// the preimage does not match, and the skill is WITHHELD IN SILENCE — no
// error, no warning, just a skill that stopped being delivered.
//
// Both readers here are localFSReaders and both therefore expose FS(): the
// builtin one over the fs EMBEDDED in the binary, the project one over the
// real tree. "First reader that has a filesystem" is thus decided entirely by
// composition order, and the wrong order has already shipped this bug once —
// internal/config's reader composition carries a comment explaining that the
// builtin reader goes AFTER the project reader for exactly this reason.
//
// That comment is the only thing that was holding the invariant, and a comment
// is not a mechanism: any future edit that prepends a reader silently
// reintroduces it. The "builtin first" case below is that edit, written down.
// It must resolve to the project tree anyway.
func TestReadersFS_PrefersTheProjectTreeRegardlessOfReaderOrder(t *testing.T) {
	const marker = "/skills/marker.txt"

	newProjectFS := func(t *testing.T) afero.Fs {
		t.Helper()
		fs := afero.NewMemMapFs()
		require.NoError(t, afero.WriteFile(fs, marker, []byte("PROJECT-TREE"), 0o644))
		return fs
	}

	for _, tc := range []struct {
		name  string
		order func(project, builtin Reader) []Reader
	}{
		{"project first", func(p, b Reader) []Reader { return []Reader{p, b} }},
		{"builtin first", func(p, b Reader) []Reader { return []Reader{b, p} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectFS := newProjectFS(t)
			project := NewProjectReader(projectFS, []string{"/"})
			builtin := NewBuiltinReader()

			got := readersFS(tc.order(project, builtin))

			// The EFFECT, not the identity: the reported filesystem must be one
			// the project's own content is actually readable through. Comparing
			// pointers would pass for any fs that merely happened to be the
			// project's; reading proves the preimage would resolve.
			data, err := afero.ReadFile(got, marker)
			require.NoError(t, err,
				"the reported filesystem cannot see the project tree, so every project "+
					"skill's trust preimage would hash against a tree that is not there "+
					"and the skill would be withheld in silence")
			assert.Equal(t, "PROJECT-TREE", string(data))
		})
	}
}

// TestReadersFS_FallsBackToTheBuiltinFilesystemWhenItIsTheOnlyOne is the other
// half, and it is what stops the rule above from being implemented as "ignore
// the builtin reader". A builtin-only composition has exactly one filesystem
// and must report it: the embedded tree is the right answer there, not a
// fallback to the OS filesystem, which would resolve builtin content against
// paths that do not exist on disk.
func TestReadersFS_FallsBackToTheBuiltinFilesystemWhenItIsTheOnlyOne(t *testing.T) {
	builtin := NewBuiltinReader()
	want := builtin.(interface{ FS() afero.Fs }).FS()

	got := readersFS([]Reader{builtin})

	// Compared against the builtin reader's OWN filesystem, not merely probed
	// for readability. An earlier version of this test asserted that the
	// reported fs could list "." and hold entries, and that SURVIVED deleting
	// the fallback entirely: the OS filesystem lists "." and holds entries too,
	// so the assertion was true of every possible answer and proved nothing.
	// The discriminator has to be which filesystem it IS.
	assert.Equal(t, want, got,
		"a builtin-only composition must report the EMBEDDED filesystem; falling back "+
			"to the OS filesystem resolves builtin content against paths that do not exist on disk")

	// And it must be the usable embedded tree, not a zero value that merely
	// compares equal to another zero value.
	entries, err := afero.ReadDir(got, ".")
	require.NoError(t, err)
	assert.NotEmpty(t, entries, "the embedded builtin tree is not empty")

	// The two answers are genuinely distinguishable, which is what makes the
	// comparison above meaningful rather than a tautology over identical types.
	assert.NotEqual(t, afero.NewOsFs(), got,
		"the embedded filesystem and the OS filesystem must not compare equal, or "+
			"the assertion above could not tell the fallback from its absence")
}
