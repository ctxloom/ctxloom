package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
	mockreader "github.com/ctxloom/ctxloom/internal/transcript/vendorreader/mock"
)

// Shared fixtures for the `session` noun's CLI tests. They live in their own
// file because several session test files drive the same cobra tree against
// the same on-disk harp layout, and a helper parked in whichever test file
// happened to need it first ties every other file's fate to that one.

// execRootCmd runs the real cobra command tree exactly as a shell invocation
// would (rootCmd.SetArgs + Execute), capturing stdout into a fresh buffer and
// restoring rootCmd's IO/args afterward.
func execRootCmd(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&bytes.Buffer{})
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	err = rootCmd.Execute()
	return out.String(), err
}

// execRootCmdBoth is execRootCmd with the diagnostic channel captured too. A
// destructive leaf's report-only refusal is written to STDERR (the loud "I
// removed nothing" line), so a test that reads only stdout cannot tell a
// command that reported from one that silently did nothing.
func execRootCmdBoth(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errBuf bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errBuf)
	rootCmd.SetArgs(args)
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})
	err = rootCmd.Execute()
	return out.String(), errBuf.String(), err
}

// pinnedEngineVersion is the version a seeded session claims to have run under,
// per engine. Required, not decorative: reader selection is
// (engine, RECORDED version) -> adapter, and a session carrying no
// engine_version REFUSES to be read at all rather than guess at a format
// (vendorreader.SelectAdapter). Seeding it is what makes these fixtures fail —
// or pass — for the reason the test names instead of for a missing version.
//
// The value is DERIVED per backend from wherever that backend declares it —
// never re-typed here. A hand-copied table is an unchecked binding: it agrees
// with the source on the day it is written and silently disagrees forever
// after, and a seeded session would then claim a version no reader is
// validated against while the test still passed.
//
// The two sources differ because the two backends differ, which is the point:
//   - a REAL engine's pin lives in .github/engine-versions.env, the lock CI
//     drift-checks against the vendor's published release feed.
//   - mock has no vendor and no feed, so it declares its own validated version
//     in its adapter, and that declaration is the authority.
func pinnedEngineVersion(t *testing.T, backend string) (string, bool) {
	t.Helper()
	switch backend {
	case config.BackendMock:
		require.NotEmpty(t, mockreader.VersionedAdapters, "mock declares no versioned adapter")
		return mockreader.VersionedAdapters[0].ValidatedVersion, true
	case config.BackendClaudeCode:
		return enginePinFromLock(t, "CLAUDE_CODE_CLI_VERSION"), true
	}
	return "", false
}

// enginePinFromLock reads one KEY=value out of .github/engine-versions.env.
func enginePinFromLock(t *testing.T, key string) string {
	t.Helper()
	repoRoot, err := sourcedir.RepoRoot()
	require.NoError(t, err)
	raw, err := os.ReadFile(filepath.Join(repoRoot, ".github", "engine-versions.env"))
	require.NoError(t, err, "read .github/engine-versions.env")

	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, found := strings.Cut(line, "="); found && k == key {
			require.NotEmpty(t, v, "%s is present but empty in the lock", key)
			return v
		}
	}
	t.Fatalf("%s is not pinned in .github/engine-versions.env", key)
	return ""
}

// seedHookSession mints an indexed session for backend and puts its harp in
// the environment, which is where a turn-boundary hook looks to learn WHICH
// engine wrote the transcript it is about to parse
// (operations.ResolveTurnTranscript).
//
// Required by every turn-boundary fixture, not decorative: with no indexed
// session there is no engine to select a reader for, and the hook takes its
// fail-safe exit before reaching the guard the test names — which is a
// fixture passing, or failing, for a reason its name does not mention.
//
// The caller isolates HOME first; this deliberately does not, so a test that
// forgot to fails loudly against a missing index rather than quietly writing
// into the real one.
func seedHookSession(t *testing.T, backend string) string {
	t.Helper()
	mgr, err := sessions.Open()
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(t.TempDir(), backend)
	require.NoError(t, err)
	seedEngineVersion(t, mgr, entry.HarpName, backend)
	t.Setenv(agent.SessionHarpEnv, entry.HarpName)
	return entry.HarpName
}

// seedEngineVersion records the pinned version for harp, so a fixture exercises
// the behaviour it was written for rather than the unknown-version refusal.
func seedEngineVersion(t *testing.T, mgr *sessions.Manager, harp, backend string) {
	t.Helper()
	v, ok := pinnedEngineVersion(t, backend)
	require.True(t, ok, "no pinned engine version for backend %q", backend)
	require.NoError(t, mgr.RecordEngineVersion(harp, v))
}

// seedEndedSession mints a harp under the isolated HOME and marks it ended.
// Every destructive leaf refuses a session with no ended_at — destroying what
// a live agent is still writing to is the one thing none of them may do — so a
// fixture for a destroyer has to be an ENDED session or it proves only that
// the liveness guard fires.
func seedEndedSession(t *testing.T, projectDir, backend string) (*sessions.Manager, string) {
	t.Helper()
	mgr, err := sessions.Open()
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(projectDir, backend)
	require.NoError(t, err)
	require.NoError(t, mgr.MarkEnded(entry.HarpName, time.Now().UTC()))
	// A FREE lock file, the way a session that ended however it ended leaves
	// one: every destroyer refuses a harp whose lock cannot prove its owner
	// dead, and a fixture with no lock at all reads as unprovable.
	require.NoError(t, sessionlock.Hold(entry.HarpName))
	sessionlock.Release(entry.HarpName)
	return mgr, entry.HarpName
}

// seedTranscript writes a canonical transcript for harp and returns its path.
// The bytes are a real JSONL line rather than a placeholder: a purge reports
// the byte count it freed, and a zero-byte fixture cannot tell "freed the
// file" from "freed nothing".
func seedTranscript(t *testing.T, harp string) string {
	t.Helper()
	path, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(`{"type":"user","content":"hello"}`+"\n"), 0o644))
	return path
}

// seedEssence writes a distilled essence for harp and returns its path.
func seedEssence(t *testing.T, harp string) string {
	t.Helper()
	path, err := paths.HarpEssencePath(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("# Seeded essence\n"), 0o644))
	return path
}

// seedAuthoredNote writes a file a HUMAN is taken to have put in the harp
// directory. Nothing under `session` may ever destroy it, so every destroyer
// test that claims to be safe needs one present to be safe ABOUT.
func seedAuthoredNote(t *testing.T, harp, name string) string {
	t.Helper()
	dir, err := paths.HarpDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte("# notes nobody filed\n"), 0o644))
	return path
}

// onDisk answers the question both sides of every destructive test ask, in
// opposite directions: the report side asserts true afterwards, the apply
// side asserts false. A stat that fails for any reason OTHER than absence
// fails the test rather than reporting "gone" — an unanswerable stat would
// otherwise pass the apply side for the wrong reason.
func onDisk(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	require.True(t, os.IsNotExist(err), "stat %s: %v", path, err)
	return false
}
