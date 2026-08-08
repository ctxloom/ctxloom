package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func newIndexManager(t *testing.T) (*Manager, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "index.yaml")
	m, err := Open(path)
	require.NoError(t, err)
	return m, path
}

// The version has to SURVIVE THE FILE, not just the process: it is read back
// on a later ctxloom invocation (a `session backfill`, a resume) to choose the
// reader, and an in-memory-only stamp would leave every stored session
// unreadable while looking correct in the run that recorded it — this
// project's characteristic silent no-op.
func TestRecordEngineVersion_PersistsToTheIndexFile(t *testing.T) {
	m, path := newIndexManager(t)
	e, err := m.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)

	require.NoError(t, m.RecordEngineVersion(e.HarpName, "2.1.225"))

	reopened, err := Open(path)
	require.NoError(t, err)
	got, err := reopened.Find(e.HarpName)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "2.1.225", got.EngineVersion,
		"the recorded version must come back from disk — a later process is what reads it to choose a reader")
}

// The version is recorded VERBATIM, exactly as the engine printed it. A
// canonicalized value (v2.1.225, or 1.18 padded to 1.18.0) is a string no
// `--version` output ever produced, and a human comparing the index against
// their engine would be chasing a difference ctxloom invented.
func TestRecordEngineVersion_StoresTheEnginesOwnRendering(t *testing.T) {
	m, _ := newIndexManager(t)
	e, err := m.AssignHarp("/proj", "codex")
	require.NoError(t, err)

	require.NoError(t, m.RecordEngineVersion(e.HarpName, "0.144.4"))
	got, err := m.Find(e.HarpName)
	require.NoError(t, err)
	assert.Equal(t, "0.144.4", got.EngineVersion)
}

// An empty version must leave the field UNSET, not write "". A written empty
// string is indistinguishable from a session that predates the field, so the
// read path could no longer tell "the probe failed here" from "this is old" —
// and, worse, a run that inspected the index would see the key present and
// conclude the probe had worked.
func TestRecordEngineVersion_EmptyVersionWritesNothing(t *testing.T) {
	m, _ := newIndexManager(t)
	e, err := m.AssignHarp("/proj", "kiro")
	require.NoError(t, err)
	require.NoError(t, m.RecordEngineVersion(e.HarpName, "2.13.0"))

	require.NoError(t, m.RecordEngineVersion(e.HarpName, ""),
		"an empty version is a no-op, not an error")

	got, err := m.Find(e.HarpName)
	require.NoError(t, err)
	assert.Equal(t, "2.13.0", got.EngineVersion,
		"an empty version must not erase a version that was genuinely recorded")
}

// A harp that isn't in the index is a caller bug, and silently succeeding
// would leave a session with no version and no signal that one was ever
// attempted.
func TestRecordEngineVersion_UnknownHarpErrors(t *testing.T) {
	m, _ := newIndexManager(t)
	assert.Error(t, m.RecordEngineVersion("no-such-harp", "1.0.0"))
}

// The field is ADDITIVE, in the PurgedAt shape: an index written before it
// existed loads with the version simply unset (not an error, not a migration),
// and one written without a version emits no key at all — so an older binary
// reading this file sees exactly the file it wrote.
func TestEngineVersion_IsAdditiveAndOmitted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.yaml")
	legacy := "sessions:\n" +
		"    - harp_name: pre-field-harp\n" +
		"      backend: claude-code\n" +
		"      project_dir: /proj\n" +
		"      started_at: 2026-01-01T00:00:00Z\n"
	require.NoError(t, os.WriteFile(path, []byte(legacy), 0o644))

	m, err := Open(path)
	require.NoError(t, err)
	got, err := m.Find("pre-field-harp")
	require.NoError(t, err, "an index written before the field existed must still load")
	require.NotNil(t, got)
	assert.Empty(t, got.EngineVersion, "a pre-field session carries no version — which the read path treats as unknown")

	// And a session with no version must not gain an engine_version key.
	out, err := yaml.Marshal(Entry{HarpName: "h", ProjectDir: "/p"})
	require.NoError(t, err)
	assert.NotContains(t, string(out), "engine_version",
		"omitempty keeps the key out of files that have nothing to say, so an older binary reads back what it wrote")
}
