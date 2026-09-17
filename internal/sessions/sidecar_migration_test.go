package sessions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// migrationShape is one real shape a session directory (or an index row) can
// be in when the one-time index.yaml migration meets it, with the verdict the
// migration and the enumeration must reach. This table is the gate the row
// was withheld for lack of: a migration that gets one of these wrong fails
// here, not on a live store.
type migrationShape struct {
	name string
	// harp is the directory name under the root (and the index row's harp_name).
	harp string
	// indexRow is the row index.yaml carries for this harp; empty means the
	// index never knew it.
	indexRow string
	// build lays down the directory shape before the migration runs.
	build func(t *testing.T, dir string)

	wantMigrated bool // the migration wrote this harp's sidecar
	wantListed   bool // the enumeration lists it afterwards
	wantPurged   bool // and lists it as purged
}

func migrationShapes() []migrationShape {
	return []migrationShape{
		{
			name: "healthy",
			harp: "healthy",
			indexRow: `  - harp_name: healthy
    session_id: sess-h
    backend: claude-code
    project_dir: /proj/a
    started_at: 2026-09-01T10:00:00Z
    source_entries: 2
`,
			build: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, os.MkdirAll(filepath.Join(dir, paths.PersistDirName), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, paths.PersistDirName, paths.CanonicalTranscriptFileName),
					[]byte(`{"kind":"entry"}`+"\n"+`{"kind":"entry"}`+"\n"), 0o644))
				require.NoError(t, os.WriteFile(filepath.Join(dir, paths.EssenceFileName),
					[]byte("---\nsummary: healthy session\nentry_count: 2\n---\nbody\n"), 0o644))
			},
			wantMigrated: true, wantListed: true,
		},
		{
			name: "purged: directory present, content gone",
			harp: "purged",
			indexRow: `  - harp_name: purged
    session_id: sess-p
    backend: claude-code
    project_dir: /proj/a
    started_at: 2026-09-01T10:00:00Z
    transcript_path: /vendor/never-there.jsonl
    purged_at: 2026-09-02T10:00:00Z
`,
			build: func(t *testing.T, dir string) {
				t.Helper()
				// PurgeSession removes files one by one and never the
				// directory: what survives is the empty shell.
				require.NoError(t, os.MkdirAll(dir, 0o755))
			},
			wantMigrated: true, wantListed: true, wantPurged: true,
		},
		{
			name: "pruned vendor transcript, authored persist content",
			harp: "pruned",
			indexRow: `  - harp_name: pruned
    session_id: sess-pr
    backend: claude-code
    project_dir: /proj/a
    started_at: 2026-09-01T10:00:00Z
    transcript_path: /vendor/pruned-by-vendor.jsonl
`,
			build: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, os.MkdirAll(filepath.Join(dir, paths.PersistDirName), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, paths.PersistDirName, "design.plan.md"), []byte("# plan\n"), 0o644))
			},
			// Nothing reconciles it away: the directory is the record and
			// the plan under persist/ is irreplaceable.
			wantMigrated: true, wantListed: true,
		},
		{
			name:     "orphaned capture: directory with a transcript the index never knew",
			harp:     "orphan",
			indexRow: "",
			build: func(t *testing.T, dir string) {
				t.Helper()
				require.NoError(t, os.MkdirAll(filepath.Join(dir, paths.PersistDirName), 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, paths.PersistDirName, paths.CanonicalTranscriptFileName),
					[]byte(`{"kind":"entry"}`+"\n"), 0o644))
			},
			// The migration's scope is the index's rows; a directory nobody
			// recorded is not invented into a session. Listing it is the
			// adopt path's job, not the migration's.
			wantMigrated: false, wantListed: false,
		},
		{
			name: "index row with no directory",
			harp: "rowonly",
			indexRow: `  - harp_name: rowonly
    backend: codex
    project_dir: /proj/b
    started_at: 2026-09-03T10:00:00Z
`,
			build: func(t *testing.T, dir string) {},
			// Never lose a row: the directory is minted so the row has a
			// place to live.
			wantMigrated: true, wantListed: true,
		},
	}
}

func layDownShapes(t *testing.T) (root string) {
	t.Helper()
	requireIsolatedSessionRoot(t)
	root, err := paths.HomeSessionsDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(root, 0o755))
	index := "sessions:\n"
	for _, s := range migrationShapes() {
		s.build(t, filepath.Join(root, s.harp))
		index += s.indexRow
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, paths.IndexFileName), []byte(index), 0o644))
	return root
}

func TestMigrateIndex_RealShapes(t *testing.T) {
	root := layDownShapes(t)

	report, err := MigrateIndex(root)
	require.NoError(t, err)

	m, err := Open()
	require.NoError(t, err)
	listed, err := m.ListAll()
	require.NoError(t, err)
	byName := map[string]Entry{}
	for _, e := range listed {
		byName[e.HarpName] = e
	}

	for _, s := range migrationShapes() {
		t.Run(s.name, func(t *testing.T) {
			harp := s.harp
			if s.wantMigrated {
				assert.Contains(t, report.Migrated, harp)
				_, statErr := os.Stat(filepath.Join(root, harp, paths.SessionSidecarFileName))
				assert.NoError(t, statErr, "the migration must leave a sidecar")
			} else {
				assert.NotContains(t, report.Migrated, harp)
				_, statErr := os.Stat(filepath.Join(root, harp, paths.SessionSidecarFileName))
				assert.True(t, os.IsNotExist(statErr), "the migration invents no sidecar for a directory the index never recorded")
			}
			e, listed := byName[harp]
			assert.Equal(t, s.wantListed, listed)
			if s.wantPurged {
				assert.NotNil(t, e.PurgedAt, "a purged shape must list AS purged — that is what keeps a deliberate purge distinguishable from damage")
			}
		})
	}

	t.Run("healthy keeps its summary via essence.md, not a stored copy", func(t *testing.T) {
		assert.Equal(t, "healthy session", byName["healthy"].Summary)
	})

	t.Run("re-entry is a no-op: nothing migrated twice, no row duplicated", func(t *testing.T) {
		again, err := MigrateIndex(root)
		require.NoError(t, err)
		assert.Empty(t, again.Migrated)
		relisted, err := m.ListAll()
		require.NoError(t, err)
		assert.Len(t, relisted, len(listed))
	})

	t.Run("the consumed index is renamed, never re-read", func(t *testing.T) {
		_, statErr := os.Stat(filepath.Join(root, paths.IndexFileName))
		assert.True(t, os.IsNotExist(statErr))
		_, statErr = os.Stat(filepath.Join(root, paths.MigratedIndexFileName))
		assert.NoError(t, statErr, "the consumed file is kept under its migrated name as a manual fallback")
	})
}

func TestMigrateIndex_InterruptedRunResumesWithoutOverwriting(t *testing.T) {
	root := layDownShapes(t)
	// A previous, interrupted run wrote healthy's sidecar and then died before
	// renaming the index. What it wrote must survive the resumed run verbatim.
	require.NoError(t, os.WriteFile(filepath.Join(root, "healthy", paths.SessionSidecarFileName),
		[]byte("project_dir: /proj/written-by-first-run\nbackend: claude-code\nstarted_at: 2026-09-01T10:00:00Z\n"), 0o644))

	report, err := MigrateIndex(root)
	require.NoError(t, err)
	assert.NotContains(t, report.Migrated, "healthy")
	assert.Contains(t, report.AlreadyPresent, "healthy")

	m, err := Open()
	require.NoError(t, err)
	e, err := m.Find("healthy")
	require.NoError(t, err)
	require.NotNil(t, e)
	assert.Equal(t, "/proj/written-by-first-run", e.ProjectDir)
}

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
