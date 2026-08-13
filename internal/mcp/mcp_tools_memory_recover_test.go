package mcp

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// claudeVendorFixture is the real claude-code transcript the vendorreader
// package's own suite exercises, reused rather than hand-rolled so a conversion
// that only works on a synthetic stub cannot pass here.
func claudeVendorFixture() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "transcript", "vendorreader", "claude", "testdata", "transcript-fixture.jsonl")
}

// TestLoadOrDistillSession_ConvertsVendorTranscriptOnDemand is the claim the
// whole self-heal exists for: a session whose transcript was never converted is
// RECOVERED, not refused.
//
// An interactive session has no live tee — the structured capture path cannot
// reach a pty — so the engine's own store is the only record, and reading it
// back used to be the user's job. Recovery answered "run `ctxloom session
// backfill <harp>` and try again", which is a correct instruction delivered at
// the one moment its reader has just lost the context needed to act on it.
//
// The session is addressed by a backend-native UUID, not by its harp, because
// that is the production shape: a harp-keyed id makes the write key and the
// read key coincide by accident and the test would pass no matter which side
// used which.
func TestLoadOrDistillSession_ConvertsVendorTranscriptOnDemand(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)
	projectDir := t.TempDir()
	entry, err := mgr.AssignHarp(projectDir, "claude-code")
	require.NoError(t, err)
	harp := entry.HarpName

	const vendorSessionID = "8f1d1f2e-6c40-4a71-9d2c-0b6f5a3e7c11"
	require.NotEqual(t, harp, vendorSessionID, "the ids must differ or this test proves nothing")
	require.NoError(t, mgr.BindSession(harp, vendorSessionID, claudeVendorFixture()))
	// Adapter selection REFUSES a session recording no engine version rather
	// than guessing a newest reader (vendorreader/version.go), so an index entry
	// without one converts nothing — which is a different failure than the one
	// under test here.
	require.NoError(t, mgr.RecordEngineVersion(harp, "2.1.225"))

	appDir := filepath.Join(projectDir, ".ctxloom")
	s := &ctxServer{
		cfg:              config.NewFixture(config.Fixture{AppDir: appDir}),
		compactorFactory: fixedCompactor(vendorSessionID, "Distilled: recovered without a manual backfill."),
	}

	_, out, err := s.loadOrDistillSession(context.Background(), vendorSessionID, "claude-code", "", policyLive)
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.True(t, out.Loaded, "the vendor transcript was there to be converted; recovery must not refuse it")
	assert.Contains(t, out.Content, "recovered without a manual backfill",
		"the distilled essence must come back, not an empty success envelope")
}

// TestLoadOrDistillSession_LiveRefreshesAnAlreadyConvertedTranscript is the
// SECOND-/recover case, and it is the one that fails silently.
//
// The first recovery in a session converts its transcript as of that moment.
// Every later one then finds a canonical transcript already present — so a
// self-heal that only fires when the transcript is MISSING reads a session
// frozen at the first recovery: complete-looking, confidently missing
// everything that happened since, and indistinguishable from a working
// recovery by anything downstream.
//
// The assertion is on the canonical transcript's own bytes rather than the
// returned essence, because the essence here comes from a fixed compactor: it
// would read identically whether or not the transcript underneath it had been
// refreshed, which is exactly the kind of assertion that proves nothing.
func TestLoadOrDistillSession_LiveRefreshesAnAlreadyConvertedTranscript(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)
	projectDir := t.TempDir()
	entry, err := mgr.AssignHarp(projectDir, "claude-code")
	require.NoError(t, err)
	harp := entry.HarpName

	// A writable copy, truncated: the session has not finished yet.
	full, err := os.ReadFile(claudeVendorFixture())
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(full), "\n"), "\n")
	require.Greater(t, len(lines), 4)
	vendorPath := filepath.Join(t.TempDir(), "live-transcript.jsonl")
	// Half: enough turns that the first recovery is a real one (a transcript
	// with no entries trips the empty-session guard and would make this test
	// pass or fail for the wrong reason), with the rest still to come.
	require.NoError(t, os.WriteFile(vendorPath, []byte(strings.Join(lines[:len(lines)/2], "\n")+"\n"), 0o644))

	const vendorSessionID = "3c2a5f90-11ab-4c7e-8f31-2d9e6b04a7f2"
	require.NoError(t, mgr.BindSession(harp, vendorSessionID, vendorPath))
	require.NoError(t, mgr.RecordEngineVersion(harp, "2.1.225"))

	s := &ctxServer{
		cfg:              config.NewFixture(config.Fixture{AppDir: filepath.Join(projectDir, ".ctxloom")}),
		compactorFactory: fixedCompactor(vendorSessionID, "Distilled: first look."),
	}

	_, first, err := s.loadOrDistillSession(context.Background(), vendorSessionID, "claude-code", "", policyLive)
	require.NoError(t, err)
	require.True(t, first.Loaded)
	canonPath, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	afterFirst, err := os.ReadFile(canonPath)
	require.NoError(t, err)

	// The session keeps going while its context is being recovered.
	require.NoError(t, os.WriteFile(vendorPath, full, 0o644))

	_, second, err := s.loadOrDistillSession(context.Background(), vendorSessionID, "claude-code", "", policyLive)
	require.NoError(t, err)
	require.True(t, second.Loaded)
	afterSecond, err := os.ReadFile(canonPath)
	require.NoError(t, err)

	assert.Greater(t, len(afterSecond), len(afterFirst),
		"a live recovery must re-read the engine's store, not trust the transcript its own earlier run wrote")
	assert.Contains(t, string(afterSecond), "[Request interrupted by user for tool use]",
		"the turns added since the first recovery must be in the transcript the second one reads")
}

// TestLoadOrDistillSession_LiveRefreshesWhenAddressedByHarp is the same live
// refresh as above, reached by the OTHER spelling of the same session — and it
// did not happen at all.
//
// The refresh was gated on operations.HarpForSession, which matches on
// Entry.SessionID and so only ever resolves a backend-native id. A harp
// arriving instead resolved to "", the gate closed, and the whole pre-read
// refresh was skipped in silence. That is not an exotic call shape: it is what
// the callers actually hand over, since transcript.CanonicalHistory.ListSessions
// sets meta.ID to the harp and recover's own mtime fallback picks its target
// out of exactly that list.
//
// Asserted on the canonical transcript's bytes, never on out.WasCached: the
// failure mode here is a recovery that reports success while serving a
// transcript nobody re-read, so the tool's own account of what it did is the
// one thing that cannot be used as evidence.
func TestLoadOrDistillSession_LiveRefreshesWhenAddressedByHarp(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)
	projectDir := t.TempDir()
	entry, err := mgr.AssignHarp(projectDir, "claude-code")
	require.NoError(t, err)
	harp := entry.HarpName

	full, err := os.ReadFile(claudeVendorFixture())
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(full), "\n"), "\n")
	require.Greater(t, len(lines), 4)
	vendorPath := filepath.Join(t.TempDir(), "live-transcript.jsonl")
	require.NoError(t, os.WriteFile(vendorPath, []byte(strings.Join(lines[:len(lines)/2], "\n")+"\n"), 0o644))

	const vendorSessionID = "9b7e1c04-2f55-4d8a-a0c6-7e3b1d92f4aa"
	require.NotEqual(t, harp, vendorSessionID)
	require.NoError(t, mgr.BindSession(harp, vendorSessionID, vendorPath))
	require.NoError(t, mgr.RecordEngineVersion(harp, "2.1.225"))

	s := &ctxServer{
		cfg:              config.NewFixture(config.Fixture{AppDir: filepath.Join(projectDir, ".ctxloom")}),
		compactorFactory: fixedCompactor(harp, "Distilled: first look."),
	}

	// Addressed by HARP throughout — the spelling the gate could not resolve.
	_, first, err := s.loadOrDistillSession(context.Background(), harp, "claude-code", "", policyLive)
	require.NoError(t, err)
	require.True(t, first.Loaded)
	canonPath, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	afterFirst, err := os.ReadFile(canonPath)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(vendorPath, full, 0o644))

	_, second, err := s.loadOrDistillSession(context.Background(), harp, "claude-code", "", policyLive)
	require.NoError(t, err)
	require.True(t, second.Loaded)
	afterSecond, err := os.ReadFile(canonPath)
	require.NoError(t, err)

	assert.Greater(t, len(afterSecond), len(afterFirst),
		"a harp-addressed live recovery must re-read the engine's store too, not just a UUID-addressed one")
	assert.Contains(t, string(afterSecond), "[Request interrupted by user for tool use]",
		"the turns added since the first recovery must reach the transcript the second one reads")
}

// TestLoadOrDistillSession_FailedLiveRefreshDoesNotServeTheCache closes the
// silent-staleness hole the refresh left open.
//
// A live recovery re-reads the engine's store before loading. When that
// re-read FAILS, the canonical transcript on disk is whatever the last
// successful refresh left behind — so its byte size still matches the size
// stamped into the cached essence, the staleness check reports "hasn't moved",
// and recovery hands back that essence with was_cached=true. Every part of
// that is a true statement about a file this call could not bring up to date,
// and none of it is a true statement about the session.
//
// The assertion is that a SECOND distillation actually ran (the swapped-in
// compactor's text comes back), not that was_cached went false: the bug is a
// tool misreporting its own freshness, so its self-report cannot be the proof.
func TestLoadOrDistillSession_FailedLiveRefreshDoesNotServeTheCache(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)
	projectDir := t.TempDir()
	entry, err := mgr.AssignHarp(projectDir, "claude-code")
	require.NoError(t, err)
	harp := entry.HarpName

	const vendorSessionID = "5d0e8b31-7c94-4a12-b8e5-6f1a2c9d3e70"
	require.NoError(t, mgr.BindSession(harp, vendorSessionID, claudeVendorFixture()))
	require.NoError(t, mgr.RecordEngineVersion(harp, "2.1.225"))

	s := &ctxServer{
		cfg:              config.NewFixture(config.Fixture{AppDir: filepath.Join(projectDir, ".ctxloom")}),
		compactorFactory: fixedCompactor(vendorSessionID, "Distilled: first look."),
	}

	_, first, err := s.loadOrDistillSession(context.Background(), vendorSessionID, "claude-code", "", policyLive)
	require.NoError(t, err)
	require.True(t, first.Loaded)
	require.Contains(t, first.Content, "first look")

	// Break the refresh, and ONLY the refresh: the conversion writes through a
	// temp sibling inside the harp's persist dir, so a read-only dir fails the
	// re-read while leaving the already-captured canonical transcript perfectly
	// readable — exactly the state that makes the stale cache look current.
	canonPath, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	persistDir := filepath.Dir(canonPath)
	require.NoError(t, os.Chmod(persistDir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(persistDir, 0o700) })

	s.compactorFactory = fixedCompactor(vendorSessionID, "Distilled: second look.")

	_, second, err := s.loadOrDistillSession(context.Background(), vendorSessionID, "claude-code", "", policyLive)
	require.NoError(t, err)
	require.True(t, second.Loaded)
	assert.Contains(t, second.Content, "second look",
		"a recovery whose live re-read failed must re-distill, not serve an essence it cannot vouch for")
}

// TestSessionHarpForID pins the resolver both live recovery and the staleness
// fingerprint depend on: it must answer for a harp AND for the backend-native
// session id bound to it, because grpc.CanonicalFallbackSource.GetSession
// accepts both and every lookup standing beside it has to agree.
func TestSessionHarpForID(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)
	// The project must be the working directory: operations.HarpForSession
	// resolves the backend-native spelling through the PROJECT-SCOPED listing,
	// while the harp spelling resolves through an unscoped Find. That asymmetry
	// is real and outlives this test — see the note on sessionHarpForID.
	cwd, err := os.Getwd()
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(cwd, "claude-code")
	require.NoError(t, err)
	harp := entry.HarpName

	// A transcript that EXISTS: operations.ListSessions drops an entry whose
	// bound transcript is gone and which has no essence (isUnrecoverable), and
	// a dropped entry resolves to no harp for a reason that has nothing to do
	// with the spelling under test.
	vendorPath := filepath.Join(t.TempDir(), "live.jsonl")
	require.NoError(t, os.WriteFile(vendorPath, []byte("{}\n"), 0o644))

	const vendorSessionID = "1a4c9d77-8e21-4b6f-95c3-0af2e6b81d34"
	require.NoError(t, mgr.BindSession(harp, vendorSessionID, vendorPath))

	assert.Equal(t, harp, sessionHarpForID(harp),
		"a harp is what CanonicalHistory.ListSessions and `memory list` hand back; it must resolve")
	assert.Equal(t, harp, sessionHarpForID(vendorSessionID),
		"the backend-native spelling must keep resolving")
	assert.Empty(t, sessionHarpForID("neither-a-harp-nor-a-bound-id"))
	assert.Empty(t, sessionHarpForID(""))
}

// TestSessionHarpForID_ResolvesRotatedAwaySessionID pins that sessionHarpForID
// resolves a session id a /clear has since rotated PAST — no longer the
// harp's current SessionID, but preserved in sessions.Entry.Rotations —
// through operations.HarpForSession's lineage-aware lookup
// (sessions.Store.FindBySessionID). A stale hook payload, an old vendor
// transcript, or a caller's own memory of "the session before the clear"
// names exactly this id; without the lineage lookup it resolved to nothing at
// all once BindSession had re-pointed the entry past it.
func TestSessionHarpForID_ResolvesRotatedAwaySessionID(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(cwd, "claude-code")
	require.NoError(t, err)
	harp := entry.HarpName

	require.NoError(t, mgr.BindSession(harp, "pre-clear-id", "/pre-clear.jsonl"))
	require.NoError(t, mgr.BindSession(harp, "post-clear-id", "/post-clear.jsonl"))

	assert.Equal(t, harp, sessionHarpForID("pre-clear-id"),
		"a session id rotated away by a /clear must still resolve to its harp")
	assert.Equal(t, harp, sessionHarpForID("post-clear-id"),
		"the current binding must still resolve too")
}

// TestLoadOrDistillSession_ArchivedAlsoConvertsOnDemand covers the SIBLING
// tools — load_session and get_previous_session — which hit the same wall as
// /recover: an interactive session they were asked to open has no canonical
// transcript until someone reads the engine's store back.
//
// Written after a surviving mutation. Deleting the on-demand conversion from
// the not-captured branch changed nothing that any test could see, because the
// live policy reaches that branch only after its own pre-read refresh has
// already converted the transcript. The branch exists FOR the archived policy,
// and nothing was exercising it — so the sibling half of the fix was shipping
// unproven while the recover half looked covered.
func TestLoadOrDistillSession_ArchivedAlsoConvertsOnDemand(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)
	projectDir := t.TempDir()
	entry, err := mgr.AssignHarp(projectDir, "claude-code")
	require.NoError(t, err)
	harp := entry.HarpName

	const vendorSessionID = "5e08c3d1-42b7-4f96-a0e8-1c7b93de4405"
	require.NoError(t, mgr.BindSession(harp, vendorSessionID, claudeVendorFixture()))
	require.NoError(t, mgr.RecordEngineVersion(harp, "2.1.225"))

	s := &ctxServer{
		cfg:              config.NewFixture(config.Fixture{AppDir: filepath.Join(projectDir, ".ctxloom")}),
		compactorFactory: fixedCompactor(vendorSessionID, "Distilled: a prior session opened without a manual backfill."),
	}

	_, out, err := s.loadOrDistillSession(context.Background(), vendorSessionID, "claude-code", "", policyArchived)
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.True(t, out.Loaded, "opening a prior interactive session must convert its transcript, not refuse it")
	assert.Contains(t, out.Content, "opened without a manual backfill")
}

// TestLoadOrDistillSession_ArchivedDoesNotRewriteAnExistingTranscript is the
// other side of the policy, and the reason it is a policy rather than an
// always-on refresh: a finished session's transcript cannot have changed, so
// re-converting it spends a full read and rewrite to reproduce bytes that are
// already correct. load_session and get_previous_session must not pay it.
func TestLoadOrDistillSession_ArchivedDoesNotRewriteAnExistingTranscript(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)
	projectDir := t.TempDir()
	entry, err := mgr.AssignHarp(projectDir, "claude-code")
	require.NoError(t, err)
	harp := entry.HarpName

	const vendorSessionID = "b71e0c48-9d3f-4a20-bb15-70c8e2f61d93"
	require.NoError(t, mgr.BindSession(harp, vendorSessionID, claudeVendorFixture()))
	require.NoError(t, mgr.RecordEngineVersion(harp, "2.1.225"))

	canonPath, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(canonPath), 0o755))
	require.NoError(t, os.WriteFile(canonPath, canonicalRecords(harp, vendorSessionID), 0o644))
	before, err := os.ReadFile(canonPath)
	require.NoError(t, err)

	s := &ctxServer{
		cfg:              config.NewFixture(config.Fixture{AppDir: filepath.Join(projectDir, ".ctxloom")}),
		compactorFactory: fixedCompactor(vendorSessionID, "Distilled: archived."),
	}

	_, out, err := s.loadOrDistillSession(context.Background(), vendorSessionID, "claude-code", "", policyArchived)
	require.NoError(t, err)
	require.True(t, out.Loaded)

	after, err := os.ReadFile(canonPath)
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after),
		"an archived load must leave the captured transcript exactly as it found it")
}

// TestLoadOrDistillSession_NoCaptureMessageDoesNotSendTheUserBackToBackfill
// pins what the degraded message may and may not say once conversion is
// automatic.
//
// It must still name the harp — a caller holding only a UUID cannot act on
// anything else. It must NOT name `session backfill`: that is precisely the
// conversion just attempted on the caller's behalf, so offering it as the
// remedy sends someone to re-run the thing that has already failed. This
// assertion is the guard against that advice creeping back the next time
// somebody improves the error text.
//
// The fixture binds a session id with no locatable vendor transcript at all,
// which is the genuine "nothing anyone can do here" case.
func TestLoadOrDistillSession_NoCaptureMessageDoesNotSendTheUserBackToBackfill(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(t.TempDir(), "claude-code")
	require.NoError(t, err)
	harp := entry.HarpName

	const fakeSessionID = "d9c76e71-cbfe-41e2-b0f3-4a7d440deec1"
	require.NoError(t, mgr.BindSession(harp, fakeSessionID, ""))

	s := &ctxServer{cfg: config.NewFixture(config.Fixture{AppDir: filepath.Join(t.TempDir(), ".ctxloom")})}

	_, out, err := s.loadOrDistillSession(context.Background(), fakeSessionID, "claude-code", "", policyLive)
	require.NoError(t, err, "recovery must never block the agent (CLAUDE.md) — this degrades to a usable message, not a tool error")
	require.NotNil(t, out)
	assert.False(t, out.Loaded)
	assert.Contains(t, out.Message, harp, "message must name the harp")
	assert.NotContains(t, out.Message, "session backfill",
		"conversion is automatic now; telling the user to run the command that just ran is not a remedy")
}
