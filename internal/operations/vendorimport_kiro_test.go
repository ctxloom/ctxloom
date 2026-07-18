package operations

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite" // registers driver "sqlite" — see kiro/store.go's own doc for why this exact name/driver

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// msToTime mirrors kiroimporter.EnumerateConversations' own
// time.UnixMilli(...).UTC() conversion, so a test-built StartedAt compares on
// the same footing as the UpdatedAt values locateKiroConversation reads back
// from the fixture db.
func msToTime(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// kiroFixturePath is the REAL kiro conversation row the kiro importer
// package's own test suite already exercises
// (internal/transcript/importer/kiro/testdata/conversation-fixture.json) —
// reused here rather than a second hand-rolled row, same discipline as
// vendorimport_test.go's codex/claude/antigravity fixtures.
var kiroFixturePath = filepath.Join(thisDir(), "..", "transcript", "importer", "kiro", "testdata", "conversation-fixture.json")

// kiroFixtureRow mirrors kiro_test.go's own fixtureRow shape (conversations_v2
// row: key/conversation_id/created_at/updated_at + the row's own value blob).
type kiroFixtureRow struct {
	Key            string          `json:"key"`
	ConversationID string          `json:"conversation_id"`
	CreatedAt      int64           `json:"created_at"`
	UpdatedAt      int64           `json:"updated_at"`
	Value          json.RawMessage `json:"value"`
}

// newKiroFixtureDB creates a fresh conversations_v2 sqlite db (kiro/schema.go's
// reverse-engineered DDL) at $XDG_DATA_HOME/kiro-cli/data.sqlite3 — the exact
// path kiroDBPath resolves — seeded with rows, and points XDG_DATA_HOME at a
// fresh t.TempDir() so kiroDBPath finds it deterministically regardless of
// the host's own ambient XDG_DATA_HOME.
func newKiroFixtureDB(t *testing.T, rows ...kiroFixtureRow) string {
	t.Helper()
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)

	dbPath := filepath.Join(dataHome, "kiro-cli", "data.sqlite3")
	require.NoError(t, os.MkdirAll(filepath.Dir(dbPath), 0o755))

	db, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS conversations_v2 (
		key TEXT NOT NULL,
		conversation_id TEXT NOT NULL,
		value TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (key, conversation_id)
	)`)
	require.NoError(t, err)

	for _, row := range rows {
		_, err := db.Exec(
			`INSERT INTO conversations_v2 (key, conversation_id, value, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
			row.Key, row.ConversationID, string(row.Value), row.CreatedAt, row.UpdatedAt,
		)
		require.NoError(t, err)
	}
	return dbPath
}

func loadKiroFixtureRow(t *testing.T) kiroFixtureRow {
	t.Helper()
	raw, err := os.ReadFile(kiroFixturePath)
	require.NoError(t, err)
	var row kiroFixtureRow
	require.NoError(t, json.Unmarshal(raw, &row))
	return row
}

func TestKiroDBPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	assert.Equal(t, filepath.Join(home, ".local", "share", "kiro-cli", "data.sqlite3"), kiroDBPath())

	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	assert.Equal(t, filepath.Join(xdg, "kiro-cli", "data.sqlite3"), kiroDBPath())
}

func TestLocateKiroConversation_NoDB(t *testing.T) {
	testsupport.Isolate(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "does-not-exist"))
	_, ok := locateKiroConversation(context.Background(), sessions.Entry{ProjectDir: "/some/project"})
	assert.False(t, ok)
}

// TestLocateKiroConversation_BoundSessionID covers the "bind DID supply a
// session_id" route: locate must trust it directly and never enumerate.
func TestLocateKiroConversation_BoundSessionID(t *testing.T) {
	row := loadKiroFixtureRow(t)
	dbPath := newKiroFixtureDB(t, row)

	src, ok := locateKiroConversation(context.Background(), sessions.Entry{SessionID: "bound-conversation-id"})
	require.True(t, ok)
	assert.Equal(t, dbPath+"#bound-conversation-id", src)
	_ = row // db just needs to exist; this route never reads its rows
}

// TestLocateKiroConversation_EnumerateFallback covers the realistic case
// (no SessionID bound — kiro's agentSpawn hook payload has not been
// confirmed to carry a usable conversation id): locate falls back to
// matching EnumerateConversations by WorkDir, picking the newest
// conversation not older than StartedAt.
func TestLocateKiroConversation_EnumerateFallback(t *testing.T) {
	row := loadKiroFixtureRow(t)
	older := kiroFixtureRow{
		Key: row.Key, ConversationID: "older-conversation-in-same-dir",
		CreatedAt: row.CreatedAt - 10_000, UpdatedAt: row.UpdatedAt - 10_000, Value: row.Value,
	}
	otherDir := kiroFixtureRow{
		Key: "/some/other/project", ConversationID: "conversation-in-other-dir",
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt + 1, Value: row.Value,
	}
	dbPath := newKiroFixtureDB(t, older, row, otherDir)

	startedAt := msToTime(row.UpdatedAt - 5_000) // after `older`, before `row`
	src, ok := locateKiroConversation(context.Background(), sessions.Entry{
		ProjectDir: row.Key,
		StartedAt:  startedAt,
	})
	require.True(t, ok)
	assert.Equal(t, dbPath+"#"+row.ConversationID, src,
		"must pick the newest conversation in the matching WorkDir that isn't older than StartedAt — not the older same-dir row, and not the other project's row")
}

func TestLocateKiroConversation_NoMatchingWorkDir(t *testing.T) {
	row := loadKiroFixtureRow(t)
	newKiroFixtureDB(t, row)

	_, ok := locateKiroConversation(context.Background(), sessions.Entry{ProjectDir: "/nothing/matches/this"})
	assert.False(t, ok)
}

func TestLocateKiroConversation_NoProjectDirNoSessionID(t *testing.T) {
	row := loadKiroFixtureRow(t)
	newKiroFixtureDB(t, row)

	_, ok := locateKiroConversation(context.Background(), sessions.Entry{})
	assert.False(t, ok, "no SessionID and no ProjectDir leaves nothing to match against")
}

// TestConvertVendorTranscript_KiroEnumerateFallback is the end-to-end kiro
// path through the SAME ConvertVendorTranscript every other engine's test
// exercises: a real fixture conversation, no SessionID bound, located via
// enumerate-by-workdir, and actually converted into a canonical transcript.
func TestConvertVendorTranscript_KiroEnumerateFallback(t *testing.T) {
	testsupport.Isolate(t)
	row := loadKiroFixtureRow(t)
	newKiroFixtureDB(t, row)

	harp := "convert-kiro-harp"
	e := sessions.Entry{
		HarpName:   harp,
		Backend:    "kiro",
		ProjectDir: row.Key,
		StartedAt:  msToTime(row.UpdatedAt - 60_000),
	}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	assert.True(t, converted)

	lines := canonicalLines(t, harp)
	require.NotEmpty(t, lines)
	for _, l := range lines {
		assert.Contains(t, l, `"engine":"kiro"`)
	}
}
