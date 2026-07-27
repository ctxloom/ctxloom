package config

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// BuiltinCompanionBins is now the UNION of DiscoverCompanions' first-party
// list (ltk, taskloom, reprise — S8 companion loadout discovery) with
// whatever an embedded built-in bundle's hooks/MCP still reference (none
// today: S8 moved ltk/taskloom off the embedded-bundle mechanism onto their
// own loadouts). reprise is included even though it doesn't implement
// `loadout` yet — see firstPartyCompanions' doc comment.
func TestBuiltinCompanionBins_UnionsDiscoveryWithEmbeddedBundles(t *testing.T) {
	bins := BuiltinCompanionBins()
	assert.Equal(t, []string{"ltk", "reprise", "taskloom"}, bins, "sorted, deduped, ctxloom excluded")
}

func TestProbeCompanions_ReportsVersionFromJSONProbe(t *testing.T) {
	restoreLook := SetLookPathForTesting(func(bin string) (string, error) {
		return "/usr/bin/" + bin, nil
	})
	defer restoreLook()
	// Probes run concurrently, so guard the collection.
	var probedMu sync.Mutex
	var probed []string
	restoreProbe := SetCompanionVersionOutputForTesting(func(path string) ([]byte, error) {
		probedMu.Lock()
		probed = append(probed, path)
		probedMu.Unlock()
		return []byte(`{"name":"x","version":"v1.2.3"}`), nil
	})
	defer restoreProbe()

	statuses := ProbeCompanions()
	require.Len(t, statuses, 3)
	for _, st := range statuses {
		assert.Equal(t, "/usr/bin/"+st.Bin, st.Path)
		assert.Equal(t, "v1.2.3", st.Version)
		assert.NoError(t, st.Err)
	}
	assert.ElementsMatch(t, []string{"/usr/bin/ltk", "/usr/bin/reprise", "/usr/bin/taskloom"}, probed)
}

func TestProbeCompanions_MissingBinaryYieldsEmptyPathAndNoProbe(t *testing.T) {
	restoreLook := SetLookPathForTesting(func(string) (string, error) {
		return "", exec.ErrNotFound
	})
	defer restoreLook()
	restoreProbe := SetCompanionVersionOutputForTesting(func(string) ([]byte, error) {
		t.Fatal("version probe must not run for a missing binary")
		return nil, nil
	})
	defer restoreProbe()

	for _, st := range ProbeCompanions() {
		assert.Empty(t, st.Path)
		assert.Empty(t, st.Version)
		assert.NoError(t, st.Err, "missing is a state, not a probe error")
	}
}

func TestProbeCompanions_ProbeFailureIsNonFatal(t *testing.T) {
	restoreLook := SetLookPathForTesting(func(bin string) (string, error) {
		return "/usr/bin/" + bin, nil
	})
	defer restoreLook()

	t.Run("exec error", func(t *testing.T) {
		restore := SetCompanionVersionOutputForTesting(func(string) ([]byte, error) {
			return nil, errors.New("boom")
		})
		defer restore()
		for _, st := range ProbeCompanions() {
			assert.NotEmpty(t, st.Path, "binary still counts as present")
			assert.Error(t, st.Err)
		}
	})

	t.Run("unparseable output", func(t *testing.T) {
		restore := SetCompanionVersionOutputForTesting(func(string) ([]byte, error) {
			return []byte("not json"), nil
		})
		defer restore()
		for _, st := range ProbeCompanions() {
			assert.Error(t, st.Err)
			assert.Empty(t, st.Version)
		}
	})

	t.Run("missing version field", func(t *testing.T) {
		restore := SetCompanionVersionOutputForTesting(func(string) ([]byte, error) {
			return []byte(`{"name":"ltk"}`), nil
		})
		defer restore()
		for _, st := range ProbeCompanions() {
			assert.Error(t, st.Err)
		}
	})
}

// writeFakeCompanion drops an executable shell script named name into dir and
// returns its path.
func writeFakeCompanion(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	return path
}

// shrinkProbeDurations shortens the probe timeout and wait-delay so the
// stall-path tests run in milliseconds, restoring them on cleanup.
func shrinkProbeDurations(t *testing.T, timeout, waitDelay time.Duration) {
	t.Helper()
	origTimeout, origDelay := companionProbeTimeout, companionProbeWaitDelay
	companionProbeTimeout, companionProbeWaitDelay = timeout, waitDelay
	t.Cleanup(func() {
		companionProbeTimeout, companionProbeWaitDelay = origTimeout, origDelay
	})
}

// TestCompanionVersionOutput_RealExec exercises the production seam body —
// the actual `version --format json` argv, the probe timeout, and the
// WaitDelay pipe bound — against fake companion binaries. Every other test
// replaces the seam, so without this the real exec path has zero coverage.
func TestCompanionVersionOutput_RealExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake companions are sh scripts")
	}
	dir := t.TempDir()

	t.Run("valid JSON output", func(t *testing.T) {
		// The script proves the argv contract too: it only answers the
		// version when called exactly as `version --format json`.
		bin := writeFakeCompanion(t, dir, "goodtool",
			`[ "$1 $2 $3" = "version --format json" ] || exit 2
echo '{"name":"goodtool","version":"v9.9.9"}'`)
		version, err := companionVersion(bin)
		require.NoError(t, err)
		assert.Equal(t, "v9.9.9", version)
	})

	t.Run("wedged companion hits the probe timeout", func(t *testing.T) {
		shrinkProbeDurations(t, 200*time.Millisecond, 100*time.Millisecond)
		bin := writeFakeCompanion(t, dir, "wedged", "sleep 30")
		start := time.Now()
		_, err := companionVersion(bin)
		require.Error(t, err, "a wedged companion must fail the probe, not hang it")
		assert.Less(t, time.Since(start), 5*time.Second, "probe must be bounded by timeout+WaitDelay")
	})

	t.Run("grandchild holding stdout is bounded by WaitDelay", func(t *testing.T) {
		shrinkProbeDurations(t, 200*time.Millisecond, 100*time.Millisecond)
		// The direct child exits immediately but leaves a background
		// grandchild holding the inherited stdout pipe open. Without
		// cmd.WaitDelay, Output blocks until that pipe closes (30s here;
		// forever for a daemonized companion) and startup stalls.
		bin := writeFakeCompanion(t, dir, "spawner", "sleep 30 &\nexit 0")
		start := time.Now()
		_, err := companionVersion(bin)
		require.Error(t, err)
		assert.Less(t, time.Since(start), 5*time.Second, "WaitDelay must cut the pipe wait short")
	})
}

// ==========================================================================
// Companion disable switch (--no-companions / CTXLOOM_NO_COMPANIONS)
// ==========================================================================

// TestCompanionsDisabled_SkipsProbeEntirely is the contract of the switch:
// when companions are disabled process-wide, discovery must not run at all —
// no companion subprocess is executed and no loadout is contributed. Probing
// execs whatever companion binaries sit on the host PATH, so this is what makes
// a run (and CI) reproducible regardless of the machine.
func TestCompanionsDisabled_SkipsProbeEntirely(t *testing.T) {
	t.Cleanup(func() { SetCompanionsDisabled(false) })

	probed := 0
	cfg := &Config{appPaths: []string{t.TempDir()}}
	cfg.companionProbe = func(signing.TrustRoot) map[string]*bundles.Bundle {
		probed++
		return map[string]*bundles.Bundle{"ctxloom:companion@ltk": {Name: "ltk"}}
	}

	SetCompanionsDisabled(true)

	assert.Empty(t, cfg.companionBundleSeed(), "no companion loadout may be contributed when disabled")
	assert.Zero(t, probed, "the probe must not be executed at all when companions are disabled")
}

// TestProbeCompanions_DisabledYieldsNothing (U047-F01) proves ProbeCompanions
// itself honours the switch at the exec boundary — the prior gate
// (TestCompanionsDisabled_SkipsProbeEntirely) only proved
// companionBundleSeed's OWN check short-circuits before ever calling the
// probe; it never proved ProbeCompanions would refuse to run if something
// else called it directly, which is exactly what reportCompanions
// (cli/startup_helpers.go, called unconditionally from `ctxloom run`/`ctxloom
// mcp`) did.
func TestProbeCompanions_DisabledYieldsNothing(t *testing.T) {
	t.Cleanup(func() { SetCompanionsDisabled(false) })
	restoreLook := SetLookPathForTesting(func(bin string) (string, error) {
		t.Fatal("lookPath must not run when companions are disabled")
		return "", nil
	})
	defer restoreLook()

	SetCompanionsDisabled(true)
	assert.Empty(t, ProbeCompanions(), "no companion binary may be probed when disabled")
}

// TestProbeCompanionLoadouts_DisabledYieldsNothing is ProbeCompanionLoadouts'
// half of U047-F01: the loadout probe execs companion binaries too, and had
// no gate of its own either.
func TestProbeCompanionLoadouts_DisabledYieldsNothing(t *testing.T) {
	t.Cleanup(func() { SetCompanionsDisabled(false) })
	restoreLook := SetLookPathForTesting(func(bin string) (string, error) {
		t.Fatal("lookPath must not run when companions are disabled")
		return "", nil
	})
	defer restoreLook()

	SetCompanionsDisabled(true)
	assert.Empty(t, ProbeCompanionLoadouts(nil), "no loadout may be probed when disabled")
}

// syncBuffer is a mutex-guarded bytes.Buffer: ProbeCompanionLoadouts fans its
// per-companion probes out across goroutines, so a plain bytes.Buffer as the
// clidiag sink races when more than one probe warns concurrently.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestProbeCompanionLoadouts_WedgedCompanionWarns (U047-F04) proves a
// loadout probe failure that is NOT the ordinary "no loadout subcommand"
// case (a wedged companion timing out, or any other exec failure) is
// reported — previously it vanished with zero diagnostic and the run
// reported success having delivered nothing from that companion.
func TestProbeCompanionLoadouts_WedgedCompanionWarns(t *testing.T) {
	restoreLook := SetLookPathForTesting(func(bin string) (string, error) {
		return "/usr/bin/" + bin, nil
	})
	defer restoreLook()
	restoreLoadout := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	})
	defer restoreLoadout()

	buf := &syncBuffer{}
	restoreSink := clidiag.SetSink(buf)
	defer restoreSink()

	out := ProbeCompanionLoadouts(nil)
	assert.Empty(t, out, "a wedged companion still contributes nothing")
	assert.Contains(t, buf.String(), "loadout probe failed", "a non-benign failure must be diagnosed, not silent")
}

// TestProbeCompanionLoadouts_UnknownSubcommandStaysQuiet is the contrast: a
// companion that hasn't adopted the `loadout` protocol yet (ordinary
// *exec.ExitError, e.g. "unknown command") must NOT warn — that is the
// common, benign case U047-F04's fix must not turn noisy.
func TestProbeCompanionLoadouts_UnknownSubcommandStaysQuiet(t *testing.T) {
	restoreLook := SetLookPathForTesting(func(bin string) (string, error) {
		return "/usr/bin/" + bin, nil
	})
	defer restoreLook()
	restoreLoadout := SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) {
		return nil, &exec.ExitError{}
	})
	defer restoreLoadout()

	buf := &syncBuffer{}
	restoreSink := clidiag.SetSink(buf)
	defer restoreSink()

	out := ProbeCompanionLoadouts(nil)
	assert.Empty(t, out)
	assert.Empty(t, buf.String(), "an unadopted loadout subcommand is the ordinary case, not a warning")
}

// TestCompanionsEnabled_ProbesByDefault is the converse: the default is on, so
// the switch cannot silently suppress companions for everyone.
func TestCompanionsEnabled_ProbesByDefault(t *testing.T) {
	t.Cleanup(func() { SetCompanionsDisabled(false) })
	SetCompanionsDisabled(false)

	probed := 0
	cfg := &Config{appPaths: []string{t.TempDir()}}
	cfg.companionProbe = func(signing.TrustRoot) map[string]*bundles.Bundle {
		probed++
		return map[string]*bundles.Bundle{"ctxloom:companion@ltk": {Name: "ltk"}}
	}

	assert.Len(t, cfg.companionBundleSeed(), 1)
	assert.Equal(t, 1, probed)
}

// TestDisableCompanionProbe_BeatsGlobalEnabled pins the precedence a test needs:
// the per-Config seam wins over the process-wide switch, so a parallel test can
// pin its own fixture without depending on (or clobbering) global state.
func TestDisableCompanionProbe_BeatsGlobalEnabled(t *testing.T) {
	t.Cleanup(func() { SetCompanionsDisabled(false) })
	SetCompanionsDisabled(false) // companions ON process-wide

	cfg := &Config{appPaths: []string{t.TempDir()}}
	cfg.DisableCompanionProbe()

	assert.Empty(t, cfg.companionBundleSeed(),
		"a Config that disabled its own probe must see no companions even when the process has them enabled")
}
