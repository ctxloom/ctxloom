package kiro

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpenReadOnly_HonoursContextCancellation pins that the OPEN half of a
// kiro read is cancellable, not just the query that follows it. Opening is
// where the blocking actually happens (the eager ping touches the file, which
// may be on a slow or unresponsive mount), so a caller that has already given
// up must not be made to wait for it.
//
// The db path deliberately does NOT exist: if the ping ran regardless of ctx,
// the error would be SQLite's own "unable to open database file" rather than
// the cancellation the caller asked for, so the two outcomes are
// distinguishable rather than merely both-non-nil.
func TestOpenReadOnly_HonoursContextCancellation(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.sqlite3")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := openReadOnly(ctx, absent)
	require.ErrorIs(t, err, context.Canceled)

	// The sole ctx-carrying entry point that opens its own connection must
	// propagate the same answer (enumerate.go).
	_, err = EnumerateConversations(ctx, absent)
	require.ErrorIs(t, err, context.Canceled)
}

// TestOpenReadOnly_PathWithURIMetacharacters pins the load-bearing half of
// openReadOnly's contract: mode=ro is a GUARANTEE (this reader must never
// mutate kiro-cli's own store), so it has to survive a db path that is a
// perfectly legal POSIX filename but a hostile SQLite URI. '#' opens a
// fragment, '?' opens the query string (which is where mode=ro itself lives),
// and '%' introduces a percent-escape SQLite decodes — so any of the three,
// pasted unescaped into a "file:" URI, silently addresses a DIFFERENT file
// and/or drops mode=ro, leaving a read-write connection onto a phantom db
// SQLite has just created next to the real one.
//
// Each subtest therefore asserts three things about the same connection: it
// reads the table that only the INTENDED file has, a write is refused, and no
// extra file appeared in the directory.
func TestOpenReadOnly_PathWithURIMetacharacters(t *testing.T) {
	for _, name := range []string{
		"plain.sqlite3",
		"frag#ment.sqlite3",
		"query?mode=rw.sqlite3",
		"pct%2Fescape.sqlite3",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, name)

			// Seed under a boring name and RENAME into place: the driver's own
			// DSN parsing splits a plain path on '?' too, so building the
			// fixture through it would create the db somewhere other than
			// dbPath and the subtest would measure the wrong file (a fixture
			// that is not hostile is not a fixture).
			seed := filepath.Join(dir, "seed")
			db := mustOpenAndCreateTable(t, seed)
			require.NoError(t, db.Close())
			require.NoError(t, os.Rename(seed, dbPath))
			require.FileExists(t, dbPath)

			ro, err := openReadOnly(t.Context(), dbPath)
			require.NoError(t, err, "openReadOnly must resolve the real file")
			defer func() { _ = ro.Close() }()

			// Addressing the INTENDED file: only it carries conversations_v2.
			var n int
			require.NoError(t, ro.QueryRow(`SELECT count(*) FROM conversations_v2`).Scan(&n),
				"the connection is not addressing the db at dbPath")

			// mode=ro still in force.
			_, err = ro.Exec(`CREATE TABLE probe (x INTEGER)`)
			require.Error(t, err, "mode=ro was dropped: this reader can WRITE the vendor's store")

			// Nothing new was created alongside it (a phantom db, or a
			// -wal/-shm sidecar a read-write connection would leave behind).
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			require.Equal(t, []string{name}, names, "openReadOnly touched files other than dbPath")
		})
	}
}
