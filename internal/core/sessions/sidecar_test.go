package sessions

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// The session store is the set of session DIRECTORIES under the sessions
// root, each carrying a sidecar with the facts the directory itself cannot
// recover. There is no global index: nothing here writes one, and these tests
// pin that nothing reads one either.
//
// The YAML below is hand-written on purpose: it is the on-disk contract. A
// test that marshalled an Entry would pass whatever the struct tags happen
// to say.

const testSidecarName = "session.yaml"

// openSidecarRoot opens a Manager on an isolated sessions root that is ALSO
// the HOME-resolved root, so the harp-dir paths every fill helper builds
// through paths.HarpDir agree with the root the Manager enumerates.
func openSidecarRoot(t *testing.T) (*Manager, string) {
	t.Helper()
	requireIsolatedSessionRoot(t)
	root, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(root, 0o755))
	m, err := Open(nil)
	require.NoError(t, err)
	return m, root
}

func writeSidecar(t *testing.T, root, harp, yamlBody string) {
	t.Helper()
	dir := filepath.Join(root, harp)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, testSidecarName), []byte(yamlBody), 0o644))
}

func harpNames(entries []Entry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.HarpName)
	}
	return out
}

func TestListAll_SessionPresentAsDirectoryListsWithNoIndexYAML(t *testing.T) {
	m, root := openSidecarRoot(t)
	writeSidecar(t, root, "swift-amber-falcon", `project_dir: /proj/a
backend: claude-code
session_id: sess-1
started_at: 2026-09-01T10:00:00Z
source_entries: 12
engine_version: "2.1.0"
`)
	_, statErr := os.Stat(filepath.Join(root, paths.IndexFileName))
	require.True(t, os.IsNotExist(statErr), "precondition: no index.yaml on disk")

	got, err := m.ListAll()
	require.NoError(t, err)
	require.Equal(t, []string{"swift-amber-falcon"}, harpNames(got))
	e := got[0]
	assert.Equal(t, "/proj/a", e.ProjectDir)
	assert.Equal(t, "claude-code", e.Backend)
	assert.Equal(t, "sess-1", e.SessionID)
	assert.Equal(t, 12, e.SourceEntries)
	assert.Equal(t, "2.1.0", e.EngineVersion)
	assert.Equal(t, time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC), e.StartedAt)
	// The harp name is the directory's, never a field the sidecar has to
	// repeat and keep in step with a rename.
	assert.Equal(t, "swift-amber-falcon", e.HarpName)
}

func TestFindBySessionID_RotatedAwayIDResolvesFromSidecar(t *testing.T) {
	m, root := openSidecarRoot(t)
	writeSidecar(t, root, "cleared-harp", `project_dir: /proj/a
backend: claude-code
session_id: sess-after-clear
started_at: 2026-09-01T10:00:00Z
rotations:
  - session_id: sess-before-clear
    transcript_path: /vendor/old.jsonl
    rotated_at: 2026-09-01T11:00:00Z
`)
	got, err := m.FindBySessionID("sess-before-clear")
	require.NoError(t, err)
	require.NotNil(t, got, "a session id a /clear rotated away is recorded only in the sidecar's rotations and must still resolve")
	assert.Equal(t, "cleared-harp", got.HarpName)
	assert.Equal(t, "sess-after-clear", got.SessionID)
	require.Len(t, got.Rotations, 1)
	assert.Equal(t, "/vendor/old.jsonl", got.Rotations[0].TranscriptPath)

	cur, err := m.FindBySessionID("sess-after-clear")
	require.NoError(t, err)
	require.NotNil(t, cur)
	assert.Equal(t, "cleared-harp", cur.HarpName)
}

func TestListForProject_FiltersByProjectDirFromSidecar(t *testing.T) {
	m, root := openSidecarRoot(t)
	writeSidecar(t, root, "in-a", "project_dir: /proj/a\nbackend: claude-code\nstarted_at: 2026-09-01T10:00:00Z\n")
	writeSidecar(t, root, "in-b", "project_dir: /proj/b\nbackend: claude-code\nstarted_at: 2026-09-01T10:00:00Z\n")
	writeSidecar(t, root, "also-a", "project_dir: /proj/a\nbackend: codex\nstarted_at: 2026-09-02T10:00:00Z\n")

	got, err := m.ListForProject("/proj/a")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"in-a", "also-a"}, harpNames(got))

	all, err := m.ListAll()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"in-a", "in-b", "also-a"}, harpNames(all))
}

func TestListAll_PurgedDirIsListedAsPurgedNotDropped(t *testing.T) {
	m, root := openSidecarRoot(t)
	// Exactly what `session purge` leaves: the directory with its content
	// files removed one by one, the sidecar still in place, a stamp saying
	// the removal was deliberate. No transcript, no essence, nothing
	// authored under persist/.
	writeSidecar(t, root, "purged-on-purpose", `project_dir: /proj/a
backend: claude-code
session_id: sess-p
transcript_path: /vendor/gone.jsonl
started_at: 2026-09-01T10:00:00Z
ended_at: 2026-09-01T12:00:00Z
purged_at: 2026-09-02T09:00:00Z
`)
	got, err := m.ListAll()
	require.NoError(t, err)
	require.Equal(t, []string{"purged-on-purpose"}, harpNames(got),
		"a purged session is a real directory missing its content; it lists as purged, it is never mistaken for damage and dropped")
	require.NotNil(t, got[0].PurgedAt)
	assert.Equal(t, time.Date(2026, 9, 2, 9, 0, 0, 0, time.UTC), got[0].PurgedAt.UTC())

	one, err := m.Find("purged-on-purpose")
	require.NoError(t, err)
	require.NotNil(t, one)
	assert.NotNil(t, one.PurgedAt)
}

// TestListAll_StaleIndexYAMLLeftBehindIsIgnored pins the property the
// decided architecture states for the sessions tree: the sidecar
// (paths.SessionSidecarFileName under each harp dir) is the record, and a
// pre-rename index.yaml at the sessions root is NOT a source of sessions —
// whether or not an older binary's migration left its marker
// (paths.MigratedIndexFileName) beside it. The index carries a harp the tree
// does not have; that harp must never surface, and the file must never be
// consumed.
func TestListAll_StaleIndexYAMLLeftBehindIsIgnored(t *testing.T) {
	for _, tc := range []struct {
		name       string
		withMarker bool
	}{
		{name: "beside the migration marker", withMarker: true},
		{name: "without any marker (a pure pre-rename index)", withMarker: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, root := openSidecarRoot(t)
			writeSidecar(t, root, "real-one", "project_dir: /proj/a\nbackend: claude-code\nstarted_at: 2026-09-01T10:00:00Z\n")
			if tc.withMarker {
				require.NoError(t, os.WriteFile(filepath.Join(root, paths.MigratedIndexFileName), []byte("sessions: []\n"), 0o644))
			}
			stale := filepath.Join(root, paths.IndexFileName)
			require.NoError(t, os.WriteFile(stale, []byte(`sessions:
  - harp_name: ghost-row
    project_dir: /proj/a
    backend: claude-code
    started_at: 2026-08-01T10:00:00Z
`), 0o644))

			// Re-Open against the stale file: nothing may read it.
			m2, err := Open(nil)
			require.NoError(t, err)
			got, err := m2.ListAll()
			require.NoError(t, err)
			assert.Equal(t, []string{"real-one"}, harpNames(got), "a stale index.yaml must not contribute rows")

			found, err := m2.Find("ghost-row")
			require.NoError(t, err)
			assert.Nil(t, found)
			_, statErr := os.Stat(filepath.Join(root, "ghost-row"))
			assert.True(t, os.IsNotExist(statErr), "ignored means ignored: no directory is minted for a stale row")
			_, statErr = os.Stat(stale)
			assert.NoError(t, statErr, "the stale file is left alone, not consumed")
		})
	}
}

func TestListAll_SummaryAndDetailDerivedFromEssence(t *testing.T) {
	m, root := openSidecarRoot(t)
	writeSidecar(t, root, "distilled", "project_dir: /proj/a\nbackend: claude-code\nstarted_at: 2026-09-01T10:00:00Z\nsource_entries: 3\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, "distilled", paths.EssenceFileName), []byte(`---
session_id: sess-1
distilled_at: 2026-09-01T12:00:00Z
entry_count: 3
plan_blocks: 0
summary: Rewired the session index into per-harp sidecars
---
## Context

Some prose.

### Open Items

- migrate the readers
- delete Reconcile
`), 0o644))

	got, err := m.ListAll()
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "Rewired the session index into per-harp sidecars", got[0].Summary, "summary comes from essence.md, not from any stored copy")
	assert.Equal(t, []string{"- migrate the readers", "- delete Reconcile"}, got[0].Detail)

	one, err := m.Find("distilled")
	require.NoError(t, err)
	require.NotNil(t, one)
	assert.Equal(t, "Rewired the session index into per-harp sidecars", one.Summary)
}

func TestFind_OneHarpLookupIsUnaffectedByASiblingsCorruptSidecar(t *testing.T) {
	m, root := openSidecarRoot(t)
	writeSidecar(t, root, "good", "project_dir: /proj/a\nbackend: claude-code\nstarted_at: 2026-09-01T10:00:00Z\n")
	writeSidecar(t, root, "bad", "project_dir: [unterminated\n")

	one, err := m.Find("good")
	require.NoError(t, err, "a one-harp lookup reads one sidecar; a sibling's corruption is not its concern")
	require.NotNil(t, one)
	assert.Equal(t, "/proj/a", one.ProjectDir)

	// The listing degrades to the readable set rather than failing whole.
	all, err := m.ListAll()
	require.NoError(t, err)
	assert.Equal(t, []string{"good"}, harpNames(all))
}

func TestAssignHarp_WritesTheSidecarAndNoIndex(t *testing.T) {
	m, root := openSidecarRoot(t)
	e, err := m.AssignHarp("/proj/a", "claude-code")
	require.NoError(t, err)

	_, statErr := os.Stat(filepath.Join(root, e.HarpName, testSidecarName))
	assert.NoError(t, statErr, "assignment mints the session directory with its sidecar")
	_, statErr = os.Stat(filepath.Join(root, paths.IndexFileName))
	assert.True(t, os.IsNotExist(statErr), "nothing writes index.yaml any more")

	found, err := m.Find(e.HarpName)
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "/proj/a", found.ProjectDir)
}

func TestRename_MovesTheSessionDirectory(t *testing.T) {
	m, root := openSidecarRoot(t)
	writeSidecar(t, root, "old-name", "project_dir: /proj/a\nbackend: claude-code\nstarted_at: 2026-09-01T10:00:00Z\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, "old-name", paths.EssenceFileName), []byte("---\nsummary: kept\n---\n"), 0o644))

	require.NoError(t, m.Rename("old-name", "new-name"))
	_, statErr := os.Stat(filepath.Join(root, "old-name"))
	assert.True(t, os.IsNotExist(statErr))
	_, statErr = os.Stat(filepath.Join(root, "new-name", paths.EssenceFileName))
	assert.NoError(t, statErr, "the directory IS the record: renaming the session renames the directory, essence and all")

	writeSidecar(t, root, "taken", "project_dir: /proj/a\nbackend: claude-code\nstarted_at: 2026-09-01T10:00:00Z\n")
	assert.Error(t, m.Rename("new-name", "taken"), "a rename onto an existing session is refused")
}

func TestForget_RemovesTheSidecarAndLeavesTheDirectory(t *testing.T) {
	m, root := openSidecarRoot(t)
	writeSidecar(t, root, "forgotten", "project_dir: /proj/a\nbackend: claude-code\nstarted_at: 2026-09-01T10:00:00Z\n")
	authored := filepath.Join(root, "forgotten", paths.PersistDirName, "notes.plan.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(authored), 0o755))
	require.NoError(t, os.WriteFile(authored, []byte("# plan\n"), 0o644))

	require.NoError(t, m.Forget("forgotten"))
	all, err := m.ListAll()
	require.NoError(t, err)
	assert.Empty(t, harpNames(all), "without its sidecar the directory is no longer a session")
	_, statErr := os.Stat(authored)
	assert.NoError(t, statErr, "forgetting drops the record, never the authored files beside it")
}

// TestIsSessionDir pins the one predicate every walker over the sessions
// root answers "is this a session" through.
func TestIsSessionDir(t *testing.T) {
	requireIsolatedSessionRoot(t)
	root, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(root, 0o755))

	writeSidecar(t, root, "with-sidecar", "project_dir: /proj/a\nbackend: claude-code\nstarted_at: 2026-09-01T10:00:00Z\n")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "bare-dir", paths.PersistDirName), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, paths.IndexFileName), []byte("sessions: []\n"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(root, "with-sidecar"), filepath.Join(root, "link-to-session")))

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Name()] = IsSessionDir(root, e)
	}
	assert.Equal(t, map[string]bool{
		"with-sidecar":      true,
		"bare-dir":          false, // a directory without the sidecar is not a session: it is what `session remove` leaves
		paths.IndexFileName: false, // a file at the root
		"link-to-session":   false, // never follow a symlink into (or out of) the root
	}, got)
}
