package operations

import (
	"bytes"
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

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// msToTime mirrors kiroreader.EnumerateConversations' own
// time.UnixMilli(...).UTC() conversion, so a test-built StartedAt compares on
// the same footing as the UpdatedAt values locateKiroConversation reads back
// from the fixture db.
func msToTime(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// kiroFixturePath is the REAL kiro conversation row the kiro reader
// package's own test suite already exercises
// (internal/transcript/vendorreader/kiro/testdata/conversation-fixture.json) —
// reused here rather than a second hand-rolled row, same discipline as
// vendorreader_test.go's codex/claude/antigravity fixtures.
var kiroFixturePath = filepath.Join(thisDir(), "..", "transcript", "vendorreader", "kiro", "testdata", "conversation-fixture.json")

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

// isolatedKiroFixtureDB seeds a kiro sqlite db at the exact path an
// isolated (worktree) kiro run's own XDG_DATA_HOME override resolves to
// (isolation/worktree.go's provisionConfigHome:
// <HarpEphemeralDir>/ctxloom-cfg-<agentID>-<randSuffix>/xdg-data/kiro-cli/data.sqlite3)
// — everything BUT the random suffix, which locateKiroConversation must
// discover by globbing rather than reconstructing (isolation/worktree.go's
// worktreeScratchPath appends a fresh random token per launch, so the exact
// leaf name is never knowable in advance).
func isolatedKiroFixtureDB(t *testing.T, harp string, rows ...kiroFixtureRow) string {
	t.Helper()
	ephemeral, err := paths.HarpEphemeralDir(harp)
	require.NoError(t, err)
	configHome := filepath.Join(ephemeral, "ctxloom-cfg-agent-a-rand7xk9")
	dataHome := filepath.Join(configHome, "xdg-data")
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

// TestLocateKiroConversation_PrefersIsolatedHarpDBOverHostAmbient pins that
// kiroDBPath() previously read only the HOST
// process's own $XDG_DATA_HOME, invisible to an isolated (worktree/
// container) kiro run's own per-agent override — so a conversation that
// happened entirely inside an isolated sqlite db was never found by the
// post-exit locate, even though (confirmed by reading run.go's ordering)
// the isolated config-home is still on disk at locate time — Cleanup() is
// deferred and fires only after this call returns. locateKiroConversation
// must now check the isolated harp-scoped db FIRST.
func TestLocateKiroConversation_PrefersIsolatedHarpDBOverHostAmbient(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "isolated-kiro-harp"

	row := loadKiroFixtureRow(t)
	isolatedRow := kiroFixtureRow{
		Key: row.Key, ConversationID: "isolated-only-conversation",
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Value: row.Value,
	}
	isolatedDB := isolatedKiroFixtureDB(t, harp, isolatedRow)

	// A host-ambient db also exists, with a DIFFERENT conversation in the
	// same project dir — proving the isolated db is preferred, not merely
	// found when nothing else is available.
	hostRow := kiroFixtureRow{
		Key: row.Key, ConversationID: "host-ambient-conversation",
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt + 1, Value: row.Value,
	}
	newKiroFixtureDB(t, hostRow)

	src, ok := locateKiroConversation(context.Background(), sessions.Entry{
		HarpName:   harp,
		ProjectDir: row.Key,
		StartedAt:  msToTime(row.UpdatedAt - 5_000),
	})
	require.True(t, ok)
	assert.Equal(t, isolatedDB+"#isolated-only-conversation", src,
		"an isolated run's own per-agent sqlite db must be checked before falling back to the host-ambient one")
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
		HarpName:      harp,
		Backend:       "kiro",
		ProjectDir:    row.Key,
		StartedAt:     msToTime(row.UpdatedAt - 60_000),
		EngineVersion: "2.13.0",
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

// TestLocateKiroConversation_BoundSessionIDPicksTheDBThatHoldsIt pins the
// candidate-db loop's whole reason for existing against the BOUND-SessionID
// route. candidateKiroDBPaths deliberately returns more than one db (every
// isolated per-agent config-home under this harp's ephemeral dir, then the
// host-ambient one) and promises the host db is used "when no isolated db was
// found" — but the bound route used to hand back the FIRST existing candidate
// unconditionally, without ever asking whether that db contains the
// conversation. A harp whose ephemeral dir holds any isolated kiro db (one
// isolated member agent is enough) therefore made its own host-db conversation
// unreachable: Convert fails with "no rows in result set" against a db that
// never held it, and the transcript is never imported.
func TestLocateKiroConversation_BoundSessionIDPicksTheDBThatHoldsIt(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "bound-session-multi-db-harp"
	const bound = "conversation-only-in-the-host-db"

	row := loadKiroFixtureRow(t)
	isolatedKiroFixtureDB(t, harp, kiroFixtureRow{
		Key: row.Key, ConversationID: "some-other-isolated-conversation",
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Value: row.Value,
	})
	hostDB := newKiroFixtureDB(t, kiroFixtureRow{
		Key: row.Key, ConversationID: bound,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Value: row.Value,
	})

	src, ok := locateKiroConversation(context.Background(), sessions.Entry{
		HarpName: harp, SessionID: bound,
	})
	require.True(t, ok)
	assert.Equal(t, hostDB+"#"+bound, src,
		"a bound conversation id must resolve against the candidate db that actually holds it, not whichever db happens to be checked first")
}

// TestLocateKiroConversation_UnreadableDBIsReportedNotSilentlyNothing pins
// that a kiro sqlite store that exists but cannot be read (corrupt,
// truncated mid-write, locked by a still-running kiro-cli, or simply not a
// sqlite file at all) is a DIFFERENT fact from "this session has no
// conversation to import", and the two used to be indistinguishable —
// EnumerateConversations' error was folded into the same ok=false the
// ordinary nothing-to-do case returns, so a sweeping import counted the harp
// as Skipped and said nothing. The locator still degrades to "not found"
// (fault tolerance: one bad store must not abort a whole backfill), but the
// failure is now stated.
func TestLocateKiroConversation_UnreadableDBIsReportedNotSilentlyNothing(t *testing.T) {
	testsupport.Isolate(t)
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	dbPath := filepath.Join(dataHome, "kiro-cli", "data.sqlite3")
	require.NoError(t, os.MkdirAll(filepath.Dir(dbPath), 0o755))
	require.NoError(t, os.WriteFile(dbPath, []byte("this is not a sqlite database"), 0o600))

	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	defer restore()

	_, ok := locateKiroConversation(context.Background(), sessions.Entry{ProjectDir: "/some/project"})
	assert.False(t, ok, "an unreadable store still yields no locator — one bad db must not abort the backfill")
	assert.Contains(t, warnings.String(), "kiro conversation store",
		"an unreadable kiro store must be reported, not reported as nothing to convert")
	assert.Contains(t, warnings.String(), dbPath, "the advisory must name the db the user has to fix")
}

// TestLocateKiroConversation_UnreadableCandidateDBIsReported pins the SECOND
// consumer of EnumerateConversations, kiroDBHoldingConversation, against the
// same standard its sibling locateKiroConversationInDB already meets: an
// unreadable candidate store is skipped (the next candidate may still hold the
// bound conversation — a corrupt isolated db must never make a host-db
// conversation unreachable) but it is SAID, because "I cannot read this store"
// is not "this session has nothing to import".
//
// The isolated per-agent db here is not a sqlite file at all, and the host
// ambient one holds the bound conversation: the locator must still resolve
// against the host db, and the failure must still reach the user.
func TestLocateKiroConversation_UnreadableCandidateDBIsReported(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "unreadable-candidate-harp"
	const bound = "conversation-only-in-the-host-db"

	ephemeral, err := paths.HarpEphemeralDir(harp)
	require.NoError(t, err)
	badDB := filepath.Join(ephemeral, "ctxloom-cfg-agent-a-rand7xk9", "xdg-data", "kiro-cli", "data.sqlite3")
	require.NoError(t, os.MkdirAll(filepath.Dir(badDB), 0o755))
	require.NoError(t, os.WriteFile(badDB, []byte("this is not a sqlite database"), 0o600))

	row := loadKiroFixtureRow(t)
	hostDB := newKiroFixtureDB(t, kiroFixtureRow{
		Key: row.Key, ConversationID: bound,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, Value: row.Value,
	})

	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	defer restore()

	src, ok := locateKiroConversation(context.Background(), sessions.Entry{
		HarpName: harp, SessionID: bound,
	})
	require.True(t, ok)
	assert.Equal(t, hostDB+"#"+bound, src,
		"an unreadable candidate must be skipped, not allowed to shadow the db that holds the conversation")
	assert.Contains(t, warnings.String(), "kiro conversation store",
		"a candidate db that could not be enumerated must be reported, not silently skipped")
	assert.Contains(t, warnings.String(), badDB, "the advisory must name the db the user has to fix")
}
