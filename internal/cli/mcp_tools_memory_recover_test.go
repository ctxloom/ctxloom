package cli

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
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
