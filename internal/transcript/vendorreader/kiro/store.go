package kiro

import (
	"context"
	"database/sql"
	"net/url"

	// modernc.org/sqlite is a pure-Go, CGo-free port of SQLite3 — registered
	// under database/sql as driver "sqlite" (NOT "sqlite3": that name is
	// mattn/go-sqlite3, the CGo driver ctxloom/harp must never link, since
	// both build CGO_ENABLED=0 static — a CGo driver would break that build
	// outright). Verified before use, not assumed: built and ran a standalone
	// probe with CGO_ENABLED=0 against a real kiro-cli data.sqlite3 on this
	// box (SELECT against conversations_v2 succeeded) before this import was
	// ever written. This blank import is the ONLY place in the kiro package
	// (and the only place in the module, as of this change) that references
	// modernc.org/sqlite — the dependency stays isolated to this package.
	_ "modernc.org/sqlite"
)

// sqlDriverName is the name modernc.org/sqlite registers itself under via
// database/sql.Register (its own sqlite.go, package-init time) — a literal
// ctxloom does not control and must match exactly.
const sqlDriverName = "sqlite"

// readOnlyURI renders dbPath as the SQLite "file:" URI openReadOnly connects
// with. The path is URL-ESCAPED rather than concatenated: a db path is an
// arbitrary POSIX filename, and '#', '?' and '%' are all legal in one while
// being structural in a URI — '#' truncates the path at a fragment, '?'
// captures the query string (which is where mode=ro itself lives), and '%'
// introduces an escape SQLite decodes. Concatenating any of them silently
// addresses a DIFFERENT file and/or drops mode=ro, which is precisely the
// guarantee this URI exists to make.
//
// The authority is omitted ("file:<path>", never "file://<path>"): an empty
// authority would make a RELATIVE dbPath parse as a host, which SQLite
// rejects.
func readOnlyURI(dbPath string) string {
	u := url.URL{Scheme: "file", OmitHost: true, Path: dbPath, RawQuery: "mode=ro"}
	return u.String()
}

// openReadOnly opens the kiro-cli sqlite db at dbPath using SQLite's own
// read-only URI mode (mode=ro), not a plain-path open: a plain-path
// connection can still cause SQLite to create or touch a -wal/-shm sidecar
// file next to a LIVE kiro-cli database running in WAL journal mode (the
// mode a real ~/.local/share/kiro-cli/data.sqlite3 on this box was found in
// — see schema.go's package doc). This reader must never mutate the
// vendor's own store, even incidentally — mode=ro is a load-bearing
// guarantee here, not just a hint. Ping is called immediately (rather than
// leaving the driver's normal lazy-connect behavior) so an unopenable db
// (missing file, wrong permissions, not actually a sqlite file) fails loud
// right here — the same "structural failure, no further progress possible"
// case codex.Adapter.Convert's os.Open gets for free from being eager.
//
// That eagerness is exactly why ctx belongs here rather than only on the
// queries that follow: the ping is the call that actually touches the file
// (possibly on a slow or unresponsive mount), so it is the one a caller who
// has already given up must not be made to wait for.
func openReadOnly(ctx context.Context, dbPath string) (*sql.DB, error) {
	db, err := sql.Open(sqlDriverName, readOnlyURI(dbPath))
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// queryConversationValue returns the JSON value column for conversationID —
// the sole row conversations_v2's (key, conversation_id) primary key
// guarantees for a given id, on every real db inspected on this box (every
// conversation_id was 1:1 with exactly one key/row; see schema.go's package
// doc). ORDER BY updated_at DESC LIMIT 1 is defensive, not load-bearing
// against anything actually observed — it only matters if a future kiro-cli
// version ever legitimately re-keys the same conversation_id under a second
// workdir (key), a shape the real data on this box never exhibited.
// sql.ErrNoRows surfaces to the caller unwrapped-here (Convert wraps it) so
// "conversation not found" reads as the structural, non-degradable failure
// it is — there is no partial content to salvage from a row that never
// existed.
func queryConversationValue(ctx context.Context, db *sql.DB, conversationID string) (string, error) {
	row := db.QueryRowContext(ctx,
		`SELECT value FROM conversations_v2 WHERE conversation_id = ? ORDER BY updated_at DESC LIMIT 1`,
		conversationID)
	var value string
	if err := row.Scan(&value); err != nil {
		return "", err
	}
	return value, nil
}
