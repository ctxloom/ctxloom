package operations

import (
	"bufio"
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

// thisDir is this test file's own source directory, resolved via
// runtime.Caller rather than a relative literal: this package's TestMain
// (main_test.go) chdirs the whole test run into a throwaway sandbox dir
// (isolation from the package source dir as a working dir), so a
// cwd-relative fixture path would silently resolve to nothing there —
// mirrors codex_test.go's/kiro_test.go's own repoRoot helper.
func thisDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Dir(file)
}

// codexFixturePath, claudeFixturePath, antigravityFixturePath resolve to the
// REAL vendor-native fixture files each reader package's own test suite
// already exercises (internal/transcript/vendorreader/<engine>/testdata) — this
// suite reuses them rather than inventing a second, parallel set, per the
// project's fixture-reuse discipline (memory "silent-no-op-failure-mode":
// assert on real payload content, not a hand-rolled minimal stub that could
// never expose a real parse bug).
var (
	codexFixturePath       = filepath.Join(thisDir(), "..", "transcript", "vendorreader", "codex", "testdata", "rollout-fixture.jsonl")
	claudeFixturePath      = filepath.Join(thisDir(), "..", "transcript", "vendorreader", "claude", "testdata", "transcript-fixture.jsonl")
	antigravityFixturePath = filepath.Join(thisDir(), "..", "transcript", "vendorreader", "antigravity", "testdata", "transcript_full-fixture.jsonl")
)

// canonicalLines reads harp's canonical transcript.jsonl back and returns
// its non-empty lines, or nil if the file was never created.
func canonicalLines(t *testing.T, harp string) []string {
	t.Helper()
	p, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	f, err := os.Open(p)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if l := strings.TrimSpace(scanner.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	require.NoError(t, scanner.Err())
	return lines
}

func TestConvertVendorTranscript_ClaudeCodeBoundPath(t *testing.T) {
	testsupport.Isolate(t)
	harp := "convert-claude-harp"
	e := sessions.Entry{
		HarpName:       harp,
		Backend:        config.BackendClaudeCode,
		TranscriptPath: claudeFixturePath,
	}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	assert.True(t, converted)

	lines := canonicalLines(t, harp)
	require.NotEmpty(t, lines, "Convert should have produced canonical lines from a real fixture")
	// Every written record must carry the REGISTRY backend name
	// (config.BackendClaudeCode == "claude-code"), not the reader
	// package's own short test-fixture name ("claude") — see
	// vendorImportRegistry's doc comment for why: RecordOneshot and the live
	// structured-chat tee (lm/grpc/chat.go's openRecorder, keyed off the
	// plugin's own Info RPC name) both already stamp "claude-code" for this
	// harp/engine, and a mismatched Engine value on the SAME harp would be
	// a real, visible inconsistency to a transcript reader.
	for _, l := range lines {
		assert.Contains(t, l, `"engine":"claude-code"`)
	}
}

func TestConvertVendorTranscript_CodexBoundPath(t *testing.T) {
	testsupport.Isolate(t)
	harp := "convert-codex-harp"
	e := sessions.Entry{
		HarpName:       harp,
		Backend:        "codex",
		TranscriptPath: codexFixturePath,
	}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	assert.True(t, converted)
	assert.NotEmpty(t, canonicalLines(t, harp))
}

func TestConvertVendorTranscript_AntigravityBoundPath(t *testing.T) {
	testsupport.Isolate(t)
	harp := "convert-antigravity-harp"
	e := sessions.Entry{
		HarpName:       harp,
		Backend:        "antigravity",
		TranscriptPath: antigravityFixturePath,
	}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	assert.True(t, converted)
	assert.NotEmpty(t, canonicalLines(t, harp))
}

func TestConvertVendorTranscript_UnregisteredBackend(t *testing.T) {
	testsupport.Isolate(t)
	harp := "convert-unregistered-harp"
	e := sessions.Entry{
		HarpName:       harp,
		Backend:        "opencode", // opencode keeps its own native reader — no vendor-reader entry
		TranscriptPath: claudeFixturePath,
	}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	assert.False(t, converted)
	assert.Nil(t, canonicalLines(t, harp))
}

func TestConvertVendorTranscript_NoBoundTranscript(t *testing.T) {
	testsupport.Isolate(t)
	harp := "convert-no-transcript-harp"
	e := sessions.Entry{HarpName: harp, Backend: "codex"} // never bound

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	assert.False(t, converted)
	assert.Nil(t, canonicalLines(t, harp))
}

func TestConvertVendorTranscript_DanglingBoundPath(t *testing.T) {
	testsupport.Isolate(t)
	harp := "convert-dangling-harp"
	e := sessions.Entry{
		HarpName:       harp,
		Backend:        "codex",
		TranscriptPath: filepath.Join(t.TempDir(), "does-not-exist.jsonl"),
	}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	assert.False(t, converted, "a bind pointing at a since-removed file must degrade to not-found, not a hard failure")
}

// TestConvertVendorTranscript_Idempotent covers the exit-seam contract: once
// a canonical transcript exists for a harp, a second call is a permanent
// no-op for it — never a re-import (which would duplicate every entry, since
// Convert has no incremental/resume concept; see ConvertVendorTranscript's
// own doc comment).
func TestConvertVendorTranscript_Idempotent(t *testing.T) {
	testsupport.Isolate(t)
	harp := "convert-idempotent-harp"
	e := sessions.Entry{
		HarpName:       harp,
		Backend:        "codex",
		TranscriptPath: codexFixturePath,
	}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	require.True(t, converted)
	first := canonicalLines(t, harp)
	require.NotEmpty(t, first)

	converted, err = ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	assert.False(t, converted, "a harp that already has a canonical transcript must be skipped")
	assert.Equal(t, first, canonicalLines(t, harp), "the canonical file must be byte-for-byte untouched by the skipped second call")
}

// TestConvertVendorTranscript_SkipsWhenLegacyCanonicalExists pins the
// transcript.acp.jsonl -> transcript.jsonl rename's idempotency corollary: a
// harp that already has a PRE-RENAME (legacy-named) canonical transcript
// must be treated the same as one with a current-named transcript — skipped,
// not re-converted. Without the paths.ResolveHarpCanonicalTranscriptPath
// fallback in hasCanonicalTranscript, this would re-run Convert and produce
// a SECOND, current-named canonical file alongside the legacy one,
// duplicating the session's history across two files.
func TestConvertVendorTranscript_SkipsWhenLegacyCanonicalExists(t *testing.T) {
	testsupport.Isolate(t)
	harp := "convert-legacy-exists-harp"

	persistDir, err := paths.HarpPersistDir(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(persistDir, 0o755))
	legacyPath := filepath.Join(persistDir, "transcript.acp.jsonl")
	require.NoError(t, os.WriteFile(legacyPath, []byte(`{"v":1,"harp":"`+harp+`"}`+"\n"), 0o644))

	e := sessions.Entry{
		HarpName:       harp,
		Backend:        "codex",
		TranscriptPath: codexFixturePath,
	}
	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	assert.False(t, converted, "a harp with an existing legacy-named canonical transcript must be skipped")

	assert.Nil(t, canonicalLines(t, harp), "no current-named canonical file should have been created alongside the legacy one")
	legacyData, err := os.ReadFile(legacyPath)
	require.NoError(t, err)
	assert.Equal(t, `{"v":1,"harp":"`+harp+`"}`+"\n", string(legacyData), "the legacy file must be byte-for-byte untouched")
}

// TestConvertVendorTranscript_BestEffortOnFailure: a bind that STATS fine
// (locate succeeds) but is not actually a readable transcript file (a
// directory, standing in for "the bind pointed somewhere Convert's own
// os.Open/read genuinely can't handle") surfaces as a real error with
// converted=true — "tried and failed", distinct from every "nothing to do"
// case above. BackfillVendorTranscripts' own test covers that this failure
// never stops OTHER entries from converting.
func TestConvertVendorTranscript_BestEffortOnFailure(t *testing.T) {
	testsupport.Isolate(t)
	harp := "convert-failure-harp"
	e := sessions.Entry{
		HarpName:       harp,
		Backend:        "codex",
		TranscriptPath: t.TempDir(), // exists (os.Stat succeeds) but is not a file
	}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	assert.True(t, converted, "Convert was genuinely attempted")
	assert.Error(t, err)
}

func TestConvertVendorTranscript_EmptyHarp(t *testing.T) {
	testsupport.Isolate(t)
	e := sessions.Entry{Backend: "codex", TranscriptPath: codexFixturePath}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	assert.False(t, converted)
}

// TestVendorImportRegistry_CoversFourEngines locks in exactly which backend
// names carry a vendor reader — a change here (adding/removing an engine) should
// be a deliberate, visible edit to this test, not a silent registry drift.
func TestVendorImportRegistry_CoversFourEngines(t *testing.T) {
	got := make([]string, 0, len(vendorImportRegistry))
	for name := range vendorImportRegistry {
		got = append(got, name)
	}
	assert.ElementsMatch(t, []string{config.BackendClaudeCode, "codex", "antigravity", "kiro"}, got)
}

// TestLocateBoundTranscript exercises the shared locate func directly
// (rather than only indirectly through ConvertVendorTranscript above), so a
// future edit to its stat-and-degrade behavior is caught at the unit it
// actually lives in.
func TestLocateBoundTranscript(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "transcript.jsonl")
	require.NoError(t, os.WriteFile(real, []byte("{}\n"), 0o644))

	src, ok := locateBoundTranscript(context.Background(), sessions.Entry{TranscriptPath: real})
	assert.True(t, ok)
	assert.Equal(t, real, src)

	_, ok = locateBoundTranscript(context.Background(), sessions.Entry{})
	assert.False(t, ok, "an unbound entry has nothing to locate")

	_, ok = locateBoundTranscript(context.Background(), sessions.Entry{TranscriptPath: filepath.Join(dir, "gone.jsonl")})
	assert.False(t, ok, "a dangling bind must degrade to not-found")
}
