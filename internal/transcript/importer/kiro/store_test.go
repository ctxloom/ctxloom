package kiro

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOpenReadOnly_PathWithURIMetacharacters pins the load-bearing half of
// openReadOnly's contract: mode=ro is a GUARANTEE (this importer must never
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
			db := mustOpenAndCreateTable(t, dbPath)
			require.NoError(t, db.Close())

			ro, err := openReadOnly(dbPath)
			require.NoError(t, err, "openReadOnly must resolve the real file")
			defer func() { _ = ro.Close() }()

			// Addressing the INTENDED file: only it carries conversations_v2.
			var n int
			require.NoError(t, ro.QueryRow(`SELECT count(*) FROM conversations_v2`).Scan(&n),
				"the connection is not addressing the db at dbPath")

			// mode=ro still in force.
			_, err = ro.Exec(`CREATE TABLE probe (x INTEGER)`)
			require.Error(t, err, "mode=ro was dropped: this importer can WRITE the vendor's store")

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
