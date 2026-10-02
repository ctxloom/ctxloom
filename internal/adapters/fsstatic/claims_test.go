package fsstatic

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/confpatch"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

const (
	claimsDir  = "/home/u/.ctxloom/records"
	mcpTarget  = "/proj/.mcp.json"
	userSecret = "abc123-the-users-own-token"
)

// userMCP is a .mcp.json as a USER would write it: a key ctxloom models
// nowhere and a remote server carrying the user's own secret.
var userMCP = `{
  "$schema": "https://example.com/mcp.schema.json",
  "mcpServers": {
    "remote-thing": {
      "type": "http",
      "url": "https://mcp.example.com/v1",
      "headers": {"Authorization": "Bearer ` + userSecret + `"}
    }
  }
}
`

var (
	session = delivery.SessionWriter("brisk-otter")
	other   = delivery.SessionWriter("calm-heron")
	project = delivery.ProjectWriter

	installEntry  = map[string]any{"command": "ctxloom", "args": []any{"mcp"}}
	sessionEntry  = map[string]any{"command": "ctxloom", "args": []any{"mcp", "--session", "brisk-otter"}}
	relayEntry    = map[string]any{"command": "ctxloom", "args": []any{"claude-relay"}}
	taskloomEntry = map[string]any{"command": "taskloom", "args": []any{"mcp"}}
)

func server(name string, v any) present.Claim {
	return present.Claim{Pointer: "/mcpServers/" + name, Value: v}
}

func newClaims(t *testing.T, fs afero.Fs) *Claims {
	t.Helper()
	c, err := NewClaims(fs, claimsDir)
	require.NoError(t, err)
	return c
}

func noLock(_ string, fn func() error) error { return fn() }

// commitOps runs ops against one fresh batch and commits it.
func commitOps(t *testing.T, c *Claims, fs afero.Fs, ops ...func(*Staging) error) (safefs.Committed, error) {
	t.Helper()
	b := safefs.NewBatch(fs, noLock)
	s := c.In(b)
	for _, op := range ops {
		if err := op(s); err != nil {
			return safefs.Committed{}, err
		}
	}
	return b.Commit()
}

func stage(target string, w delivery.Writer, cs ...present.Claim) func(*Staging) error {
	return func(s *Staging) error { return s.Stage(target, w, cs) }
}

func release(target string, w delivery.Writer) func(*Staging) error {
	return func(s *Staging) error { return s.Release(target, w, nil) }
}

func mustCommit(t *testing.T, c *Claims, fs afero.Fs, ops ...func(*Staging) error) safefs.Committed {
	t.Helper()
	got, err := commitOps(t, c, fs, ops...)
	require.NoError(t, err)
	return got
}

func servers(t *testing.T, fs afero.Fs) map[string]any {
	t.Helper()
	data, err := afero.ReadFile(fs, mcpTarget)
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	m, _ := doc["mcpServers"].(map[string]any)
	return m
}

func read(t *testing.T, fs afero.Fs, path string) string {
	t.Helper()
	data, err := afero.ReadFile(fs, path)
	require.NoError(t, err)
	return string(data)
}

func withUserFile(t *testing.T) afero.Fs {
	t.Helper()
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, mcpTarget, userMCP, 0o644)
	return fs
}

func TestClaimsAStagedEntryLandsAndReleasingItRestoresTheUsersBytes(t *testing.T) {
	fs := withUserFile(t)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))
	assert.Equal(t, installEntry["command"], servers(t, fs)["ctxloom"].(map[string]any)["command"])
	assert.Contains(t, servers(t, fs), "remote-thing")

	mustCommit(t, c, fs, release(mcpTarget, project))
	assert.Equal(t, userMCP, read(t, fs, mcpTarget), "the user is left with exactly the file they wrote")
}

func TestClaimsCreateTheFileAndRemoveItWithTheLastClaim(t *testing.T) {
	fs := afero.NewMemMapFs()
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))
	assert.Contains(t, servers(t, fs), "ctxloom")

	got := mustCommit(t, c, fs, release(mcpTarget, project))
	assert.Equal(t, []string{mcpTarget}, got.Removed, "a file ctxloom created leaves with its last claim")
	targets, err := c.Targets(project)
	require.NoError(t, err)
	assert.Empty(t, targets)

	// The emptied record outlives the write that emptied it (its note says
	// that write may not have landed); the next operation finds the target
	// settled and removes it.
	mustCommit(t, c, fs, release(mcpTarget, project))
	_, err = fs.Stat(filepath.Join(claimsDir, confpatch.RecordPrefix(mcpTarget)+".claims.yaml"))
	assert.True(t, os.IsNotExist(err), "no record is left for a file nobody claims anything in")
}

// Bug 2: an entry two writers both put there is SHARED, and outlives
// whichever of them leaves first.
func TestClaimsASharedEntrySurvivesEitherWritersRelease(t *testing.T) {
	for _, first := range []delivery.Writer{project, session} {
		t.Run(string(first), func(t *testing.T) {
			fs := withUserFile(t)
			c := newClaims(t, fs)
			mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))
			mustCommit(t, c, fs, stage(mcpTarget, session, server("ctxloom", installEntry)))

			mustCommit(t, c, fs, release(mcpTarget, first))
			assert.Contains(t, servers(t, fs), "ctxloom", "the other writer still claims it")

			second := session
			if first == session {
				second = project
			}
			mustCommit(t, c, fs, release(mcpTarget, second))
			assert.Equal(t, userMCP, read(t, fs, mcpTarget))
		})
	}
}

// The reverse-order case at the unit level: a run delivers, an at-rest apply
// lands mid-run, the run tears down, then the project uninstalls. Neither
// release may take the other writer's entry.
func TestClaimsAnAtRestApplyMidRunKeepsTheRunsEntry(t *testing.T) {
	fs := withUserFile(t)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, session, server("ctxloom", installEntry), server("ctxloom-relay", relayEntry)))
	mustCommit(t, c, fs, release(mcpTarget, project), stage(mcpTarget, project, server("ctxloom", installEntry)))
	assert.Contains(t, servers(t, fs), "ctxloom-relay", "the at-rest apply keeps the LIVE run's entry")

	mustCommit(t, c, fs, release(mcpTarget, session))
	got := servers(t, fs)
	assert.NotContains(t, got, "ctxloom-relay", "the run's own entry leaves with it")
	assert.Contains(t, got, "ctxloom", "the install's entry outlives the run")

	mustCommit(t, c, fs, release(mcpTarget, project))
	assert.Equal(t, userMCP, read(t, fs, mcpTarget))
}

// F2: SESSION OVER PROJECT, whichever stages last. Releasing the session
// falls back to the project's value.
func TestClaimsTheSessionsValueWinsWhicheverStagesLast(t *testing.T) {
	for _, order := range [][]delivery.Writer{{project, session}, {session, project}} {
		t.Run(string(order[0])+"-first", func(t *testing.T) {
			fs := withUserFile(t)
			c := newClaims(t, fs)
			value := map[delivery.Writer]any{project: installEntry, session: sessionEntry}
			for _, w := range order {
				mustCommit(t, c, fs, stage(mcpTarget, w, server("ctxloom", value[w])))
			}
			assert.Equal(t, "--session", servers(t, fs)["ctxloom"].(map[string]any)["args"].([]any)[1])

			mustCommit(t, c, fs, release(mcpTarget, session))
			assert.Equal(t, []any{"mcp"}, servers(t, fs)["ctxloom"].(map[string]any)["args"], "the project's value comes back")
		})
	}
}

// Releasing the PROJECT under a live session leaves the session's value.
func TestClaimsReleasingTheProjectLeavesTheSessionsValue(t *testing.T) {
	fs := withUserFile(t)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, session, server("ctxloom", sessionEntry)))
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))
	mustCommit(t, c, fs, release(mcpTarget, project))
	assert.Equal(t, "--session", servers(t, fs)["ctxloom"].(map[string]any)["args"].([]any)[1])
}

// Among writers of one kind the latest stage wins: a session launching now
// needs ITS entry in the file, and falls back to the earlier one on release.
func TestClaimsBetweenTwoSessionsTheLatestWins(t *testing.T) {
	fs := withUserFile(t)
	c := newClaims(t, fs)
	a := map[string]any{"command": "ctxloom", "args": []any{"a"}}
	b := map[string]any{"command": "ctxloom", "args": []any{"b"}}
	mustCommit(t, c, fs, stage(mcpTarget, session, server("ctxloom-relay", a)))
	mustCommit(t, c, fs, stage(mcpTarget, other, server("ctxloom-relay", b)))
	assert.Equal(t, []any{"b"}, servers(t, fs)["ctxloom-relay"].(map[string]any)["args"])
	mustCommit(t, c, fs, release(mcpTarget, other))
	assert.Equal(t, []any{"a"}, servers(t, fs)["ctxloom-relay"].(map[string]any)["args"])
}

// The container wedge: the writer that CREATED /mcpServers leaving first must
// not take the container — another writer's entry is in it — and the last
// writer out takes it, leaving the file as the user had it.
func TestClaimsAContainerOutlivesTheWriterThatCreatedIt(t *testing.T) {
	const base = "{\n  \"model\": \"opus\"\n}\n"
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, mcpTarget, base, 0o644)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, session, server("ctxloom-relay", relayEntry)))
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))

	mustCommit(t, c, fs, release(mcpTarget, session))
	assert.Equal(t, []string{"ctxloom"}, keys(servers(t, fs)))

	mustCommit(t, c, fs, release(mcpTarget, project))
	assert.Equal(t, base, read(t, fs, mcpTarget))
}

// Carry-forward: a release keeps the claims its keep func names, exactly as
// recorded, until a later release drops them.
func TestClaimsAReleaseKeepsWhatItsKeepFuncNames(t *testing.T) {
	fs := withUserFile(t)
	c := newClaims(t, fs)
	tl := server("taskloom", taskloomEntry)
	tl.Via = "companion:taskloom"
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry), tl))

	keepTaskloom := func(_, via string) bool { return via == "companion:taskloom" }
	mustCommit(t, c, fs,
		func(s *Staging) error { return s.Release(mcpTarget, project, keepTaskloom) },
		stage(mcpTarget, project, server("ctxloom", installEntry)))
	assert.Contains(t, servers(t, fs), "taskloom", "a companion whose probe failed keeps its entry")

	mustCommit(t, c, fs, release(mcpTarget, project), stage(mcpTarget, project, server("ctxloom", installEntry)))
	assert.NotContains(t, servers(t, fs), "taskloom", "a later delivery without it drops it")
}

// F8: a user's own value at the path a claim names is never displaced.
func TestClaimsRefuseAUsersValueAtTheClaimedPath(t *testing.T) {
	fs := withUserFile(t)
	c := newClaims(t, fs)
	_, err := commitOps(t, c, fs, stage(mcpTarget, project, server("remote-thing", installEntry)))
	require.ErrorIs(t, err, ErrNotOurs)
	var no *NotOursError
	require.True(t, errors.As(err, &no))
	assert.Equal(t, NotOursError{Target: mcpTarget, Pointer: "/mcpServers/remote-thing"}, *no)
	assert.Equal(t, userMCP, read(t, fs, mcpTarget))
	targets, err := c.Targets(project)
	require.NoError(t, err)
	assert.Empty(t, targets, "a refused stage records nothing")
}

// An unclaimed entry that runs ctxloom is ctxloom's own (an install from
// before this record existed, or another copy of the binary): taken over.
func TestClaimsTakeOverAnEntryCtxloomWroteWithoutARecord(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, mcpTarget, `{"mcpServers": {"ctxloom": {"command": "/old/bin/ctxloom", "args": ["old"]}}}`, 0o644)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))
	assert.Equal(t, []any{"mcp"}, servers(t, fs)["ctxloom"].(map[string]any)["args"])
}

// Drift: a claimed entry the user has since edited into something that is not
// ctxloom's is not taken back out.
func TestClaimsRefuseToReleaseAnEntryTheUserEdited(t *testing.T) {
	fs := withUserFile(t)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))
	edited := strings.Replace(read(t, fs, mcpTarget), `"command": "ctxloom"`, `"command": "my-wrapper"`, 1)
	require.NotEqual(t, edited, read(t, fs, mcpTarget))
	testsupport.WriteFileString(t, fs, mcpTarget, edited, 0o644)

	_, err := commitOps(t, c, fs, release(mcpTarget, project))
	require.ErrorIs(t, err, ErrNotOurs)
	assert.Equal(t, edited, read(t, fs, mcpTarget))
}

// An edit to one claimed entry does not wedge an operation on another.
func TestClaimsADriftedEntryDoesNotBlockAnUnrelatedRelease(t *testing.T) {
	fs := withUserFile(t)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))
	mustCommit(t, c, fs, stage(mcpTarget, session, server("ctxloom-relay", relayEntry)))
	edited := strings.Replace(read(t, fs, mcpTarget), `"mcp"`, `"mcp", "--mine"`, 1)
	testsupport.WriteFileString(t, fs, mcpTarget, edited, 0o644)

	mustCommit(t, c, fs, release(mcpTarget, session))
	assert.NotContains(t, servers(t, fs), "ctxloom-relay")
	assert.Equal(t, []any{"mcp", "--mine"}, servers(t, fs)["ctxloom"].(map[string]any)["args"])
}

// One operation, however many writers and claims it stages, writes the
// target once.
func TestClaimsWriteTheTargetOncePerCommit(t *testing.T) {
	fs := &renameCounter{Fs: withUserFile(t), n: map[string]int{}}
	c := newClaims(t, fs)
	mustCommit(t, c, fs,
		release(mcpTarget, project),
		stage(mcpTarget, project, server("ctxloom", installEntry)),
		stage(mcpTarget, project, server("taskloom", taskloomEntry)),
		stage(mcpTarget, session, server("ctxloom-relay", relayEntry)))
	assert.Equal(t, 1, fs.n[mcpTarget])

	fs.n = map[string]int{}
	mustCommit(t, c, fs, release(mcpTarget, project), stage(mcpTarget, project, server("ctxloom", installEntry), server("taskloom", taskloomEntry)))
	assert.Zero(t, fs.n[mcpTarget], "a redelivery of the same claims writes nothing")
	assert.Zero(t, fs.n[filepath.Join(claimsDir, confpatch.RecordPrefix(mcpTarget)+".claims.yaml")], "nor its record")
}

// F8: the record holds ctxloom's own values only — never a user's, and so
// never a user's secret.
func TestClaimsTheRecordNeverHoldsTheUsersValues(t *testing.T) {
	fs := withUserFile(t)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))
	entries, err := afero.ReadDir(fs, claimsDir)
	require.NoError(t, err)
	require.NotEmpty(t, entries)
	for _, e := range entries {
		assert.NotContains(t, read(t, fs, filepath.Join(claimsDir, e.Name())), userSecret, e.Name())
	}
}

// F5: the record this replaces is deleted, by exact name, on the first write.
func TestClaimsDeleteTheOldOwnershipRecord(t *testing.T) {
	fs := withUserFile(t)
	old := filepath.Join(claimsDir, confpatch.RecordPrefix(mcpTarget)+".ownership.yaml")
	unrelated := filepath.Join(claimsDir, confpatch.RecordPrefix("/elsewhere/.mcp.json")+".ownership.yaml")
	testsupport.WriteFileString(t, fs, old, "writers: {}\n", 0o600)
	testsupport.WriteFileString(t, fs, unrelated, "writers: {}\n", 0o600)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))
	_, err := fs.Stat(old)
	assert.True(t, os.IsNotExist(err), "the old record for this target is gone")
	_, err = fs.Stat(unrelated)
	assert.NoError(t, err, "another target's is not this write's to delete")
}

// Record first, then target: a target write that never lands leaves a record
// that knows it may not have. The next operation completes it.
// The recovery tests claim entries that do NOT run ctxloom, so no ownership
// proof but the record's own note can recognise the value a lost write left.
var (
	taskA = map[string]any{"command": "taskloom", "args": []any{"a"}}
	taskB = map[string]any{"command": "taskloom", "args": []any{"b"}}
)

func TestClaimsRecoverAChangeWhoseTargetWriteNeverLanded(t *testing.T) {
	fs := &failingRename{Fs: withUserFile(t), target: mcpTarget}
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("taskloom", taskA)))

	fs.fail = true
	_, err := commitOps(t, c, fs, stage(mcpTarget, project, server("taskloom", taskB)))
	require.ErrorIs(t, err, errInjected)
	fs.fail = false
	assert.Equal(t, []any{"a"}, servers(t, fs)["taskloom"].(map[string]any)["args"], "the write did not land")

	mustCommit(t, c, fs, release(mcpTarget, project))
	assert.Equal(t, userMCP, read(t, fs, mcpTarget), "the old value is recognised as ctxloom's and taken out")
}

// Two lost writes in a row: the second's note must still describe the file
// as it stands — the state before the FIRST lost write.
func TestClaimsRecoverAfterTwoLostWritesInARow(t *testing.T) {
	fs := &failingRename{Fs: withUserFile(t), target: mcpTarget}
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("taskloom", taskA)))

	fs.fail = true
	_, err := commitOps(t, c, fs, stage(mcpTarget, project, server("taskloom", taskB)))
	require.ErrorIs(t, err, errInjected)
	_, err = commitOps(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))
	require.ErrorIs(t, err, errInjected)
	fs.fail = false

	mustCommit(t, c, fs, release(mcpTarget, project))
	assert.Equal(t, userMCP, read(t, fs, mcpTarget))
}

func TestClaimsRecoverAReleaseWhoseTargetWriteNeverLanded(t *testing.T) {
	fs := &failingRename{Fs: withUserFile(t), target: mcpTarget}
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("taskloom", taskA)))
	mustCommit(t, c, fs, stage(mcpTarget, session, server("ctxloom-relay", relayEntry)))

	fs.fail = true
	_, err := commitOps(t, c, fs, release(mcpTarget, project))
	require.ErrorIs(t, err, errInjected)
	fs.fail = false

	mustCommit(t, c, fs, release(mcpTarget, session))
	assert.Equal(t, userMCP, read(t, fs, mcpTarget), "the entry the lost write was removing is gone too")
}

func TestClaimsPathsReportWritersEffectiveFirstAndWhetherTheFileHoldsIt(t *testing.T) {
	fs := withUserFile(t)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))
	mustCommit(t, c, fs, stage(mcpTarget, session, server("ctxloom", sessionEntry)))
	got, err := c.Paths(fs, mcpTarget)
	require.NoError(t, err)
	assert.Equal(t, []PathState{{Pointer: "/mcpServers/ctxloom", Writers: []delivery.Writer{session, project}, Live: true}}, got)

	edited := strings.Replace(read(t, fs, mcpTarget), `"brisk-otter"`, `"someone-else"`, 1)
	require.NotEqual(t, edited, read(t, fs, mcpTarget))
	testsupport.WriteFileString(t, fs, mcpTarget, edited, 0o644)
	got, err = c.Paths(fs, mcpTarget)
	require.NoError(t, err)
	assert.False(t, got[0].Live, "the file no longer holds the effective value")
}

func TestClaimsTargetsAndWriters(t *testing.T) {
	fs := withUserFile(t)
	c := newClaims(t, fs)
	const second = "/proj/other.json"
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))
	mustCommit(t, c, fs, stage(second, session, present.Claim{Pointer: "/x", Value: 1}))
	got, err := c.Targets(project)
	require.NoError(t, err)
	assert.Equal(t, []string{mcpTarget}, got)
	writers, err := c.Writers()
	require.NoError(t, err)
	assert.Equal(t, []delivery.Writer{project, session}, writers)
}

// A file in no format hew reads is claimed whole; ctxloom never claims over a
// file the user already has there.
func TestClaimsAWholeFileClaim(t *testing.T) {
	const cmd = "/proj/.claude/commands/x.md"
	fs := afero.NewMemMapFs()
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(cmd, project, present.Claim{Value: []byte("body\n")}))
	assert.Equal(t, "body\n", read(t, fs, cmd))
	mustCommit(t, c, fs, release(cmd, project))
	_, err := fs.Stat(cmd)
	assert.True(t, os.IsNotExist(err))

	testsupport.WriteFileString(t, fs, cmd, "the user's own\n", 0o644)
	_, err = commitOps(t, c, fs, stage(cmd, project, present.Claim{Value: []byte("body\n")}))
	require.ErrorIs(t, err, ErrNotOurs)
	assert.Equal(t, "the user's own\n", read(t, fs, cmd))
}

func TestClaimsRefuseAPointerIntoAFileHewCannotRead(t *testing.T) {
	fs := afero.NewMemMapFs()
	c := newClaims(t, fs)
	_, err := commitOps(t, c, fs, stage("/proj/notes.md", project, present.Claim{Pointer: "/x", Value: 1}))
	require.Error(t, err)
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

type renameCounter struct {
	afero.Fs
	mu sync.Mutex
	n  map[string]int
}

func (r *renameCounter) Rename(o, n string) error {
	r.mu.Lock()
	r.n[n]++
	r.mu.Unlock()
	return safefs.Rename(r.Fs, o, n)
}

var errInjected = errors.New("injected: the target write did not land")

// failingRename fails the rename onto target while fail is set: the record,
// written first, lands; the target does not.
type failingRename struct {
	afero.Fs
	target string
	fail   bool
}

func (f *failingRename) Rename(o, n string) error {
	if f.fail && n == f.target {
		return errInjected
	}
	return safefs.Rename(f.Fs, o, n)
}

func TestClaimsStageRefusesAMalformedClaim(t *testing.T) {
	fs := afero.NewMemMapFs()
	c := newClaims(t, fs)
	s := c.In(safefs.NewBatch(fs, noLock))
	for name, claims := range map[string][]present.Claim{
		"a place claimed twice":                 {server("x", 1), server("x", 2)},
		"a whole file whose value is not bytes": {{Value: "text"}},
		"a whole file beside an inner claim":    {{Value: []byte("x")}, server("x", 1)},
		"a pointer that is not one":             {{Pointer: "mcpServers", Value: 1}},
	} {
		assert.Error(t, s.Stage(mcpTarget, project, claims), name)
	}
	assert.Error(t, s.Stage("/proj/notes.md", project, []present.Claim{{Pointer: "/x", Value: 1}}), "a pointer into a file hew cannot read")
	assert.Error(t, s.Stage(mcpTarget, "", []present.Claim{server("x", 1)}), "no writer")
	assert.Error(t, s.Release(mcpTarget, "", nil), "no writer")
}

// A value someone else put there that happens to equal ctxloom's claim — a
// companion's own registration, written by that companion — is co-claimed
// but never taken out: the last release leaves it as it was found.
func TestClaimsAValueFoundThereIsLeftByTheLastRelease(t *testing.T) {
	const withTaskloom = `{"mcpServers": {"taskloom": {"command": "taskloom", "args": ["mcp"]}}}`
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, mcpTarget, withTaskloom, 0o644)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("taskloom", taskloomEntry), server("ctxloom", installEntry)))
	mustCommit(t, c, fs, release(mcpTarget, project))
	assert.Equal(t, withTaskloom, read(t, fs, mcpTarget))
}

// A claimed entry rewritten since by another copy of ctxloom is still
// ctxloom's: a release takes it out rather than refusing.
func TestClaimsReleaseTakesOutAClaimedEntryAnotherCtxloomRewrote(t *testing.T) {
	fs := withUserFile(t)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))
	rewritten := strings.Replace(read(t, fs, mcpTarget), `"command": "ctxloom"`, `"command": "/other/bin/ctxloom"`, 1)
	testsupport.WriteFileString(t, fs, mcpTarget, rewritten, 0o644)
	mustCommit(t, c, fs, release(mcpTarget, project))
	assert.Equal(t, userMCP, read(t, fs, mcpTarget))
}

// A claim's value can change, whatever command it runs.
func TestClaimsAWriterChangesItsOwnValue(t *testing.T) {
	fs := withUserFile(t)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("taskloom", taskA)))
	mustCommit(t, c, fs, stage(mcpTarget, project, server("taskloom", taskB)))
	assert.Equal(t, []any{"b"}, servers(t, fs)["taskloom"].(map[string]any)["args"])
	got, err := c.Paths(fs, mcpTarget)
	require.NoError(t, err)
	assert.Equal(t, []delivery.Writer{project}, got[0].Writers, "a writer is listed once however often it restates")
}

// A container ctxloom created that the user has since put their own entry in
// is the user's now too: it outlives ctxloom's last claim.
func TestClaimsAContainerTheUserWroteIntoIsKept(t *testing.T) {
	fs := afero.NewMemMapFs()
	c := newClaims(t, fs)
	testsupport.WriteFileString(t, fs, mcpTarget, "{\n  \"model\": \"opus\"\n}\n", 0o644)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))
	withMine := strings.Replace(read(t, fs, mcpTarget), `"ctxloom": {`, `"mine": {"command": "mine"}, "ctxloom": {`, 1)
	require.NotEqual(t, withMine, read(t, fs, mcpTarget))
	testsupport.WriteFileString(t, fs, mcpTarget, withMine, 0o644)
	mustCommit(t, c, fs, release(mcpTarget, project))
	assert.Equal(t, []string{"mine"}, keys(servers(t, fs)))
}

// A user's own file that is an empty document is not ctxloom's to remove.
func TestClaimsAUsersEmptyDocumentOutlivesTheLastRelease(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, mcpTarget, "{}", 0o644)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))
	mustCommit(t, c, fs, release(mcpTarget, project))
	assert.Equal(t, "{}", read(t, fs, mcpTarget))
}

// A whole file the user already had, byte for byte, is found, not created:
// the last release leaves it.
func TestClaimsAWholeFileFoundThereIsLeft(t *testing.T) {
	const cmd = "/proj/.claude/commands/x.md"
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, cmd, "body\n", 0o644)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(cmd, project, present.Claim{Value: []byte("body\n")}))
	mustCommit(t, c, fs, release(cmd, project))
	assert.Equal(t, "body\n", read(t, fs, cmd))
}

func TestClaimsPathsOfAWholeFileReportWhetherItStillHoldsTheClaim(t *testing.T) {
	const cmd = "/proj/.claude/commands/x.md"
	fs := afero.NewMemMapFs()
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(cmd, project, present.Claim{Value: []byte("body\n")}))
	got, err := c.Paths(fs, cmd)
	require.NoError(t, err)
	assert.True(t, got[0].Live)
	testsupport.WriteFileString(t, fs, cmd, "edited\n", 0o644)
	got, err = c.Paths(fs, cmd)
	require.NoError(t, err)
	assert.False(t, got[0].Live)
}

// Restating a CHANGED value is a newer claim: among sessions it goes on top.
func TestClaimsARestatedChangeIsTheLatestClaim(t *testing.T) {
	fs := withUserFile(t)
	c := newClaims(t, fs)
	a2 := map[string]any{"command": "ctxloom", "args": []any{"a2"}}
	mustCommit(t, c, fs, stage(mcpTarget, session, server("ctxloom-relay", taskA)))
	mustCommit(t, c, fs, stage(mcpTarget, other, server("ctxloom-relay", taskB)))
	mustCommit(t, c, fs, release(mcpTarget, session), stage(mcpTarget, session, server("ctxloom-relay", a2)))
	assert.Equal(t, []any{"a2"}, servers(t, fs)["ctxloom-relay"].(map[string]any)["args"])
}

// An unclaimed entry identical to ctxloom's claim that RUNS ctxloom is
// ctxloom's own (an install from before the record), not a value found:
// the last release takes it out.
func TestClaimsAnIdenticalCtxloomEntryIsTakenOverNotFound(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, mcpTarget, `{"mcpServers": {"ctxloom": {"command": "ctxloom", "args": ["mcp"]}}}`, 0o644)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))
	mustCommit(t, c, fs, release(mcpTarget, project))
	assert.Empty(t, servers(t, fs))
}

// A release of a writer that claims nothing in a file touches nothing,
// whatever the file is.
func TestClaimsAReleaseWithNothingClaimedTouchesNothing(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, "/proj/notes.md", "mine\n", 0o644)
	c := newClaims(t, fs)
	got := mustCommit(t, c, fs, release("/proj/notes.md", project))
	assert.Equal(t, safefs.Committed{Unchanged: []string{"/proj/notes.md"}}, got)
}

// An unchanged whole-file claim over a file the user has since edited is left
// alone, so a redelivery does not refuse.
func TestClaimsAnUnchangedWholeFileClaimLeavesTheUsersEdit(t *testing.T) {
	const cmd = "/proj/.claude/commands/x.md"
	fs := afero.NewMemMapFs()
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(cmd, project, present.Claim{Value: []byte("body\n")}))
	testsupport.WriteFileString(t, fs, cmd, "edited\n", 0o644)
	mustCommit(t, c, fs, release(cmd, project), stage(cmd, project, present.Claim{Value: []byte("body\n")}))
	assert.Equal(t, "edited\n", read(t, fs, cmd))
}

// A user who deleted their own file does not get an empty one back from a
// release that has nothing left to take out of it.
func TestClaimsAReleaseDoesNotRecreateAFileTheUserDeleted(t *testing.T) {
	fs := withUserFile(t)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(mcpTarget, project, server("ctxloom", installEntry)))
	require.NoError(t, fs.Remove(mcpTarget))
	mustCommit(t, c, fs, release(mcpTarget, project))
	_, err := fs.Stat(mcpTarget)
	assert.True(t, os.IsNotExist(err))
}

// A record claiming a place inside a file hew cannot read (written by a build
// that read that format) is refused, not guessed at.
func TestClaimsRefuseARecordedPlaceInAFileHewCannotRead(t *testing.T) {
	const notes = "/proj/notes.md"
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, notes, "mine\n", 0o644)
	rec := "claims: 2\ntarget: " + notes + "\nseq: 1\npaths:\n  /x:\n    - writer: project\n      seq: 1\n      value: 1\n"
	testsupport.WriteFileString(t, fs, filepath.Join(claimsDir, confpatch.RecordPrefix(notes)+".claims.yaml"), rec, 0o600)
	c := newClaims(t, fs)
	_, err := commitOps(t, c, fs, release(notes, project))
	require.Error(t, err)
	assert.Equal(t, "mine\n", read(t, fs, notes))
}

const contextFile = "/proj/CLAUDE.md"

func section(text string) present.Claim {
	return present.Claim{Pointer: present.AppendedSection, Value: []byte(text)}
}

// An appended section follows the user's own text, a blank line between;
// restating it replaces it, and the last release leaves the user's text.
func TestClaimsAnAppendedSectionFollowsTheUsersText(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, contextFile, "# mine\n", 0o644)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(contextFile, project, section("ctx A\n")))
	assert.Equal(t, "# mine\n\nctx A\n", read(t, fs, contextFile))
	mustCommit(t, c, fs, release(contextFile, project), stage(contextFile, project, section("ctx B")))
	assert.Equal(t, "# mine\n\nctx B\n", read(t, fs, contextFile))
	mustCommit(t, c, fs, release(contextFile, project))
	assert.Equal(t, "# mine\n", read(t, fs, contextFile))
}

func TestClaimsASectionCreatesItsFileAndLeavesWithIt(t *testing.T) {
	fs := afero.NewMemMapFs()
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(contextFile, project, section("ctx\n")))
	assert.Equal(t, "ctx\n", read(t, fs, contextFile))
	mustCommit(t, c, fs, release(contextFile, project))
	_, err := fs.Stat(contextFile)
	assert.True(t, os.IsNotExist(err))
}

func TestClaimsTheSessionsSectionWins(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, contextFile, "# mine\n", 0o644)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(contextFile, session, section("session ctx\n")))
	mustCommit(t, c, fs, stage(contextFile, project, section("project ctx\n")))
	assert.Equal(t, "# mine\n\nsession ctx\n", read(t, fs, contextFile))
	mustCommit(t, c, fs, release(contextFile, session))
	assert.Equal(t, "# mine\n\nproject ctx\n", read(t, fs, contextFile))
}

// Text the user wrote AFTER ctxloom's section means the section is no longer
// at the end ctxloom put it: refused, not cut out of the middle.
func TestClaimsRefuseToReleaseASectionTheUserWroteAfter(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, contextFile, "# mine\n", 0o644)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(contextFile, project, section("ctx\n")))
	testsupport.WriteFileString(t, fs, contextFile, "# mine\n\nctx\n\nmore of mine\n", 0o644)
	_, err := commitOps(t, c, fs, release(contextFile, project))
	require.ErrorIs(t, err, ErrNotOurs)
	assert.Equal(t, "# mine\n\nctx\n\nmore of mine\n", read(t, fs, contextFile))
}

const settingsTarget = "/proj/.claude/settings.json"

func element(pointer string, v any) present.Claim {
	return present.Claim{Pointer: pointer + "/-", Value: v}
}

func settingsDoc(t *testing.T, fs afero.Fs) map[string]any {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(read(t, fs, settingsTarget)), &doc))
	return doc
}

func deny(t *testing.T, fs afero.Fs) []any {
	t.Helper()
	perms, _ := settingsDoc(t, fs)["permissions"].(map[string]any)
	list, _ := perms["deny"].([]any)
	return list
}

const userSettings = "{\n  \"permissions\": {\n    \"deny\": [\"Read(./.env)\"]\n  }\n}\n"

// An element is identified by its value: two writers claiming the same one
// share it, it is in the array once, and it leaves with the last of them.
func TestClaimsAnElementIsSharedAndLeavesWithItsLastWriter(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, settingsTarget, userSettings, 0o644)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(settingsTarget, project, element("/permissions/deny", "Task")))
	mustCommit(t, c, fs, stage(settingsTarget, session, element("/permissions/deny", "Task")))
	assert.Equal(t, []any{"Read(./.env)", "Task"}, deny(t, fs))
	mustCommit(t, c, fs, release(settingsTarget, project))
	assert.Equal(t, []any{"Read(./.env)", "Task"}, deny(t, fs))
	mustCommit(t, c, fs, release(settingsTarget, session))
	assert.Equal(t, userSettings, read(t, fs, settingsTarget))
}

// Two writers' different elements in one array are independent.
func TestClaimsDifferentElementsAreIndependent(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, settingsTarget, userSettings, 0o644)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(settingsTarget, project, element("/permissions/deny", "Task")))
	mustCommit(t, c, fs, stage(settingsTarget, session, element("/permissions/deny", "WebFetch")))
	mustCommit(t, c, fs, release(settingsTarget, project))
	assert.Equal(t, []any{"Read(./.env)", "WebFetch"}, deny(t, fs))
}

// An element the user already has is theirs: claimed alongside, never taken.
func TestClaimsAnElementTheUserHadIsFoundAndKept(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, settingsTarget, userSettings, 0o644)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(settingsTarget, project, element("/permissions/deny", "Read(./.env)")))
	assert.Equal(t, userSettings, read(t, fs, settingsTarget))
	mustCommit(t, c, fs, release(settingsTarget, project))
	assert.Equal(t, userSettings, read(t, fs, settingsTarget))
}

var hookGroup = map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": "ctxloom", "args": []any{"hook", "tool-reflect"}}}}

// An element whose array is missing creates it, and the containers it made
// leave with it: the file comes back exactly.
func TestClaimsAnElementCreatesItsArrayAndPrunesIt(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, settingsTarget, "{\n  \"model\": \"opus\"\n}\n", 0o644)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(settingsTarget, project, element("/hooks/PreToolUse", hookGroup)))
	hooks := settingsDoc(t, fs)["hooks"].(map[string]any)
	assert.Equal(t, []any{hookGroup}, hooks["PreToolUse"])
	mustCommit(t, c, fs, release(settingsTarget, project))
	assert.Equal(t, "{\n  \"model\": \"opus\"\n}\n", read(t, fs, settingsTarget))
}

// A hook group an install from before the record left, every hook in it
// running ctxloom, is ctxloom's own: taken over, and taken out on release.
func TestClaimsAHookGroupThatRunsCtxloomIsTakenOver(t *testing.T) {
	fs := afero.NewMemMapFs()
	g, err := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{hookGroup}}})
	require.NoError(t, err)
	testsupport.WriteFileString(t, fs, settingsTarget, string(g), 0o644)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(settingsTarget, project, element("/hooks/PreToolUse", hookGroup)))
	mustCommit(t, c, fs, release(settingsTarget, project))
	hooks, _ := settingsDoc(t, fs)["hooks"].(map[string]any)
	assert.Empty(t, hooks["PreToolUse"])
}

// A claimed element the user deleted comes back on the next delivery.
func TestClaimsALostElementIsPutBack(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, settingsTarget, userSettings, 0o644)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(settingsTarget, project, element("/permissions/deny", "Task")))
	testsupport.WriteFileString(t, fs, settingsTarget, userSettings, 0o644)
	mustCommit(t, c, fs, release(settingsTarget, project), stage(settingsTarget, project, element("/permissions/deny", "Task")))
	assert.Equal(t, []any{"Read(./.env)", "Task"}, deny(t, fs))
}

func TestClaimsPathsOfAnElement(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, settingsTarget, userSettings, 0o644)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(settingsTarget, project, element("/permissions/deny", "Task")))
	got, err := c.Paths(fs, settingsTarget)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "/permissions/deny/-", got[0].Pointer)
	assert.True(t, got[0].Live)
	testsupport.WriteFileString(t, fs, settingsTarget, userSettings, 0o644)
	got, err = c.Paths(fs, settingsTarget)
	require.NoError(t, err)
	assert.False(t, got[0].Live)
}

func TestClaimsStageRefusesMalformedSectionsAndElements(t *testing.T) {
	fs := afero.NewMemMapFs()
	c := newClaims(t, fs)
	s := c.In(safefs.NewBatch(fs, noLock))
	assert.Error(t, s.Stage(contextFile, project, []present.Claim{{Pointer: present.AppendedSection, Value: "text"}}), "a section's value is its bytes")
	assert.Error(t, s.Stage(contextFile, project, []present.Claim{section("a"), {Value: []byte("b")}}), "a section beside a whole file")
	assert.Error(t, s.Stage(settingsTarget, project, []present.Claim{element("/permissions/deny", "Task"), element("/permissions/deny", "Task")}), "one element twice")
	assert.NoError(t, s.Stage(settingsTarget, project, []present.Claim{element("/permissions/deny", "Task"), element("/permissions/deny", "Web")}), "two elements of one array")
}

// A member whose pointer passes through a key named "-" is a member, not an
// element: only the record's own element keys read as elements.
func TestClaimsAKeyNamedDashIsAMember(t *testing.T) {
	fs := afero.NewMemMapFs()
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(settingsTarget, project, present.Claim{Pointer: "/odd/-/name", Value: 1}))
	odd := settingsDoc(t, fs)["odd"].(map[string]any)
	assert.Equal(t, map[string]any{"name": float64(1)}, odd["-"])
}

// Two writers claiming one file in different ways (whole and by section) is
// refused rather than guessed between.
func TestClaimsRefuseWritersClaimingAFileInDifferentWays(t *testing.T) {
	fs := afero.NewMemMapFs()
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(contextFile, project, present.Claim{Value: []byte("whole\n")}))
	_, err := commitOps(t, c, fs, stage(contextFile, session, section("sec\n")))
	require.Error(t, err)
	assert.Equal(t, "whole\n", read(t, fs, contextFile))
}

// An unchanged section over a file the user has since edited is left alone,
// so a redelivery does not refuse.
func TestClaimsAnUnchangedSectionLeavesTheUsersEdit(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, contextFile, "# mine\n", 0o644)
	c := newClaims(t, fs)
	mustCommit(t, c, fs, stage(contextFile, project, section("ctx\n")))
	testsupport.WriteFileString(t, fs, contextFile, "# mine\n\nctx\n\nmore\n", 0o644)
	mustCommit(t, c, fs, release(contextFile, project), stage(contextFile, project, section("ctx\n")))
	assert.Equal(t, "# mine\n\nctx\n\nmore\n", read(t, fs, contextFile))
}

// An element's array that the user has turned into something else is theirs.
func TestClaimsRefuseAnElementWhoseArrayIsNotOne(t *testing.T) {
	fs := afero.NewMemMapFs()
	testsupport.WriteFileString(t, fs, settingsTarget, `{"permissions": {"deny": "everything"}}`, 0o644)
	c := newClaims(t, fs)
	_, err := commitOps(t, c, fs, stage(settingsTarget, project, element("/permissions/deny", "Task")))
	require.ErrorIs(t, err, ErrNotOurs)
}

// A hook group that runs ANOTHER tool, already there, is that tool's or the
// user's — ctxloom delivers companions' hooks too — so it is found and kept.
func TestClaimsAHookGroupRunningAnotherToolIsFoundAndKept(t *testing.T) {
	for name, group := range map[string]map[string]any{
		"another tool":           {"matcher": "Bash", "hooks": []any{map[string]any{"type": "command", "command": "ltk", "args": []any{"evaluate"}}}},
		"hooks that are no list": {"matcher": "Bash", "hooks": "ctxloom"},
	} {
		t.Run(name, func(t *testing.T) {
			fs := afero.NewMemMapFs()
			g, err := json.Marshal(map[string]any{"hooks": map[string]any{"PreToolUse": []any{group}}})
			require.NoError(t, err)
			testsupport.WriteFileString(t, fs, settingsTarget, string(g), 0o644)
			c := newClaims(t, fs)
			mustCommit(t, c, fs, stage(settingsTarget, project, element("/hooks/PreToolUse", group)))
			mustCommit(t, c, fs, release(settingsTarget, project))
			assert.Equal(t, string(g), read(t, fs, settingsTarget))
		})
	}
}
