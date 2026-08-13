package operations

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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

// codexFixturePath, claudeFixturePath resolve to the
// REAL vendor-native fixture files each reader package's own test suite
// already exercises (internal/transcript/vendorreader/<engine>/testdata) — this
// suite reuses them rather than inventing a second, parallel set, per the
// project's fixture-reuse discipline (memory "silent-no-op-failure-mode":
// assert on real payload content, not a hand-rolled minimal stub that could
// never expose a real parse bug).
var (
	codexFixturePath  = filepath.Join(thisDir(), "..", "transcript", "vendorreader", "codex", "testdata", "rollout-fixture.jsonl")
	claudeFixturePath = filepath.Join(thisDir(), "..", "transcript", "vendorreader", "claude", "testdata", "transcript-fixture.jsonl")
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
		EngineVersion:  "2.1.225",
	}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	assert.True(t, converted)

	lines := canonicalLines(t, harp)
	require.NotEmpty(t, lines, "Convert should have produced canonical lines from a real fixture")
	// Every written record must carry the REGISTRY backend name
	// (config.BackendClaudeCode == "claude-code"), not the reader
	// package's own short test-fixture name ("claude") — see
	// vendorReaderRegistry's doc comment for why: RecordOneshot and the live
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
		EngineVersion:  "0.144.4",
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
		EngineVersion:  "0.144.4",
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
		EngineVersion:  "0.144.4",
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
// case above.
func TestConvertVendorTranscript_BestEffortOnFailure(t *testing.T) {
	testsupport.Isolate(t)
	harp := "convert-failure-harp"
	e := sessions.Entry{
		HarpName:       harp,
		Backend:        "codex",
		TranscriptPath: t.TempDir(), // exists (os.Stat succeeds) but is not a file
		EngineVersion:  "0.144.4",
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

// TestVendorReaderRegistry_CoversThreeEngines locks in exactly which backend
// names carry a vendor reader — a change here (adding/removing an engine) should
// be a deliberate, visible edit to this test, not a silent registry drift.
func TestVendorReaderRegistry_CoversThreeEngines(t *testing.T) {
	got := make([]string, 0, len(vendorReaderRegistry))
	for name := range vendorReaderRegistry {
		got = append(got, name)
	}
	assert.ElementsMatch(t, []string{config.BackendClaudeCode, "codex", "kiro"}, got)
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

// rotationFixturePath and liveFixturePath are two REAL claude-code fixtures
// (see thisDir's doc comment on fixture reuse) whose canonical output is
// distinguishable by content: rotationFixturePath (claudeFixturePath,
// reused as the pre-clear segment here) carries the user text "GOAL:
// Truthfulness audit", and liveFixturePath (turn-boundary-fixture.jsonl,
// otherwise only exercised by the claude reader's own test suite) carries the
// distinct assistant TEXT "Connectivity check result: the reach-back path did
// not deliver a message." Markers are message TEXT, not a vendor message id —
// the canonical schema does not preserve the vendor's internal msg.id field,
// so an id-shaped marker would never appear in converted output no matter
// which file it came from. Neither marker appears in the other file (checked
// once, in TestRotationFixtures_MarkersAreDisjoint) — the rebuild tests below
// rely on that disjointness to prove which bytes came from which source.
var (
	rotationFixturePath = claudeFixturePath
	liveFixturePath     = filepath.Join(thisDir(), "..", "transcript", "vendorreader", "claude", "testdata", "turn-boundary-fixture.jsonl")
	rotationMarker      = "GOAL: Truthfulness audit"
	liveMarker          = "Connectivity check result: the reach-back path did not deliver a message"
)

// TestRotationFixtures_MarkersAreDisjoint guards the two fixture-content
// assumptions every rotation-lineage test below builds on: a fixture change
// that broke this pinning would make those tests pass for the wrong reason
// (both markers present in both files) rather than fail loudly.
func TestRotationFixtures_MarkersAreDisjoint(t *testing.T) {
	rotationBytes, err := os.ReadFile(rotationFixturePath)
	require.NoError(t, err)
	liveBytes, err := os.ReadFile(liveFixturePath)
	require.NoError(t, err)

	assert.Contains(t, string(rotationBytes), rotationMarker)
	assert.NotContains(t, string(liveBytes), rotationMarker)
	assert.Contains(t, string(liveBytes), liveMarker)
	assert.NotContains(t, string(rotationBytes), liveMarker)
}

// TestConvertVendorTranscript_RotationLineage_ConcatenatesSegmentAndLive pins
// the harp-lifetime rebuild's core payload contract: the canonical transcript
// produced for a harp with rotation history contains bytes from BOTH the
// cached rotation segment AND the live conversion, segment first. This is the
// fix's central behavior — before it, RefreshVendorTranscript rebuilt from
// ONLY the current binding, so a harp that had been /clear'd lost everything
// before the clear the moment the canonical transcript was (re)built.
//
// Zero-length guard: canonicalLines must be non-empty (require.NotEmpty)
// BEFORE the Contains assertions — two empty reads would trivially "agree" on
// containing nothing, which would pass the Contains checks below for the
// wrong reason.
func TestConvertVendorTranscript_RotationLineage_ConcatenatesSegmentAndLive(t *testing.T) {
	testsupport.Isolate(t)
	harp := "rotation-concat-harp"
	e := sessions.Entry{
		HarpName:       harp,
		Backend:        config.BackendClaudeCode,
		EngineVersion:  "2.1.225",
		TranscriptPath: liveFixturePath, // the CURRENT (post-clear) binding
		Rotations: []sessions.Rotation{
			{SessionID: "pre-clear-id", TranscriptPath: rotationFixturePath, RotatedAt: time.Now()},
		},
	}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	assert.True(t, converted)

	lines := canonicalLines(t, harp)
	require.NotEmpty(t, lines, "the rebuild must produce a non-empty canonical transcript")
	joined := strings.Join(lines, "\n")
	assert.Contains(t, joined, rotationMarker, "the canonical transcript must contain the pre-clear rotation's content")
	assert.Contains(t, joined, liveMarker, "the canonical transcript must contain the live binding's content")

	rotationIdx := strings.Index(joined, rotationMarker)
	liveIdx := strings.Index(joined, liveMarker)
	require.NotEqual(t, -1, rotationIdx)
	require.NotEqual(t, -1, liveIdx)
	assert.Less(t, rotationIdx, liveIdx, "the rotation segment (oldest first) must precede the live conversion")

	// The segment cache must also have been materialized on disk, ready for
	// reuse by a later rebuild.
	segPath, err := paths.ResolveHarpSegmentPath(harp, "pre-clear-id")
	require.NoError(t, err)
	segBytes, err := os.ReadFile(segPath)
	require.NoError(t, err, "the rotation segment must be cached at ResolveHarpSegmentPath")
	assert.Contains(t, string(segBytes), rotationMarker)
}

// TestConvertVendorTranscript_CachedSegmentIsReused pins that a rotation
// segment is converted AT MOST ONCE: once cached, a later rebuild
// (RefreshVendorTranscript) must reuse the cached file rather than re-running
// the vendor adapter over that rotation's transcript again.
//
// Observed by poisoning the cached segment file with recognizable content
// between the two rebuild calls. If the second rebuild REUSES the cache (the
// correct behavior), the poison survives into the final canonical transcript.
// If a mutation makes it reconvert unconditionally, the poison is
// overwritten by a fresh conversion of the real fixture and never appears.
func TestConvertVendorTranscript_CachedSegmentIsReused(t *testing.T) {
	testsupport.Isolate(t)
	harp := "rotation-cache-reuse-harp"
	e := sessions.Entry{
		HarpName:       harp,
		Backend:        config.BackendClaudeCode,
		EngineVersion:  "2.1.225",
		TranscriptPath: liveFixturePath,
		Rotations: []sessions.Rotation{
			{SessionID: "pre-clear-id", TranscriptPath: rotationFixturePath, RotatedAt: time.Now()},
		},
	}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	require.True(t, converted)

	segPath, err := paths.ResolveHarpSegmentPath(harp, "pre-clear-id")
	require.NoError(t, err)
	require.FileExists(t, segPath, "the first rebuild must have cached the segment")

	const poison = `{"v":1,"engine":"claude-code","harp":"` + "rotation-cache-reuse-harp" + `","seq":0,"kind":"raw","raw":{"poisoned":true}}` + "\n"
	require.NoError(t, os.WriteFile(segPath, []byte(poison), 0o644))

	converted, err = RefreshVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	require.True(t, converted)

	lines := canonicalLines(t, harp)
	require.NotEmpty(t, lines)
	joined := strings.Join(lines, "\n")
	assert.Contains(t, joined, "poisoned", "the second rebuild must have REUSED the cached segment, not reconverted it")
	assert.NotContains(t, joined, rotationMarker, "reconverting would have overwritten the poison with the real fixture's content")
	assert.Contains(t, joined, liveMarker, "the live binding must still be converted fresh on every refresh")
}

// TestConvertVendorTranscript_RotationVendorFileGone_SkipsSegmentWithoutFailingRebuild
// pins the per-rotation degrade: one rotation whose vendor file has since
// vanished must be SKIPPED with a diagnostic, not fail the whole rebuild —
// the live binding (and any other, still-present rotation) must still be
// converted.
func TestConvertVendorTranscript_RotationVendorFileGone_SkipsSegmentWithoutFailingRebuild(t *testing.T) {
	testsupport.Isolate(t)
	harp := "rotation-gone-harp"
	gone := filepath.Join(t.TempDir(), "does-not-exist.jsonl")
	e := sessions.Entry{
		HarpName:       harp,
		Backend:        config.BackendClaudeCode,
		EngineVersion:  "2.1.225",
		TranscriptPath: liveFixturePath,
		Rotations: []sessions.Rotation{
			{SessionID: "vanished-id", TranscriptPath: gone, RotatedAt: time.Now()},
		},
	}

	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	defer restore()

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err, "one missing rotation file must not fail the whole rebuild")
	assert.True(t, converted)

	lines := canonicalLines(t, harp)
	require.NotEmpty(t, lines, "the live binding must still be converted")
	assert.Contains(t, strings.Join(lines, "\n"), liveMarker)

	assert.Contains(t, warnings.String(), "vanished-id", "the skipped rotation must be named on the diagnostic channel")
	assert.Contains(t, warnings.String(), harp)
}

// TestConvertVendorTranscript_AllSourcesMissing_SurfacesRatherThanSilentlySucceeding
// pins the project's characteristic silent-no-op guard applied to the
// harp-lifetime rebuild: a harp whose index carries rotation history (proof
// that a pre-clear conversation once existed) but whose live binding AND
// every rotation's vendor file are now gone must return an ERROR, not a quiet
// converted=false success — the latter is indistinguishable from "there was
// never anything to recover" and would send `session recover` right back to
// finding nothing, silently, for good.
func TestConvertVendorTranscript_AllSourcesMissing_SurfacesRatherThanSilentlySucceeding(t *testing.T) {
	testsupport.Isolate(t)
	harp := "rotation-all-missing-harp"
	goneLive := filepath.Join(t.TempDir(), "live-gone.jsonl")
	goneRotation := filepath.Join(t.TempDir(), "rotation-gone.jsonl")
	e := sessions.Entry{
		HarpName:       harp,
		Backend:        "codex",
		EngineVersion:  "0.144.4",
		TranscriptPath: goneLive,
		Rotations: []sessions.Rotation{
			{SessionID: "vanished-id", TranscriptPath: goneRotation, RotatedAt: time.Now()},
		},
	}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	assert.True(t, converted, "a rebuild WAS attempted (rotation history existed)")
	require.Error(t, err, "recovering nothing from a harp with rotation history must surface, not silently succeed")
	assert.Contains(t, err.Error(), harp)
	assert.Contains(t, err.Error(), "1 rotation")

	assert.Nil(t, canonicalLines(t, harp), "no canonical file may be left behind when the rebuild surfaces an error")
}

// TestConvertVendorTranscript_Refresh_ReplacesExistingSymlinkWithARegularFile
// pins that the atomic install correctly replaces transcript.jsonl when it is
// currently a SYMLINK — the shape linkTranscriptIntoHarpDir
// (sessions.BindSession's best-effort convenience link) leaves behind when
// the bound transcript lives outside the harp dir. os.Rename replaces
// whatever is at the destination path, symlink or not, but this pins the
// observable outcome rather than the implementation detail: after a refresh,
// the harp's canonical transcript must be a REGULAR file with the rebuilt
// content, not a symlink to whatever it pointed at before.
func TestConvertVendorTranscript_Refresh_ReplacesExistingSymlinkWithARegularFile(t *testing.T) {
	testsupport.Isolate(t)
	harp := "rotation-symlink-harp"
	e := sessions.Entry{
		HarpName:       harp,
		Backend:        config.BackendClaudeCode,
		EngineVersion:  "2.1.225",
		TranscriptPath: liveFixturePath,
	}

	dest, err := paths.HarpCanonicalTranscriptPath(harp)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(dest), 0o755))
	require.NoError(t, os.Symlink(liveFixturePath, dest))

	fi, lerr := os.Lstat(dest)
	require.NoError(t, lerr)
	require.True(t, fi.Mode()&os.ModeSymlink != 0, "fixture setup: dest must start out as a symlink")

	converted, err := RefreshVendorTranscript(context.Background(), e)
	require.NoError(t, err)
	assert.True(t, converted)

	fi, lerr = os.Lstat(dest)
	require.NoError(t, lerr)
	assert.Equal(t, os.FileMode(0), fi.Mode()&os.ModeSymlink, "the rebuild must have replaced the symlink with a regular file")

	lines := canonicalLines(t, harp)
	require.NotEmpty(t, lines)
	assert.Contains(t, strings.Join(lines, "\n"), liveMarker)
}
